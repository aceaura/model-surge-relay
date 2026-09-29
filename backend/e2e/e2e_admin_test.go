package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-relay/backend/contract/relayv1"
	"github.com/aceaura/model-surge-relay/backend/migrate"
)

// TestEndToEndMigratePolicies 一次性迁移全链路：遗产表里的两个示例脚本
// （failover + compact_overflow）映射为组合配置，报告分类正确，且可重复执行。
func TestEndToEndMigratePolicies(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// c1：failover 引用；c2：compact_overflow 引用（主池 kimi + 压缩池 compact）。
	e.mustAdmin(t, http.MethodPost, "/admin/collections", `{"name":"c1"}`, http.StatusCreated)
	e.mustAdmin(t, http.MethodPost, "/admin/collections/c1/groups",
		`{"name":"primary","type":"fast","position":0}`, http.StatusCreated)
	e.mustAdmin(t, http.MethodPost, "/admin/collections/c1/groups",
		`{"name":"backup","type":"cheap","position":1}`, http.StatusCreated)
	e.mustAdmin(t, http.MethodPost, "/admin/collections", `{"name":"c2"}`, http.StatusCreated)
	e.mustAdmin(t, http.MethodPost, "/admin/collections/c2/groups",
		`{"name":"main","type":"kimi","position":0}`, http.StatusCreated)
	e.mustAdmin(t, http.MethodPost, "/admin/collections/c2/groups",
		`{"name":"comp","type":"compact","position":1}`, http.StatusCreated)
	e.mustAdmin(t, http.MethodPut, "/admin/collections/c2/groups/main/members",
		`{"members":["kimi-1/k3"]}`, http.StatusNoContent)
	e.mustAdmin(t, http.MethodPut, "/admin/collections/c2/groups/comp/members",
		`{"members":["ark-1/ds"]}`, http.StatusNoContent)

	// 遗产数据只能走 SQL：管理面已不再接受 policy 字段。
	if _, err := e.pool.Pool().Exec(ctx,
		`INSERT INTO policies (name, language, source) VALUES
			('failover', 'lua', 'return {}'),
			('compact_overflow', 'javascript', '// compact')`); err != nil {
		t.Fatalf("seed policies: %v", err)
	}
	if _, err := e.pool.Pool().Exec(ctx,
		`INSERT INTO user_models (name, collection, policy, client_key, enabled) VALUES
			('m1', 'c1', 'failover', 'sk-1', true),
			('m2', 'c1', 'failover', 'sk-2', true),
			('m3', 'c2', 'compact_overflow', 'sk-3', true)`); err != nil {
		t.Fatalf("seed user models: %v", err)
	}

	status, body := e.admin(t, http.MethodPost, "/admin/migrate-policies", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var report migrate.Report
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if len(report.Mapped) != 2 || len(report.Unmapped) != 0 || len(report.Conflicts) != 0 {
		t.Fatalf("report = %+v, want two clean mappings", report)
	}

	// c1：failover → 组顺序链。
	got := e.mustAdmin(t, http.MethodGet, "/admin/collections/c1/strategy", "", http.StatusOK)
	if !strings.Contains(got, `"priority_chain":["primary","backup"]`) {
		t.Fatalf("c1 strategy = %s, want group-order chain", got)
	}

	// c2：compact_overflow → 主池链 + 压缩托管（阈值取主池成员最大窗口 262144）。
	got = e.mustAdmin(t, http.MethodGet, "/admin/collections/c2/strategy", "", http.StatusOK)
	for _, want := range []string{
		`"priority_chain":["main"]`, `"enabled":true`,
		`"threshold_tokens":262144`, `"compact_groups":["comp"]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("c2 strategy = %s, missing %s", got, want)
		}
	}

	// 迁移可重复执行且幂等：第二次报告仍是一对一映射。
	status, body = e.admin(t, http.MethodPost, "/admin/migrate-policies", "")
	if status != http.StatusOK {
		t.Fatalf("re-run status = %d: %s", status, body)
	}
	var rerun migrate.Report
	if err := json.Unmarshal([]byte(body), &rerun); err != nil {
		t.Fatalf("unmarshal rerun report: %v", err)
	}
	if len(rerun.Mapped) != 2 || len(rerun.Unmapped) != 0 {
		t.Fatalf("rerun report = %+v, want the same two mappings", rerun)
	}
	got = e.mustAdmin(t, http.MethodGet, "/admin/collections/c2/strategy", "", http.StatusOK)
	if !strings.Contains(got, `"threshold_tokens":262144`) {
		t.Fatalf("c2 strategy after rerun = %s, want idempotent", got)
	}
}

// TestEndToEndMigrateReportsMajorityConflict 同一集合被多个策略引用时
// 取多数（R4.2），竞争名单入报告。
func TestEndToEndMigrateReportsMajorityConflict(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	e.mustAdmin(t, http.MethodPost, "/admin/collections", `{"name":"c1"}`, http.StatusCreated)
	e.mustAdmin(t, http.MethodPost, "/admin/collections/c1/groups",
		`{"name":"primary","type":"fast","position":0}`, http.StatusCreated)
	if _, err := e.pool.Pool().Exec(ctx,
		`INSERT INTO policies (name, language, source) VALUES
			('failover', 'lua', 'return {}'),
			('homegrown', 'lua', 'return {}')`); err != nil {
		t.Fatalf("seed policies: %v", err)
	}
	if _, err := e.pool.Pool().Exec(ctx,
		`INSERT INTO user_models (name, collection, policy, client_key, enabled) VALUES
			('m1', 'c1', 'failover', 'sk-1', true),
			('m2', 'c1', 'failover', 'sk-2', true),
			('m3', 'c1', 'homegrown', 'sk-3', true)`); err != nil {
		t.Fatalf("seed user models: %v", err)
	}

	status, body := e.admin(t, http.MethodPost, "/admin/migrate-policies", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var report migrate.Report
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if len(report.Conflicts) != 1 || report.Conflicts[0].Chosen != "failover" {
		t.Fatalf("conflicts = %+v, want failover chosen by majority", report.Conflicts)
	}
	if len(report.Mapped) != 1 || len(report.Unmapped) != 0 {
		t.Fatalf("report = %+v, want the majority winner mapped", report)
	}
}

// TestEndToEndMigrateUnmappedScriptWritesDefaultChain 识别不了的脚本
// 不阻断迁移：集合写默认链，策略名入报告待人工跟进。
func TestEndToEndMigrateUnmappedScriptWritesDefaultChain(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	e.mustAdmin(t, http.MethodPost, "/admin/collections", `{"name":"c1"}`, http.StatusCreated)
	e.mustAdmin(t, http.MethodPost, "/admin/collections/c1/groups",
		`{"name":"primary","type":"fast","position":0}`, http.StatusCreated)
	if _, err := e.pool.Pool().Exec(ctx,
		`INSERT INTO policies (name, language, source) VALUES ('homegrown', 'lua', 'return {}')`); err != nil {
		t.Fatalf("seed policies: %v", err)
	}
	if _, err := e.pool.Pool().Exec(ctx,
		`INSERT INTO user_models (name, collection, policy, client_key, enabled) VALUES
			('m1', 'c1', 'homegrown', 'sk-1', true)`); err != nil {
		t.Fatalf("seed user models: %v", err)
	}

	status, body := e.admin(t, http.MethodPost, "/admin/migrate-policies", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var report migrate.Report
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if len(report.Unmapped) != 1 || report.Unmapped[0].Policy != "homegrown" {
		t.Fatalf("unmapped = %+v, want homegrown listed", report.Unmapped)
	}
	got := e.mustAdmin(t, http.MethodGet, "/admin/collections/c1/strategy", "", http.StatusOK)
	if !strings.Contains(got, `"priority_chain":["primary"]`) {
		t.Fatalf("c1 strategy = %s, want the default chain", got)
	}
}

func TestEndToEndDispatchAuthFailures(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"unknown model", `{"model":"ghost","client_key":"sk-client","request_id":"r"}`, http.StatusNotFound},
		{"wrong key", `{"model":"sonnet","client_key":"nope","request_id":"r"}`, http.StatusUnauthorized},
		{"wrong protocol", `{"model":"sonnet","inbound_protocol":"gemini","client_key":"sk-client","request_id":"r"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := e.internal(t, http.MethodPost, relayv1.DispatchBasePath+"/dispatch", tc.body)
			if status != tc.want {
				t.Fatalf("status = %d, want %d: %s", status, tc.want, body)
			}
		})
	}
}

func TestEndToEndDisabledUserModelRejected(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	e.mustAdmin(t, http.MethodPut, "/admin/user-models/sonnet",
		`{"collection":"c1","client_key":"sk-client","protocol":"anthropic","enabled":false}`,
		http.StatusOK)

	status, _, body := e.dispatch(t, "req-1", nil)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", status, body)
	}
}

func TestEndToEndHealthReportsComponents(t *testing.T) {
	e := newEnv(t)
	// 不带任何密钥：健康检查免鉴权。
	status, body := e.request(t, http.MethodGet, relayv1.HealthPath, "", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var got relayv1.HealthResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Ready || !got.Database || !got.Upstream {
		t.Fatalf("health = %+v", got)
	}
	if wantCache := os.Getenv("TEST_REDIS_ADDR") != ""; got.Cache != wantCache {
		t.Fatalf("cache = %v, want %v", got.Cache, wantCache)
	}
}

func TestEndToEndModelsListing(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	status, body := e.internal(t, http.MethodGet, relayv1.DispatchBasePath+"/models", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var got relayv1.ModelsResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "sonnet" {
		t.Fatalf("models = %+v", got.Models)
	}
	if strings.Contains(body, "sk-client") {
		t.Fatalf("listing leaks the client key: %s", body)
	}
}

func TestEndToEndKeyIsolation(t *testing.T) {
	e := newEnv(t)
	status, _ := e.request(t, http.MethodGet, "/admin/collections", "",
		map[string]string{"Authorization": "Bearer " + dispatchKey})
	if status != http.StatusUnauthorized {
		t.Fatalf("dispatch key on admin = %d, want 401", status)
	}
	status, _ = e.request(t, http.MethodGet, relayv1.DispatchBasePath+"/models", "",
		map[string]string{"Authorization": "Bearer " + adminKey})
	if status != http.StatusUnauthorized {
		t.Fatalf("admin key on dispatch = %d, want 401", status)
	}
	// 健康检查在两面之外：无密钥可达。
	status, _ = e.request(t, http.MethodGet, relayv1.HealthPath, "", nil)
	if status != http.StatusOK {
		t.Fatalf("healthz without key = %d, want 200", status)
	}
}
