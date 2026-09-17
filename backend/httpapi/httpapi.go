// Package httpapi 挂载调度面与管理面路由。
//
// 两把密钥挂在不同的前缀子树上：隔离由路由结构保证，而不是靠每个 handler 自觉。
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/contract/relayv1"
	"github.com/aceaura/model-surge-relay/backend/dispatch"
	"github.com/aceaura/model-surge-relay/backend/policy"
	"github.com/aceaura/model-surge-relay/backend/runstate"
	"github.com/aceaura/model-surge-relay/backend/usermodel"
)

// Health 是三方状态探测能力；PG 不可用即整体未就绪。
type Health interface {
	PingDatabase(ctx context.Context) error
	CacheReady(ctx context.Context) bool
	UpstreamReady(ctx context.Context) bool
}

type Collections interface {
	Create(ctx context.Context, name, note string) (collection.Collection, error)
	Get(ctx context.Context, name string) (collection.Collection, error)
	List(ctx context.Context) ([]collection.Collection, error)
	UpdateNote(ctx context.Context, name, note string) error
	Delete(ctx context.Context, name string) error
	CreateGroup(ctx context.Context, g collection.Group) (collection.Group, error)
	UpdateGroup(ctx context.Context, g collection.Group) error
	DeleteGroup(ctx context.Context, collectionName, groupName string) error
	Groups(ctx context.Context, collectionName string) ([]collection.Group, error)
	ReplaceMembers(ctx context.Context, collectionName, groupName string, modelIDs []string) error
	Snapshot(ctx context.Context, name string) (collection.Snapshot, error)
}

type Policies interface {
	Create(ctx context.Context, p policy.Policy) (policy.Policy, error)
	Update(ctx context.Context, p policy.Policy) (policy.Policy, error)
	Get(ctx context.Context, name string) (policy.Policy, error)
	List(ctx context.Context) ([]policy.Policy, error)
	Delete(ctx context.Context, name string) error
}

type PolicyEngine interface {
	Execute(ctx context.Context, p policy.Policy, in policy.Input) (policy.Decision, error)
}

type UserModels interface {
	Create(ctx context.Context, m usermodel.UserModel) (usermodel.UserModel, error)
	Update(ctx context.Context, m usermodel.UserModel) (usermodel.UserModel, error)
	Get(ctx context.Context, name string) (usermodel.UserModel, error)
	List(ctx context.Context) ([]usermodel.UserModel, error)
	Delete(ctx context.Context, name string) error
}

type RunStates interface {
	List(ctx context.Context) ([]runstate.State, error)
	Reset(ctx context.Context, modelID string) error
}

type Server struct {
	Dispatch    *dispatch.Service
	Health      Health
	Collections Collections
	Policies    Policies
	Engine      PolicyEngine
	UserModels  UserModels
	RunStates   RunStates
	DispatchKey string
	AdminKey    string
}

func (s *Server) Handler() http.Handler {
	dispatch := http.NewServeMux()
	dispatch.HandleFunc("GET "+relayv1.DispatchBasePath+"/models", s.models)
	dispatch.HandleFunc("POST "+relayv1.DispatchBasePath+"/dispatch", s.dispatch)
	dispatch.HandleFunc("POST "+relayv1.DispatchBasePath+"/results", s.results)

	admin := http.NewServeMux()
	s.routeAdmin(admin)

	root := http.NewServeMux()
	// 健康检查免鉴权：探活的调用方（编排、探针）不该持有任何密钥。
	root.HandleFunc("GET "+relayv1.HealthPath, s.health)
	root.Handle(relayv1.DispatchBasePath+"/", requireBearer(s.DispatchKey, dispatch))
	root.Handle(relayv1.AdminBasePath+"/", requireBearer(s.AdminKey, admin))
	return root
}

// requireBearer 校验 Bearer 密钥。两面各持一个实例、密钥独立互不通用；
// 校验挂在前缀子树上，因此先于子树内的路由匹配发生——跨面访问一律 401，
// 不会泄漏"该路径是否存在"。失败响应只有错误码，不回显任何配置内容。
func requireBearer(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !keyMatches(key, bearer(r)) {
			writeUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearer 取 Authorization 头里的密钥。前缀大小写不敏感，其后允许空白。
func bearer(r *http.Request) string {
	raw := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(raw) < len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(raw[len(prefix):])
}

// keyMatches 空配置永不通过：未配置密钥的接口不该变成公开接口。
func keyMatches(configured, presented string) bool {
	if configured == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(configured), []byte(presented)) == 1
}

func writeUnauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, relayv1.ErrorEnvelope{Error: relayv1.Error{
		Code:    string(apperr.Unauthorized),
		Message: "unauthorized",
	}})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	// PG 是权威存储，不可用即整体未就绪；缓存与上游只降级。
	out := relayv1.HealthResponse{
		Database: s.Health.PingDatabase(r.Context()) == nil,
		Cache:    s.Health.CacheReady(r.Context()),
		Upstream: s.Health.UpstreamReady(r.Context()),
	}
	out.Ready = out.Database
	status := http.StatusOK
	if !out.Ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, out)
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	out, err := s.Dispatch.Models(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	var req relayv1.DispatchRequest
	if !decode(w, r, &req) {
		return
	}
	out, err := s.Dispatch.Dispatch(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) results(w http.ResponseWriter, r *http.Request) {
	var rep relayv1.ResultReport
	if !decode(w, r, &rep) {
		return
	}
	out, err := s.Dispatch.Report(r.Context(), rep)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		writeError(w, apperr.New(apperr.InvalidRequest, "malformed JSON body"))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, err error) {
	var e *apperr.Error
	if !errors.As(err, &e) {
		e = apperr.From(err)
	}
	writeJSON(w, apperr.Status(e.Code), relayv1.ErrorEnvelope{Error: relayv1.Error{
		Code:      string(e.Code),
		Message:   e.Message,
		Retryable: e.Retryable,
		Field:     e.Field,
	}})
}
