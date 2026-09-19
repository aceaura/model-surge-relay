package runstate

import (
	"testing"
	"time"
)

// 本文件守 transport outcome 的零变更语义：数据面自己那条连接坏了，
// 上游可能完全健康，所以运行态一个字节都不该动。
//
// 破了这一条的症状是账号莫名冷却——运维只会看到健康账号被压制，
// 看不出根因是数据面的连接池。

func TestTransportOutcomeIsAccepted(t *testing.T) {
	if !OutcomeTransport.valid() {
		t.Fatal("transport 必须被接受，否则数据面的上报整条被拒、连流水都留不下")
	}
}

func TestTransportOutcomeDoesNotCountAsFailure(t *testing.T) {
	if OutcomeTransport.countsAsFailure() {
		t.Fatal("transport 不得计入失败计数：坏的是数据面那条连接，不是这个目标")
	}
}

func TestTransportOutcomeLeavesRuntimeUntouched(t *testing.T) {
	r := newRepo(t)

	apply(t, r, report("a/x", OutcomeTransport), defaults)

	s := get(t, r, "a/x")
	if s.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d，要 0", s.ConsecutiveFailures)
	}
	if s.Cooling {
		t.Errorf("state = %+v，不该冷却", s)
	}
}

// 在**已达阈值前一步**的状态下报 transport 不得触发冷却。
//
// 只在零失败状态下测的话，+0 与 +1 都不会冷却，测不出区别——
// 这一格才真正把「不计入失败计数」与「计入了但还没到阈值」分开。
func TestTransportOutcomeDoesNotTipOverThreshold(t *testing.T) {
	r := newRepo(t)
	// 阈值 3：先攒到 2。
	apply(t, r, report("a/x", OutcomeAbnormal), defaults)
	apply(t, r, report("a/x", OutcomeAbnormal), defaults)
	if got := get(t, r, "a/x").ConsecutiveFailures; got != 2 {
		t.Fatalf("前置状态 ConsecutiveFailures = %d，要 2", got)
	}

	apply(t, r, report("a/x", OutcomeTransport), defaults)

	s := get(t, r, "a/x")
	if s.Cooling {
		t.Fatalf("state = %+v：transport 把计数从 2 推到了 3 并触发冷却，"+
			"一条坏连接就这样把一个健康账号压制了", s)
	}
	if s.ConsecutiveFailures != 2 {
		t.Fatalf("ConsecutiveFailures = %d，要维持 2——transport 不该动它",
			s.ConsecutiveFailures)
	}
}

// 带 retry_after 的 transport 上报也不得冷却。
//
// 连接层故障本来不会带这个字段（没有响应就没有响应头），但上报是跨进程的
// 结构体，字段填错或被复用的旧值带过来都有可能。零变更必须是 outcome 决定的，
// 不能被别的字段绕过去。
func TestTransportOutcomeIgnoresRetryAfter(t *testing.T) {
	r := newRepo(t)
	rep := report("a/x", OutcomeTransport)
	rep.RetryAfter = time.Now().Add(time.Hour).UTC()

	apply(t, r, rep, defaults)

	if s := get(t, r, "a/x"); s.Cooling {
		t.Fatalf("state = %+v：transport 带 retry_after 时被冷却了，"+
			"零变更该由 outcome 决定，不能被别的字段绕过", s)
	}
}

// 与 context_exceeded 一样零变更，但两者是独立的分类。
//
// 合成一类会让运维在流水里分不开「请求太大」与「我们的连接坏了」。
func TestTransportAndContextExceededStaySeparate(t *testing.T) {
	if OutcomeTransport == OutcomeContextExceeded {
		t.Fatal("两个 outcome 的字面值相同了，运维在流水里就分不开这两种故障")
	}
}

// 旧的 outcome 集合行为不变。
func TestLegacyOutcomesStillCountAsFailure(t *testing.T) {
	for _, o := range []Outcome{OutcomeAbnormal, OutcomeRetrying, OutcomeInvalidModel} {
		if !o.countsAsFailure() {
			t.Errorf("%s 不再计入失败计数了，新增 outcome 改坏了既有语义", o)
		}
	}
	for _, o := range []Outcome{OutcomeNormal, OutcomeContextExceeded} {
		if o.countsAsFailure() {
			t.Errorf("%s 开始计入失败计数了，新增 outcome 改坏了既有语义", o)
		}
	}
}

// outcome 的字面值是跨进程契约，数据面按字符串上报，两侧永远不互相 import。
//
// 没有这一条，改掉某个 outcome 的字面值在本仓内完全自洽（所有测试都拿常量
// 自身比较），而效果是数据面上报的那一类再也匹配不上、掉进 default 分支。
func TestOutcomeWireValues(t *testing.T) {
	cases := []struct {
		got  Outcome
		want string
	}{
		{OutcomeNormal, "normal"},
		{OutcomeAbnormal, "abnormal"},
		{OutcomeRetrying, "retrying"},
		{OutcomeInvalidModel, "invalid_model"},
		{OutcomeTransport, "transport"},
		{OutcomeContextExceeded, "context_exceeded"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("outcome 字面值 = %q，要 %q："+
				"数据面按这个字符串上报，改了它这一类就匹配不上了", c.got, c.want)
		}
	}
}
