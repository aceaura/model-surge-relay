package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/policy"
	"github.com/aceaura/model-surge-relay/backend/runstate"
	"github.com/aceaura/model-surge-relay/backend/usermodel"
)

func (s *Server) routeAdmin(mux *http.ServeMux) {
	const base = "/admin"

	mux.HandleFunc("GET "+base+"/collections", s.listCollections)
	mux.HandleFunc("POST "+base+"/collections", s.createCollection)
	mux.HandleFunc("GET "+base+"/collections/{name}", s.getCollection)
	mux.HandleFunc("PUT "+base+"/collections/{name}", s.updateCollection)
	mux.HandleFunc("DELETE "+base+"/collections/{name}", s.deleteCollection)

	mux.HandleFunc("GET "+base+"/collections/{name}/groups", s.listGroups)
	mux.HandleFunc("POST "+base+"/collections/{name}/groups", s.createGroup)
	mux.HandleFunc("PUT "+base+"/collections/{name}/groups/{group}", s.updateGroup)
	mux.HandleFunc("DELETE "+base+"/collections/{name}/groups/{group}", s.deleteGroup)
	mux.HandleFunc("PUT "+base+"/collections/{name}/groups/{group}/members", s.replaceMembers)

	mux.HandleFunc("GET "+base+"/policies", s.listPolicies)
	mux.HandleFunc("POST "+base+"/policies", s.createPolicy)
	mux.HandleFunc("GET "+base+"/policies/{name}", s.getPolicy)
	mux.HandleFunc("PUT "+base+"/policies/{name}", s.updatePolicy)
	mux.HandleFunc("DELETE "+base+"/policies/{name}", s.deletePolicy)
	mux.HandleFunc("POST "+base+"/policies/{name}/dry-run", s.dryRunPolicy)

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

type policyBody struct {
	Name     string `json:"name"`
	Language string `json:"language"`
	Source   string `json:"source"`
	Note     string `json:"note"`
}

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	out, err := s.Policies.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": out})
}

func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request) {
	var body policyBody
	if !decode(w, r, &body) {
		return
	}
	out, err := s.Policies.Create(r.Context(), policy.Policy{
		Name:     body.Name,
		Language: policy.Language(body.Language),
		Source:   body.Source,
		Note:     body.Note,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	out, err := s.Policies.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updatePolicy(w http.ResponseWriter, r *http.Request) {
	var body policyBody
	if !decode(w, r, &body) {
		return
	}
	out, err := s.Policies.Update(r.Context(), policy.Policy{
		Name:     r.PathValue("name"),
		Language: policy.Language(body.Language),
		Source:   body.Source,
		Note:     body.Note,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) {
	if err := s.Policies.Delete(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// dryRunPolicy 用给定输入试运行策略：读 Collection 快照，但不碰运行态、
// 不写缓存、不解析目标，因此不影响任何真实请求。
func (s *Server) dryRunPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Collection string                  `json:"collection"`
		Request    policy.RequestContext   `json:"request"`
		Runtime    map[string]policy.State `json:"runtime,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	p, err := s.Policies.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	snap, err := s.Collections.Snapshot(r.Context(), body.Collection)
	if err != nil {
		writeError(w, err)
		return
	}
	runtimeStates := body.Runtime
	if runtimeStates == nil {
		runtimeStates = map[string]policy.State{}
	}
	decision, err := s.Engine.Execute(r.Context(), p, policy.Input{
		Request:    body.Request,
		Collection: snap,
		Runtime:    runtimeStates,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policy":         p.Name,
		"policy_version": p.Version,
		"decision":       decision,
	})
}

type userModelBody struct {
	Name       string `json:"name"`
	Collection string `json:"collection"`
	Policy     string `json:"policy"`
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
		Policy:     body.Policy,
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
		Policy:     body.Policy,
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
