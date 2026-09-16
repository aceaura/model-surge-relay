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
	internal := http.NewServeMux()
	internal.HandleFunc("GET "+relayv1.InternalBasePath+"/health", s.health)
	internal.HandleFunc("GET "+relayv1.InternalBasePath+"/models", s.models)
	internal.HandleFunc("POST "+relayv1.InternalBasePath+"/dispatch", s.dispatch)
	internal.HandleFunc("POST "+relayv1.InternalBasePath+"/results", s.results)

	admin := http.NewServeMux()
	s.routeAdmin(admin)

	root := http.NewServeMux()
	root.Handle(relayv1.InternalBasePath+"/", requireBearer(s.DispatchKey, internal))
	root.Handle(relayv1.AdminBasePath+"/", requireAdminKey(s.AdminKey, admin))
	return root
}

// requireBearer 校验调度密钥。失败响应只有错误码，不回显任何配置内容。
func requireBearer(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !keyMatches(key, presented) {
			writeUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireAdminKey(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !keyMatches(key, r.Header.Get("X-Admin-Key")) {
			writeUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
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
	out := relayv1.HealthResponse{Status: "ok", Database: "ok", Cache: "disabled", Upstream: "unreachable"}
	status := http.StatusOK
	if err := s.Health.PingDatabase(r.Context()); err != nil {
		// PG 是权威存储，不可用即整体未就绪。
		out.Status = "unready"
		out.Database = "unreachable"
		status = http.StatusServiceUnavailable
	}
	if s.Health.CacheReady(r.Context()) {
		out.Cache = "ok"
	}
	if s.Health.UpstreamReady(r.Context()) {
		out.Upstream = "ok"
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
