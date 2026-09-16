package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/collection"
	"github.com/aceaura/model-surge-relay/backend/policy"
)

func testInput() policy.Input {
	return policy.Input{
		Request: policy.RequestContext{
			UserModel:       "coding",
			InboundProtocol: "anthropic",
			EstTokens:       12000,
			TriedIDs:        []string{"ark-1/ds"},
			RequestID:       "req-1",
		},
		Collection: collection.Snapshot{
			Name: "prod",
			Groups: []collection.GroupSnapshot{
				{
					Name: "primary", Type: "main", Position: 0,
					Members: []collection.Member{
						{ModelID: "kimi-1/k3", Protocol: "anthropic", ContextWindow: 262144, Enabled: true, Known: true},
						{ModelID: "ark-1/ds", Protocol: "chat_completions", Enabled: true, Known: true},
					},
				},
				{
					Name: "backup", Type: "fallback", Position: 1,
					Members: []collection.Member{
						{ModelID: "deepseek-1/v4", Protocol: "chat_completions", ContextWindow: 1000000, Enabled: true, Known: true},
					},
				},
			},
		},
		Runtime: map[string]policy.State{
			"kimi-1/k3":     {RequestCount: 10},
			"ark-1/ds":      {Cooling: true, ConsecutiveFailures: 3},
			"deepseek-1/v4": {RequestCount: 2},
		},
	}
}

func runLua(t *testing.T, source string) (policy.Decision, error) {
	t.Helper()
	program, err := NewLua().Compile(source)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return program.Execute(ctx, testInput())
}

func TestLuaReturnsOrderedCandidates(t *testing.T) {
	got, err := runLua(t, `return {"deepseek-1/v4", "kimi-1/k3"}`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := []string{"deepseek-1/v4", "kimi-1/k3"}
	if strings.Join(got.Candidates, ",") != strings.Join(want, ",") {
		t.Errorf("candidates = %v, want %v", got.Candidates, want)
	}
}

func TestLuaReturnsObjectWithNote(t *testing.T) {
	got, err := runLua(t, `return {candidates = {"kimi-1/k3"}, note = "picked primary"}`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0] != "kimi-1/k3" {
		t.Errorf("candidates = %v", got.Candidates)
	}
	if got.Note != "picked primary" {
		t.Errorf("note = %q", got.Note)
	}
}

func TestLuaReadsCollectionAndRuntime(t *testing.T) {
	// 跳过冷却目标、按组顺序收集，验证脚本确实看到了结构与运行态。
	got, err := runLua(t, `
		local out = {}
		for _, group in ipairs(input.collection.groups) do
			for _, member in ipairs(group.members) do
				local state = input.runtime[member.model_id]
				if member.enabled and not (state and state.cooling) then
					table.insert(out, member.model_id)
				end
			end
		end
		return out
	`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := []string{"kimi-1/k3", "deepseek-1/v4"}
	if strings.Join(got.Candidates, ",") != strings.Join(want, ",") {
		t.Errorf("candidates = %v, want %v", got.Candidates, want)
	}
}

func TestLuaReadsRequestContext(t *testing.T) {
	got, err := runLua(t, `
		if input.request.user_model == "coding" and input.request.est_tokens == 12000
			and input.request.tried_ids[1] == "ark-1/ds" then
			return {"kimi-1/k3"}
		end
		return {}
	`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(got.Candidates) != 1 {
		t.Errorf("script could not read the request context: %v", got.Candidates)
	}
}

func TestLuaEmptyResultIsAllowed(t *testing.T) {
	for _, src := range []string{`return {}`, `return nil`, ``} {
		got, err := runLua(t, src)
		if err != nil {
			t.Fatalf("execute %q: %v", src, err)
		}
		if len(got.Candidates) != 0 {
			t.Errorf("%q candidates = %v, want empty", src, got.Candidates)
		}
	}
}

func TestLuaCompileErrorCarriesPosition(t *testing.T) {
	_, err := NewLua().Compile("return {\nthis is not lua\n}")
	var ce *policy.CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *policy.CompileError", err)
	}
	if ce.Language != policy.LangLua {
		t.Errorf("language = %s", ce.Language)
	}
	if ce.Line == 0 {
		t.Errorf("compile error must report a line: %+v", ce)
	}
}

func TestLuaRuntimeErrorIsReported(t *testing.T) {
	_, err := runLua(t, `local x = nil; return x.y`)
	if err == nil {
		t.Fatal("a runtime error must surface")
	}
	if errors.Is(err, policy.ErrTimeout) {
		t.Errorf("runtime error must not be reported as a timeout: %v", err)
	}
}

func TestLuaWrongReturnTypeIsRejected(t *testing.T) {
	if _, err := runLua(t, `return 42`); err == nil {
		t.Error("a numeric return must be rejected")
	}
	if _, err := runLua(t, `return {1, 2}`); err == nil {
		t.Error("non-string candidates must be rejected")
	}
}

func TestLuaInfiniteLoopTimesOut(t *testing.T) {
	program, err := NewLua().Compile(`while true do end`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, execErr := program.Execute(ctx, testInput())
		done <- execErr
	}()

	select {
	case err := <-done:
		if !errors.Is(err, policy.ErrTimeout) {
			t.Errorf("err = %v, want ErrTimeout", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("execution did not stop; the instruction limit and timeout both failed")
	}
}

func TestLuaSandboxBlocksHostAccess(t *testing.T) {
	blocked := []string{"io", "os", "package", "require", "dofile", "loadfile", "load", "loadstring", "debug", "print"}
	for _, name := range blocked {
		t.Run(name, func(t *testing.T) {
			got, err := runLua(t, `if `+name+` == nil then return {"blocked"} end return {"reachable"}`)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(got.Candidates) != 1 || got.Candidates[0] != "blocked" {
				t.Errorf("%s must not be reachable from a policy script", name)
			}
		})
	}
}

func TestLuaSafeLibsAreAvailable(t *testing.T) {
	got, err := runLua(t, `
		local n = math.max(1, 2)
		local s = string.upper("ok")
		local t2 = {}
		table.insert(t2, s .. tostring(n))
		return t2
	`)
	if err != nil {
		t.Fatalf("math/string/table must stay available: %v", err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0] != "OK2" {
		t.Errorf("candidates = %v", got.Candidates)
	}
}

func TestLuaConcurrentExecutionsDoNotShareState(t *testing.T) {
	// 脚本写全局变量：若 LState 被复用，并发执行会互相看到对方的值。
	program, err := NewLua().Compile(`
		if leaked ~= nil then return {"leaked"} end
		leaked = input.request.request_id
		return {leaked}
	`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			in := testInput()
			in.Request.RequestID = "req-" + string(rune('a'+n%26))
			got, execErr := program.Execute(context.Background(), in)
			if execErr != nil {
				errs <- execErr
				return
			}
			if len(got.Candidates) != 1 || got.Candidates[0] != in.Request.RequestID {
				errs <- errors.New("state leaked across executions: " + strings.Join(got.Candidates, ","))
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestLuaProgramIsReusableAcrossExecutions(t *testing.T) {
	program, err := NewLua().Compile(`return {input.request.request_id}`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for i := 0; i < 3; i++ {
		in := testInput()
		in.Request.RequestID = "req-" + strings.Repeat("x", i+1)
		got, err := program.Execute(context.Background(), in)
		if err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
		if got.Candidates[0] != in.Request.RequestID {
			t.Errorf("execution %d saw %q", i, got.Candidates[0])
		}
	}
}
