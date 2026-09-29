package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/runstate"
	"github.com/aceaura/model-surge-relay/backend/strategy"
	"github.com/aceaura/model-surge-relay/backend/usermodel"
)

func (s *Server) routeAdmin(mux *http.ServeMux) {
	const base = "/admin"

	mux.HandleFunc("GET "+base+"/collections", s.listCollections)
	mux.HandleFunc("POST "+base+"/collections", s.createCollection)
	mux.HandleFunc("GET "+base+"/collections/{name}", s.getCollection)
	mux.HandleFunc("PUT "+base+"/collections/{name}", s.updateCollection)
	mux.HandleFunc("DELETE "+base+"/collections/{name}", s.deleteCollection)

	// snapshot 暴露成员叠加 upstream 目录属性后的视图（known / enabled / protocol 等）。
	// groups 端点只返回成员引用字符串，管理面无从判断引用是否仍然有效。
	mux.HandleFunc("GET "+base+"/collections/{name}/snapshot", s.getSnapshot)
	mux.HandleFunc("GET "+base+"/collections/{name}/groups", s.listGroups)
	mux.HandleFunc("POST "+base+"/collections/{name}/groups", s.createGroup)
	mux.HandleFunc("PUT "+base+"/collections/{name}/groups/{group}", s.updateGroup)
	mux.HandleFunc("DELETE "+base+"/collections/{name}/groups/{group}", s.deleteGroup)
	mux.HandleFunc("PUT "+base+"/collections/{name}/groups/{group}/members", s.replaceMembers)

	// 策略组合挂在集合上：优先级链 + 超长压缩托管。dry-run 与真实调度
	// 共用同一个 compose 核，试运行看到的序列就是上线后的序列。
	mux.HandleFunc("GET "+base+"/collections/{name}/strategy", s.getStrategy)
	mux.HandleFunc("PUT "+base+"/collections/{name}/strategy", s.putStrategy)
	mux.HandleFunc("POST "+base+"/collections/{name}/strategy/dry-run", s.dryRunStrategy)

	// 一次性迁移：脚本策略 → 组合配置。可重复执行，报告三类清单。
	mux.HandleFunc("POST "+base+"/migrate-policies", s.migratePolicies)

	mux.HandleFunc("GET "+base+"/user-models", s.listUserModels)
	mux.HandleFunc("POST "+base+"/user-models", s.createUserModel)
	mux.HandleFunc("GET "+base+"/user-models/{name}", s.getUserModel)
	mux.HandleFunc("PUT "+base+"/user-models/{name}", s.updateUserModel)
	mux.HandleFunc("DELETE "+base+"/user-models/{name}", s.deleteUserModel)

	mux.HandleFunc("GET "+base+"/runtime", s.listRuntime)
	// model_id 形如 kimi-1/k3，含斜杠，必须用多段通配才能整体捕获。
	mux.HandleFunc("DELETE "+base+"/runtime/{model_id...}", s.resetRuntime)
}

type collectionBody struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

func (s *Server) listCollections(w http.ResponseWriter, r *http.Request) {
	out, err := s.Collections.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": out})
}

func (s *Server) createCollection(w http.ResponseWriter, r *http.Request) {
	var body collectionBody
	if !decode(w, r, &body) {
		return
	}
	out, err := s.Collections.Create(r.Context(), body.Name, body.Note)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) getCollection(w http.ResponseWriter, r *http.Request) {
	out, err := s.Collections.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateCollection(w http.ResponseWriter, r *http.Request) {
	var body collectionBody
	if !decode(w, r, &body) {
		return
	}
	if err := s.Collections.UpdateNote(r.Context(), r.PathValue("name"), body.Note); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteCollection(w http.ResponseWriter, r *http.Request) {
	if err := s.Collections.Delete(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	out, err := s.Collections.Snapshot(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type groupBody struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Position int             `json:"position"`
	Config   json.RawMessage `json:"config,omitempty"`
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	out, err := s.Collections.Groups(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var body groupBody
	if !decode(w, r, &body) {
		return
	}
	out, err := s.Collections.CreateGroup(r.Context(), collection.Group{
		Collection: r.PathValue("name"),
		Name:       body.Name,
		Type:       body.Type,
		Position:   body.Position,
		Config:     body.Config,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateGroup(w http.ResponseWriter, r *http.Request) {
	var body groupBody
	if !decode(w, r, &body) {
		return
	}
	err := s.Collections.UpdateGroup(r.Context(), collection.Group{
		Collection: r.PathValue("name"),
		Name:       r.PathValue("group"),
		Type:       body.Type,
		Position:   body.Position,
		Config:     body.Config,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	err := s.Collections.DeleteGroup(r.Context(), r.PathValue("name"), r.PathValue("group"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) replaceMembers(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Members []string `json:"members"`
	}
	if !decode(w, r, &body) {
		return
	}
	err := s.Collections.ReplaceMembers(r.Context(), r.PathValue("name"), r.PathValue("group"), body.Members)
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getStrategy(w http.ResponseWriter, r *http.Request) {
	out, err := s.Collections.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out.Strategy)
}

func (s *Server) putStrategy(w http.ResponseWriter, r *http.Request) {
	var st strategy.Strategy
	if !decode(w, r, &st) {
		return
	}
	if err := s.Collections.SetStrategy(r.Context(), r.PathValue("name"), st); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// dryRunStrategy 试运行集合的策略组合：与真实调度共用 compose 核，
// 不解析目标、不写运行态，因此不影响任何真实请求。
func (s *Server) dryRunStrategy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EstTokens int      `json:"est_tokens"`
		TriedIDs  []string `json:"tried_ids,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	out, err := s.Dispatch.DryRun(r.Context(), r.PathValue("name"), body.EstTokens, body.TriedIDs)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decision": out})
}

func (s *Server) migratePolicies(w http.ResponseWriter, r *http.Request) {
	if s.Migrator == nil {
		writeError(w, apperr.New(apperr.Conflict, "policy migration is not available on this deployment"))
		return
	}
	report, err := s.Migrator.Migrate(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

type userModelBody struct {
	Name       string `json:"name"`
	Collection string `json:"collection"`
	ClientKey  string `json:"client_key"`
	Protocol   string `json:"protocol"`
	Enabled    bool   `json:"enabled"`
}

func (s *Server) listUserModels(w http.ResponseWriter, r *http.Request) {
	out, err := s.UserModels.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_models": out})
}

func (s *Server) createUserModel(w http.ResponseWriter, r *http.Request) {
	var body userModelBody
	if !decode(w, r, &body) {
		return
	}
	out, err := s.UserModels.Create(r.Context(), usermodel.UserModel{
		Name:       body.Name,
		Collection: body.Collection,
		ClientKey:  body.ClientKey,
		Protocol:   body.Protocol,
		Enabled:    body.Enabled,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) getUserModel(w http.ResponseWriter, r *http.Request) {
	out, err := s.UserModels.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateUserModel(w http.ResponseWriter, r *http.Request) {
	var body userModelBody
	if !decode(w, r, &body) {
		return
	}
	out, err := s.UserModels.Update(r.Context(), usermodel.UserModel{
		Name:       r.PathValue("name"),
		Collection: body.Collection,
		ClientKey:  body.ClientKey,
		Protocol:   body.Protocol,
		Enabled:    body.Enabled,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteUserModel(w http.ResponseWriter, r *http.Request) {
	if err := s.UserModels.Delete(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listRuntime(w http.ResponseWriter, r *http.Request) {
	out, err := s.RunStates.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if out == nil {
		out = []runstate.State{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime": out})
}

func (s *Server) resetRuntime(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model_id")
	if modelID == "" {
		writeError(w, apperr.Field(apperr.InvalidRequest, "model_id", "model id is required"))
		return
	}
	if err := s.RunStates.Reset(r.Context(), modelID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
