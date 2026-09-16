package usermodel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/cache"
	"github.com/aceaura/model-surge-relay/backend/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE user_models, policies, group_members, groups, collections RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO collections (name) VALUES ('c1'), ('c2')`); err != nil {
		t.Fatalf("seed collections: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO policies (name, language, source) VALUES ('p1', 'lua', 'return {}')`); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	return s.Pool()
}

func newRepo(t *testing.T) *Repo {
	return NewRepo(testPool(t), cache.New(nil, time.Minute))
}

func seed(t *testing.T, r *Repo, m UserModel) UserModel {
	t.Helper()
	got, err := r.Create(context.Background(), m)
	if err != nil {
		t.Fatalf("create %s: %v", m.Name, err)
	}
	return got
}

func TestCreateReturnsCompleteRecord(t *testing.T) {
	r := newRepo(t)
	got := seed(t, r, UserModel{
		Name: "sonnet", Collection: "c1", Policy: "p1",
		ClientKey: "sk-1", Protocol: "anthropic", Enabled: true,
	})
	if got.Collection != "c1" || got.Policy != "p1" || got.Protocol != "anthropic" || !got.Enabled {
		t.Fatalf("record = %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("created_at missing")
	}
}

func TestCreateAllowsUnboundPolicy(t *testing.T) {
	r := newRepo(t)
	got := seed(t, r, UserModel{Name: "plain", Collection: "c1", ClientKey: "sk", Enabled: true})
	if got.Policy != "" {
		t.Fatalf("policy = %q, want empty", got.Policy)
	}
}

func TestCreateRejectsUnknownCollection(t *testing.T) {
	r := newRepo(t)
	_, err := r.Create(context.Background(),
		UserModel{Name: "x", Collection: "ghost", ClientKey: "sk", Enabled: true})
	e := asAppErr(t, err)
	if e.Code != apperr.InvalidRequest || e.Field != "collection" {
		t.Fatalf("error = %+v, want invalid_request on collection", e)
	}
}

func TestCreateRejectsUnknownPolicy(t *testing.T) {
	r := newRepo(t)
	_, err := r.Create(context.Background(),
		UserModel{Name: "x", Collection: "c1", Policy: "ghost", ClientKey: "sk", Enabled: true})
	e := asAppErr(t, err)
	if e.Code != apperr.InvalidRequest || e.Field != "policy" {
		t.Fatalf("error = %+v, want invalid_request on policy", e)
	}
}

func TestCreateRejectsMissingFields(t *testing.T) {
	r := newRepo(t)
	cases := []struct {
		name  string
		in    UserModel
		field string
	}{
		{"no name", UserModel{Collection: "c1", ClientKey: "sk"}, "name"},
		{"no collection", UserModel{Name: "x", ClientKey: "sk"}, "collection"},
		{"no client key", UserModel{Name: "x", Collection: "c1"}, "client_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Create(context.Background(), tc.in)
			e := asAppErr(t, err)
			if e.Code != apperr.InvalidRequest || e.Field != tc.field {
				t.Fatalf("error = %+v, want field %q", e, tc.field)
			}
		})
	}
}

func TestCreateRejectsDuplicateName(t *testing.T) {
	r := newRepo(t)
	seed(t, r, UserModel{Name: "dup", Collection: "c1", ClientKey: "sk", Enabled: true})
	_, err := r.Create(context.Background(),
		UserModel{Name: "dup", Collection: "c2", ClientKey: "sk", Enabled: true})
	if e := asAppErr(t, err); e.Code != apperr.Conflict {
		t.Fatalf("code = %s, want conflict", e.Code)
	}
}

func TestUpdateReplacesBinding(t *testing.T) {
	r := newRepo(t)
	seed(t, r, UserModel{Name: "m", Collection: "c1", Policy: "p1", ClientKey: "sk", Enabled: true})
	got, err := r.Update(context.Background(),
		UserModel{Name: "m", Collection: "c2", ClientKey: "sk2", Enabled: false})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Collection != "c2" || got.Policy != "" || got.Enabled {
		t.Fatalf("record = %+v", got)
	}
}

func TestUpdateMissingIsNotFound(t *testing.T) {
	r := newRepo(t)
	_, err := r.Update(context.Background(),
		UserModel{Name: "ghost", Collection: "c1", ClientKey: "sk"})
	if e := asAppErr(t, err); e.Code != apperr.NotFound {
		t.Fatalf("code = %s, want not_found", e.Code)
	}
}

func TestListAndDelete(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	seed(t, r, UserModel{Name: "b", Collection: "c1", ClientKey: "sk", Enabled: true})
	seed(t, r, UserModel{Name: "a", Collection: "c1", ClientKey: "sk", Enabled: true})
	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].Name != "a" {
		t.Fatalf("list = %+v, want name order", list)
	}
	if err := r.Delete(ctx, "a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.load(ctx, "a"); !isNotFound(err) {
		t.Fatalf("record should be gone, got %v", err)
	}
	if e := asAppErr(t, r.Delete(ctx, "a")); e.Code != apperr.NotFound {
		t.Fatalf("code = %s, want not_found", e.Code)
	}
}

func TestAuthenticateSucceedsAndStripsClientKey(t *testing.T) {
	r := newRepo(t)
	seed(t, r, UserModel{Name: "m", Collection: "c1", Policy: "p1",
		ClientKey: "sk-live", Protocol: "anthropic", Enabled: true})
	got, err := r.Authenticate(context.Background(), "m", "anthropic", "sk-live")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if got.ClientKey != "" {
		t.Fatal("authenticate must not hand the client key back to the caller")
	}
	if got.Policy != "p1" || got.Collection != "c1" {
		t.Fatalf("record = %+v", got)
	}
}

func TestAuthenticateUnknownModelIsNotFound(t *testing.T) {
	r := newRepo(t)
	_, err := r.Authenticate(context.Background(), "ghost", "anthropic", "sk")
	if e := asAppErr(t, err); e.Code != apperr.NotFound {
		t.Fatalf("code = %s, want not_found", e.Code)
	}
}

func TestAuthenticateDisabledModel(t *testing.T) {
	r := newRepo(t)
	seed(t, r, UserModel{Name: "off", Collection: "c1", ClientKey: "sk", Enabled: false})
	_, err := r.Authenticate(context.Background(), "off", "", "sk")
	if e := asAppErr(t, err); e.Code != apperr.Disabled {
		t.Fatalf("code = %s, want disabled", e.Code)
	}
}

func TestAuthenticateKeyMismatch(t *testing.T) {
	r := newRepo(t)
	seed(t, r, UserModel{Name: "m", Collection: "c1", ClientKey: "sk-right", Enabled: true})
	_, err := r.Authenticate(context.Background(), "m", "", "sk-wrong")
	e := asAppErr(t, err)
	if e.Code != apperr.Unauthorized {
		t.Fatalf("code = %s, want unauthorized", e.Code)
	}
	if strings.Contains(e.Message, "sk-right") || strings.Contains(e.Message, "sk-wrong") {
		t.Fatalf("message %q must not echo keys", e.Message)
	}
}

func TestAuthenticateProtocolMismatch(t *testing.T) {
	r := newRepo(t)
	seed(t, r, UserModel{Name: "m", Collection: "c1", ClientKey: "sk",
		Protocol: "anthropic", Enabled: true})
	_, err := r.Authenticate(context.Background(), "m", "chat_completions", "sk")
	e := asAppErr(t, err)
	if e.Code != apperr.InvalidRequest || e.Field != "inbound_protocol" {
		t.Fatalf("error = %+v", e)
	}
}

func TestAuthenticateUnsetProtocolAcceptsAny(t *testing.T) {
	r := newRepo(t)
	seed(t, r, UserModel{Name: "m", Collection: "c1", ClientKey: "sk", Enabled: true})
	if _, err := r.Authenticate(context.Background(), "m", "responses", "sk"); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
}

// TestClientKeyNeverSerializes 断言密钥不会随 JSON 输出流到响应或日志里。
func TestClientKeyNeverSerializes(t *testing.T) {
	raw, err := json.Marshal(UserModel{Name: "m", Collection: "c1", ClientKey: "sk-secret"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "sk-secret") || strings.Contains(string(raw), "client_key") {
		t.Fatalf("serialized form leaks the client key: %s", raw)
	}
}

// TestAuthenticateSucceedsOnCacheHit 回归测试：ClientKey 标了 json:"-"，
// 若缓存副本按对外形态序列化就会丢掉密钥，命中缓存后鉴权全数失败。
func TestAuthenticateSucceedsOnCacheHit(t *testing.T) {
	r := NewRepo(testPool(t), cache.New(newMemBackend(), time.Hour))
	ctx := context.Background()
	seed(t, r, UserModel{Name: "m", Collection: "c1", ClientKey: "sk-live", Enabled: true})

	if _, err := r.Authenticate(ctx, "m", "", "sk-live"); err != nil {
		t.Fatalf("first authenticate (cache miss): %v", err)
	}
	if _, err := r.Authenticate(ctx, "m", "", "sk-live"); err != nil {
		t.Fatalf("second authenticate (cache hit): %v", err)
	}
}

func TestUpdateInvalidatesCachedCopy(t *testing.T) {
	r := NewRepo(testPool(t), cache.New(newMemBackend(), time.Hour))
	ctx := context.Background()
	seed(t, r, UserModel{Name: "m", Collection: "c1", ClientKey: "sk-old", Enabled: true})
	if _, err := r.Authenticate(ctx, "m", "", "sk-old"); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	if _, err := r.Update(ctx, UserModel{Name: "m", Collection: "c1", ClientKey: "sk-new", Enabled: true}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := r.Authenticate(ctx, "m", "", "sk-new"); err != nil {
		t.Fatalf("rotated key must take effect immediately: %v", err)
	}
	if _, err := r.Authenticate(ctx, "m", "", "sk-old"); err == nil {
		t.Fatal("old key must stop working after rotation")
	}
}

type memBackend struct{ data map[string][]byte }

func newMemBackend() *memBackend { return &memBackend{data: map[string][]byte{}} }

func (b *memBackend) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := b.data[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	return v, nil
}

func (b *memBackend) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	b.data[key] = value
	return nil
}

func (b *memBackend) Del(_ context.Context, key string) error {
	delete(b.data, key)
	return nil
}

func (b *memBackend) Ready(context.Context) bool { return true }

func asAppErr(t *testing.T, err error) *apperr.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var e *apperr.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not *apperr.Error", err)
	}
	return e
}

func isNotFound(err error) bool {
	var e *apperr.Error
	return errors.As(err, &e) && e.Code == apperr.NotFound
}
