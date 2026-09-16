package runstate

import (
	"context"
	"fmt"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo 直读 PG，不走缓存：运行态是热态，缓存副本会让冷却生效延迟一个 TTL。
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Load 批量读取运行态。未出现过的目标返回零值状态，
// 调用方无需区分"没记录"与"记录干净"。
func (r *Repo) Load(ctx context.Context, modelIDs []string) (map[string]State, error) {
	out := make(map[string]State, len(modelIDs))
	for _, id := range modelIDs {
		out[id] = State{ModelID: id}
	}
	if len(modelIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT model_id, cooling_until, consecutive_failures,
		        input_tokens, output_tokens, cache_read_tokens, request_count, updated_at
		 FROM target_runtime WHERE model_id = ANY($1)`, modelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var (
			s            State
			coolingUntil *time.Time
		)
		if err := rows.Scan(&s.ModelID, &coolingUntil, &s.ConsecutiveFailures,
			&s.Usage.InputTokens, &s.Usage.OutputTokens, &s.Usage.CacheReadTokens,
			&s.Usage.RequestCount, &s.UpdatedAt); err != nil {
			return nil, err
		}
		// 冷却期满即视为未冷却，无需后台清扫任务。
		if coolingUntil != nil && coolingUntil.After(now) {
			s.Cooling = true
			s.CoolingUntil = *coolingUntil
		}
		out[s.ModelID] = s
	}
	return out, rows.Err()
}

func (r *Repo) Get(ctx context.Context, modelID string) (State, error) {
	states, err := r.Load(ctx, []string{modelID})
	if err != nil {
		return State{}, err
	}
	return states[modelID], nil
}

// List 返回全部有记录的目标运行态，供管理面查询。
func (r *Repo) List(ctx context.Context) ([]State, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT model_id, cooling_until, consecutive_failures,
		        input_tokens, output_tokens, cache_read_tokens, request_count, updated_at
		 FROM target_runtime ORDER BY model_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	out := []State{}
	for rows.Next() {
		var (
			s            State
			coolingUntil *time.Time
		)
		if err := rows.Scan(&s.ModelID, &coolingUntil, &s.ConsecutiveFailures,
			&s.Usage.InputTokens, &s.Usage.OutputTokens, &s.Usage.CacheReadTokens,
			&s.Usage.RequestCount, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if coolingUntil != nil && coolingUntil.After(now) {
			s.Cooling = true
			s.CoolingUntil = *coolingUntil
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ApplyReport 幂等应用一次上报。返回 applied=false 表示该 report_id 已处理过。
//
// 幂等判定与状态变更同事务：先插 result_reports，仅当真的插入了新行才动状态。
func (r *Repo) ApplyReport(ctx context.Context, rep ResultReport, cfg Thresholds) (bool, error) {
	if err := rep.validate(); err != nil {
		return false, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`INSERT INTO result_reports (report_id, request_id, model_id, outcome)
		 VALUES ($1, $2, $3, $4) ON CONFLICT (report_id) DO NOTHING`,
		rep.ReportID, rep.RequestID, rep.ModelID, string(rep.Outcome))
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		// 重复上报：提交空事务即可，无状态变更。
		return false, tx.Commit(ctx)
	}

	if err := applyOutcome(ctx, tx, rep, cfg); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func applyOutcome(ctx context.Context, tx pgx.Tx, rep ResultReport, cfg Thresholds) error {
	switch rep.Outcome {
	case OutcomeContextExceeded:
		// 请求太大不是目标的问题，连 upsert 都不做：避免凭空造出一行零值记录。
		return nil
	case OutcomeNormal:
		_, err := tx.Exec(ctx,
			`INSERT INTO target_runtime
			   (model_id, cooling_until, consecutive_failures,
			    input_tokens, output_tokens, cache_read_tokens, request_count, updated_at)
			 VALUES ($1, NULL, 0, $2, $3, $4, 1, now())
			 ON CONFLICT (model_id) DO UPDATE SET
			   cooling_until        = NULL,
			   consecutive_failures = 0,
			   input_tokens         = target_runtime.input_tokens + $2,
			   output_tokens        = target_runtime.output_tokens + $3,
			   cache_read_tokens    = target_runtime.cache_read_tokens + $4,
			   request_count        = target_runtime.request_count + 1,
			   updated_at           = now()`,
			rep.ModelID, rep.Usage.InputTokens, rep.Usage.OutputTokens, rep.Usage.CacheReadTokens)
		return err
	default:
		// 失败类：递增计数，达阈值置入冷却。
		// GREATEST 保证冷却时间只向后推进，重叠上报不会缩短既有冷却。
		threshold := cfg.FailureThreshold
		if threshold <= 0 {
			threshold = 0 // 0 = 从不冷却，下面的 CASE 恒不成立
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO target_runtime (model_id, consecutive_failures, cooling_until, updated_at)
			 VALUES ($1, 1, CASE WHEN $2 > 0 AND 1 >= $2 THEN now() + $3::interval ELSE NULL END, now())
			 ON CONFLICT (model_id) DO UPDATE SET
			   consecutive_failures = target_runtime.consecutive_failures + 1,
			   cooling_until = CASE
			     WHEN $2 > 0 AND target_runtime.consecutive_failures + 1 >= $2
			       THEN GREATEST(coalesce(target_runtime.cooling_until, now()), now() + $3::interval)
			     ELSE target_runtime.cooling_until
			   END,
			   updated_at = now()`,
			rep.ModelID, threshold, intervalArg(cfg.CooldownDuration))
		return err
	}
}

// Reset 解除冷却并清零失败计数，保留累计用量（用量是审计数据，不该被运维操作抹掉）。
func (r *Repo) Reset(ctx context.Context, modelID string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE target_runtime
		 SET cooling_until = NULL, consecutive_failures = 0, updated_at = now()
		 WHERE model_id = $1`, modelID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.New(apperr.NotFound, fmt.Sprintf("no runtime state for target %q", modelID))
	}
	return nil
}

// intervalArg 把时长渲染为 PG interval 字面量。负值归零，避免把冷却推到过去。
func intervalArg(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d milliseconds", d.Milliseconds())
}
