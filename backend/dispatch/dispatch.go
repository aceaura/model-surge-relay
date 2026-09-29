// Package dispatch 编排一次调度：鉴权 → 快照 → 运行态 → 组合器 → 解析。
//
// 凭据只在最后一步的解析里出现，且不回流到决策逻辑，这条顺序是设计约束而非实现巧合。
package dispatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/contract/relayv1"
	"github.com/aceaura/model-surge-relay/backend/runstate"
	"github.com/aceaura/model-surge-relay/backend/strategy"
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
	Candidates    int
	Selected      string
	SelectedPhase string
	Skipped       []relayv1.Skip
	Duration      time.Duration
	Error         string
}

type Service struct {
	UserModels  UserModels
	Collections Collections
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

	snap, decision, cands, err := s.compose(ctx, um.Collection, req.EstTokens, req.TriedIDs)
	if err != nil {
		return relayv1.DispatchResponse{}, err
	}
	entry.Candidates = len(cands)

	if len(cands) == 0 {
		// 空列表不发起解析：没有目标可解析，也不该去打扰上游。
		entry.Skipped = decision.Skipped
		return relayv1.DispatchResponse{}, apperr.New(apperr.TargetUnavailable,
			fmt.Sprintf("no eligible target for user model %q", req.Model))
	}

	for _, c := range cands {
		target, err := s.Resolver.Resolve(ctx, c.ModelID)
		if err != nil {
			decision.Skipped = append(decision.Skipped, relayv1.Skip{
				ModelID: c.ModelID,
				Reason:  relayv1.SkipResolveFailed,
				Detail:  apperr.From(err).Message,
			})
			continue
		}
		if g, ok := snap.Locate(c.ModelID); ok {
			decision.Group = g.Name
			decision.GroupType = g.Type
		}
		entry.Selected = c.ModelID
		entry.SelectedPhase = c.Phase
		entry.Skipped = decision.Skipped
		return relayv1.DispatchResponse{
			RequestID: req.RequestID,
			Target:    relayv1.TargetFromResolved(target),
			Decision:  decision,
		}, nil
	}

	entry.Skipped = decision.Skipped
	return relayv1.DispatchResponse{}, apperr.New(apperr.TargetUnavailable,
		fmt.Sprintf("all %d candidates failed to resolve: %s", len(cands), summarize(decision.Skipped)))
}

// compose 是决策公共核：快照 → 运行态 → 组合器。Dispatch 与 DryRun 共用，
// 试运行与真实调度看到完全一致的序列。
func (s *Service) compose(
	ctx context.Context,
	collectionName string,
	estTokens int,
	triedIDs []string,
) (collection.Snapshot, relayv1.Decision, []strategy.Candidate, error) {
	snap, err := s.Collections.Snapshot(ctx, collectionName)
	if err != nil {
		return collection.Snapshot{}, relayv1.Decision{}, nil, err
	}
	states, err := s.RunStates.Load(ctx, allMembers(snap))
	if err != nil {
		return collection.Snapshot{}, relayv1.Decision{}, nil, err
	}

	cands, memberSkips, groupSkips := strategy.Compose(
		snapshotGroups(snap), states, triedIDs, estTokens, snap.Strategy)

	decision := relayv1.Decision{
		Collection:          collectionName,
		CollectionUpdatedAt: snap.UpdatedAt,
		Candidates:          make([]relayv1.PhasedCandidate, 0, len(cands)),
		Skipped:             make([]relayv1.Skip, 0, len(memberSkips)),
	}
	for _, c := range cands {
		decision.Candidates = append(decision.Candidates,
			relayv1.PhasedCandidate{ModelID: c.ModelID, Phase: c.Phase})
	}
	for _, ms := range memberSkips {
		decision.Skipped = append(decision.Skipped,
			relayv1.Skip{ModelID: ms.ModelID, Reason: ms.Reason, Detail: "group " + ms.Group})
	}
	for _, gs := range groupSkips {
		decision.GroupSkips = append(decision.GroupSkips,
			relayv1.GroupSkip{Group: gs.Group, Reason: gs.Reason, Detail: gs.Detail})
	}
	return snap, decision, cands, nil
}

// DryRun 试运行：输出与真实调度一致的阶段化序列，但不解析目标、不写运行态。
func (s *Service) DryRun(ctx context.Context, collectionName string, estTokens int, triedIDs []string) (relayv1.Decision, error) {
	_, decision, _, err := s.compose(ctx, collectionName, estTokens, triedIDs)
	return decision, err
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

// snapshotGroups 把调度快照适配成组合器视图：只带可用性判定需要的三位。
func snapshotGroups(snap collection.Snapshot) []strategy.Group {
	out := make([]strategy.Group, 0, len(snap.Groups))
	for _, g := range snap.Groups {
		members := make([]strategy.Member, 0, len(g.Members))
		for _, m := range g.Members {
			members = append(members, strategy.Member{
				ModelID: m.ModelID,
				Known:   m.Known,
				Enabled: m.Enabled,
			})
		}
		out = append(out, strategy.Group{Name: g.Name, Members: members})
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
