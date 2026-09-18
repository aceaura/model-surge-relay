package runstate

import (
	"testing"
	"time"
)

// reportWithReset 是带上游明示到期时刻的失败上报。
func reportWithReset(modelID string, at time.Time) ResultReport {
	rep := report(modelID, OutcomeRetrying)
	rep.RetryAfter = at
	return rep
}

// TestExplicitResetCoolsImmediately 上游明示时立刻冷却，不等失败阈值。
//
// 这是本轮的核心：阈值是 3，只上报一次就该冷却到明示的那一刻。
// 按阈值等三次意味着在限流窗口里再撞两次。
func TestExplicitResetCoolsImmediately(t *testing.T) {
	r := newRepo(t)
	at := time.Now().Add(30 * time.Minute).UTC()

	apply(t, r, reportWithReset("a/x", at), defaults)

	s := get(t, r, "a/x")
	if !s.Cooling {
		t.Fatalf("state = %+v，上游明示到期时刻时第一次失败就该冷却", s)
	}
	// 容差 2s：SQL 侧存的是 timestamptz，往返有微秒级误差；
	// 这里要钉住的是「用了上报里的时刻」而不是「用了配置的 1 分钟」。
	if d := s.CoolingUntil.Sub(at); d > 2*time.Second || d < -2*time.Second {
		t.Fatalf("CoolingUntil = %v, want ≈ %v（上游明示的那一刻）", s.CoolingUntil, at)
	}
}

// TestExplicitResetStillCountsFailure 明示路径下失败计数照样递增。
//
// 不递增的话，一个反复限流的目标在每个窗口开头都会被重新选中，
// 启发式压制永远不起作用。
func TestExplicitResetStillCountsFailure(t *testing.T) {
	r := newRepo(t)
	at := time.Now().Add(10 * time.Minute).UTC()

	apply(t, r, reportWithReset("a/x", at), defaults)
	apply(t, r, reportWithReset("a/x", at), defaults)

	if got := get(t, r, "a/x").ConsecutiveFailures; got != 2 {
		t.Fatalf("ConsecutiveFailures = %d, want 2", got)
	}
}

// TestExplicitResetOnlyMovesForward 重叠上报不能缩短既有冷却。
//
// 换目标时前后两次上报的到期时刻可能一长一短，采信后到的短值
// 会让一个还在限流里的目标提前被放出来。
func TestExplicitResetOnlyMovesForward(t *testing.T) {
	r := newRepo(t)
	far := time.Now().Add(2 * time.Hour).UTC()
	near := time.Now().Add(1 * time.Minute).UTC()

	apply(t, r, reportWithReset("a/x", far), defaults)
	apply(t, r, reportWithReset("a/x", near), defaults)

	s := get(t, r, "a/x")
	if d := s.CoolingUntil.Sub(far); d > 2*time.Second || d < -2*time.Second {
		t.Fatalf("CoolingUntil = %v, want 仍为更晚的 %v", s.CoolingUntil, far)
	}
}

// TestNoResetFallsBackToHeuristic 上游没说时行为与本轮之前完全一致。
//
// 阈值 3：前两次不冷却，第三次才冷却。
func TestNoResetFallsBackToHeuristic(t *testing.T) {
	r := newRepo(t)

	for i := 1; i <= 2; i++ {
		apply(t, r, report("a/x", OutcomeRetrying), defaults)
		if s := get(t, r, "a/x"); s.Cooling {
			t.Fatalf("第 %d 次失败就冷却了，阈值是 %d", i, defaults.FailureThreshold)
		}
	}
	apply(t, r, report("a/x", OutcomeRetrying), defaults)
	if s := get(t, r, "a/x"); !s.Cooling {
		t.Fatalf("达阈值仍未冷却：%+v", s)
	}
}

// TestUntrustworthyResetFallsBackToHeuristic 坏时刻要回落启发式，而不是不冷却。
//
// 跨进程有排队与网络往返，数据面校验过的时刻到这里可能已经过期。
// 关键是「回落」而不是「忽略」——忽略会让这次失败完全不计入冷却判定。
func TestUntrustworthyResetFallsBackToHeuristic(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
	}{
		{"过去的时刻", time.Now().Add(-time.Hour).UTC()},
		{"超出 24h 上限", time.Now().Add(48 * time.Hour).UTC()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t)
			// 阈值设 1：回落到启发式时应立刻冷却，
			// 于是「回落了」与「整条被忽略」可区分。
			cfg := Thresholds{FailureThreshold: 1, CooldownDuration: time.Minute}

			apply(t, r, reportWithReset("a/x", tc.at), cfg)

			s := get(t, r, "a/x")
			if !s.Cooling {
				t.Fatalf("坏时刻应回落启发式并按阈值冷却，得到 %+v", s)
			}
			// 冷却到期应当是「现在 + 配置时长」，而不是那个坏时刻。
			wantMax := time.Now().Add(2 * time.Minute)
			if s.CoolingUntil.After(wantMax) {
				t.Fatalf("CoolingUntil = %v，采信了坏时刻", s.CoolingUntil)
			}
		})
	}
}

// TestValidateIgnoresRetryAfter 到期时刻不可信不该让整条上报被拒。
//
// 上报本身是有效的（用量要记、失败要计），只是那一维不可用。
// 在 validate 里拒会把一次真实的失败整条丢掉。
func TestValidateIgnoresRetryAfter(t *testing.T) {
	cases := map[string]time.Time{
		"零值":      {},
		"过去的时刻":   time.Now().Add(-time.Hour),
		"过于遥远的时刻": time.Now().Add(365 * 24 * time.Hour),
	}
	for name, at := range cases {
		t.Run(name, func(t *testing.T) {
			rep := reportWithReset("a/x", at)
			if err := rep.validate(); err != nil {
				t.Fatalf("validate 拒了上报：%v", err)
			}
		})
	}
}

// TestNormalOutcomeIgnoresRetryAfter 成功上报清冷却，不看到期时刻。
//
// 成功意味着这个目标此刻可用；此时若还按上报里残留的时刻冷却，
// 一个刚恢复的目标会被立刻锁回去。
func TestNormalOutcomeIgnoresRetryAfter(t *testing.T) {
	r := newRepo(t)
	apply(t, r, reportWithReset("a/x", time.Now().Add(time.Hour).UTC()), defaults)
	if !get(t, r, "a/x").Cooling {
		t.Fatal("前置条件不成立：应先进入冷却")
	}

	rep := report("a/x", OutcomeNormal)
	rep.RetryAfter = time.Now().Add(time.Hour).UTC()
	apply(t, r, rep, defaults)

	s := get(t, r, "a/x")
	if s.Cooling || s.ConsecutiveFailures != 0 {
		t.Fatalf("state = %+v，成功上报应清冷却与计数", s)
	}
}

// TestContextExceededIgnoresRetryAfter 上下文超限连行都不建，到期时刻也不例外。
//
// 请求太大不是目标的问题；带着一个到期时刻来也不该凭空造出冷却。
func TestContextExceededIgnoresRetryAfter(t *testing.T) {
	r := newRepo(t)
	rep := report("a/x", OutcomeContextExceeded)
	rep.RetryAfter = time.Now().Add(time.Hour).UTC()

	apply(t, r, rep, defaults)

	s := get(t, r, "a/x")
	if s.Cooling || s.ConsecutiveFailures != 0 {
		t.Fatalf("state = %+v，上下文超限不该改运行态", s)
	}
}

// TestTrustworthyReset 接收侧判据的直接单测。
//
// 与数据面 codec/ratelimit.Trustworthy 是同一套判据的两份实现
// （两服务独立发版、不跨仓 import），两边都要各自钉住。
func TestTrustworthyReset(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"零值", time.Time{}, false},
		{"过去", now.Add(-time.Second), false},
		{"恰好当下", now, false},
		{"一秒后", now.Add(time.Second), true},
		{"恰好 24h", now.Add(24 * time.Hour), true},
		{"超过 24h", now.Add(24*time.Hour + time.Second), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trustworthyReset(tc.at, now); got != tc.want {
				t.Fatalf("trustworthyReset = %v, want %v", got, tc.want)
			}
		})
	}
}
