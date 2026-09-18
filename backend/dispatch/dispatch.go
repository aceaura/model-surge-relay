// Package dispatch 编排一次调度：鉴权 → 快照 → 运行态 → 策略 → 过滤 → 解析。
//
// 凭据只在最后一步的解析里出现，且不回流到策略层，这条顺序是设计约束而非实现巧合。
package dispatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/contract/relayv1"
	"github.com/aceaura/model-surge-relay/backend/policy"
	"github.com/aceaura/model-surge-relay/backend/runstate"
	"github.com/aceaura/model-surge-relay/backend/upstreamclient"
	"github.com/aceaura/model-surge-relay/backend/usermodel"
)

type UserModels interface {
	Authenticate(ctx context.Context, name, protocol, clientKey string) (usermodel.UserModel, error)
	List(ctx context.Context) ([]usermodel.UserModel, error)
}

type Collections interface {
	Snapshot(ctx context.Context, name string) (collection.Snapshot, error)
}

type Policies interface {
	Get(ctx context.Context, name string) (policy.Policy, error)
}

type PolicyEngine interface {
	Execute(ctx context.Context, p policy.Policy, in policy.Input) (policy.Decision, error)
}

type RunStates interface {
	Load(ctx context.Context, modelIDs []string) (map[string]runstate.State, error)
	ApplyReport(ctx context.Context, rep runstate.ResultReport, cfg runstate.Thresholds) (bool, error)
}

type Resolver interface {
	Resolve(ctx context.Context, modelID string) (upstreamclient.ResolvedTarget, error)
}

// Logger 是调度日志出口。实现方负责不输出凭据——传入的 Target 已用 String() 脱敏。
type Logger interface {
	Dispatched(entry LogEntry)
}

type LogEntry struct {
	RequestID     string
	UserModel     string
	Policy        string
	PolicyVersion int
	Candidates    int
	Selected      string
	Skipped       []relayv1.Skip
	Duration      time.Duration
	Error         string
}

type Service struct {
	UserModels  UserModels
	Collections Collections
	Policies    Policies
	Engine      PolicyEngine
	RunStates   RunStates
	Resolver    Resolver
	Thresholds  runstate.Thresholds
	Logger      Logger
}

func (s *Service) Dispatch(ctx context.Context, req relayv1.DispatchRequest) (relayv1.DispatchResponse, error) {
	started := time.Now()
	entry := LogEntry{RequestID: req.RequestID, UserModel: req.Model}
	resp, err := s.dispatch(ctx, req, &entry)
	entry.Duration = time.Since(started)
	if err != nil {
		entry.Error = err.Error()
	}
	s.log(entry)
	return resp, err
}

func (s *Service) dispatch(ctx context.Context, req relayv1.DispatchRequest, entry *LogEntry) (relayv1.DispatchResponse, error) {
	if strings.TrimSpace(req.Model) == "" {
		return relayv1.DispatchResponse{}, apperr.Field(apperr.InvalidRequest, "model", "model is required")
	}

	um, err := s.UserModels.Authenticate(ctx, req.Model, req.InboundProtocol, req.ClientKey)
	if err != nil {
		return relayv1.DispatchResponse{}, err
	}

	snap, err := s.Collections.Snapshot(ctx, um.Collection)
	if err != nil {
		return relayv1.DispatchResponse{}, err
	}

	states, err := s.RunStates.Load(ctx, allMembers(snap))
	if err != nil {
		return relayv1.DispatchResponse{}, err
	}

	decision := relayv1.Decision{Collection: um.Collection, Candidates: []string{}}
	var candidates []string
	if um.Policy == "" {
		candidates = fallbackOrder(snap)
	} else {
		p, err := s.Policies.Get(ctx, um.Policy)
		if err != nil {
			return relayv1.DispatchResponse{}, err
		}
		decision.Policy = p.Name
		decision.PolicyVersion = p.Version
		entry.Policy = p.Name
		entry.PolicyVersion = p.Version

		// 恰好执行一次：失败即整体失败，不重试、不回退到兜底顺序。
		out, err := s.Engine.Execute(ctx, p, policy.Input{
			Request: policy.RequestContext{
				UserModel:       req.Model,
				InboundProtocol: req.InboundProtocol,
				EstTokens:       req.EstTokens,
				TriedIDs:        req.TriedIDs,
				RequestID:       req.RequestID,
			},
			Collection: snap,
			Runtime:    policyStates(states),
		})
		if err != nil {
			return relayv1.DispatchResponse{}, err
		}
		candidates = out.Candidates
		decision.Note = out.Note
	}

	kept, skipped := filterCandidates(candidates, snap, states, req.TriedIDs)
	decision.Skipped = skipped
	decision.Candidates = kept
	entry.Candidates = len(kept)
	entry.Skipped = skipped

	if len(kept) == 0 {
		// 空列表不发起解析：没有目标可解析，也不该去打扰上游。
		return relayv1.DispatchResponse{}, apperr.New(apperr.TargetUnavailable,
			fmt.Sprintf("no eligible target for user model %q", req.Model))
	}

	for _, id := range kept {
		target, err := s.Resolver.Resolve(ctx, id)
		if err != nil {
			decision.Skipped = append(decision.Skipped, relayv1.Skip{
				ModelID: id,
				Reason:  relayv1.SkipResolveFailed,
				Detail:  apperr.From(err).Message,
			})
			continue
		}
		if g, ok := snap.Locate(id); ok {
			decision.Group = g.Name
			decision.GroupType = g.Type
		}
		entry.Selected = id
		entry.Skipped = decision.Skipped
		return relayv1.DispatchResponse{
			RequestID: req.RequestID,
			Target:    relayv1.TargetFromResolved(target),
			Decision:  decision,
		}, nil
	}

	entry.Skipped = decision.Skipped
	return relayv1.DispatchResponse{}, apperr.New(apperr.TargetUnavailable,
		fmt.Sprintf("all %d candidates failed to resolve: %s", len(kept), summarize(decision.Skipped)))
}

func (s *Service) Report(ctx context.Context, rep relayv1.ResultReport) (relayv1.ReportResponse, error) {
	applied, err := s.RunStates.ApplyReport(ctx, runstate.ResultReport{
		ReportID:  rep.ReportID,
		RequestID: rep.RequestID,
		ModelID:   rep.ModelID,
		Outcome:   runstate.Outcome(rep.Outcome),
		Usage: runstate.Usage{
			InputTokens:     rep.Usage.InputTokens,
			OutputTokens:    rep.Usage.OutputTokens,
			CacheReadTokens: rep.Usage.CacheReadTokens,
		},
		RetryAfter: rep.RetryAfter,
	}, s.Thresholds)
	if err != nil {
		return relayv1.ReportResponse{}, err
	}
	return relayv1.ReportResponse{Applied: applied}, nil
}

func (s *Service) Models(ctx context.Context) (relayv1.ModelsResponse, error) {
	list, err := s.UserModels.List(ctx)
	if err != nil {
		return relayv1.ModelsResponse{}, err
	}
	out := relayv1.ModelsResponse{Models: make([]relayv1.UserModelSummary, 0, len(list))}
	for _, m := range list {
		out.Models = append(out.Models, relayv1.UserModelSummary{
			Name:       m.Name,
			Collection: m.Collection,
			Policy:     m.Policy,
			Protocol:   m.Protocol,
			Enabled:    m.Enabled,
		})
	}
	return out, nil
}

func (s *Service) log(entry LogEntry) {
	if s.Logger != nil {
		s.Logger.Dispatched(entry)
	}
}

func allMembers(snap collection.Snapshot) []string {
	out := []string{}
	for _, g := range snap.Groups {
		for _, m := range g.Members {
			out = append(out, m.ModelID)
		}
	}
	return out
}

func policyStates(states map[string]runstate.State) map[string]policy.State {
	out := make(map[string]policy.State, len(states))
	for id, s := range states {
		ps := policy.State{
			Cooling:             s.Cooling,
			ConsecutiveFailures: s.ConsecutiveFailures,
			InputTokens:         s.Usage.InputTokens,
			OutputTokens:        s.Usage.OutputTokens,
			RequestCount:        s.Usage.RequestCount,
		}
		if s.Cooling {
			ps.CoolingUntil = s.CoolingUntil.Unix()
		}
		out[id] = ps
	}
	return out
}

func summarize(skipped []relayv1.Skip) string {
	parts := make([]string, 0, len(skipped))
	for _, s := range skipped {
		if s.Reason != relayv1.SkipResolveFailed {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", s.ModelID, s.Detail))
	}
	return strings.Join(parts, "; ")
}
