package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/policy"
)

func runJS(t *testing.T, rt *JS, source string) (policy.Decision, error) {
	t.Helper()
	program, err := rt.Compile(source)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return program.Execute(ctx, testInput())
}

func TestJSReturnsOrderedCandidates(t *testing.T) {
	got, err := runJS(t, NewJavaScript(), `return ["deepseek-1/v4", "kimi-1/k3"];`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Join(got.Candidates, ",") != "deepseek-1/v4,kimi-1/k3" {
		t.Errorf("candidates = %v", got.Candidates)
	}
}

func TestJSReturnsObjectWithNote(t *testing.T) {
	got, err := runJS(t, NewJavaScript(), `return {candidates: ["kimi-1/k3"], note: "primary"};`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(got.Candidates) != 1 || got.Note != "primary" {
		t.Errorf("decision = %+v", got)
	}
}

func TestJSReadsCollectionAndRuntime(t *testing.T) {
	got, err := runJS(t, NewJavaScript(), `
		var out = [];
		input.collection.groups.forEach(function (group) {
			group.members.forEach(function (member) {
				var state = input.runtime[member.model_id];
				if (member.enabled && !(state && state.cooling)) {
					out.push(member.model_id);
				}
			});
		});
		return out;
	`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Join(got.Candidates, ",") != "kimi-1/k3,deepseek-1/v4" {
		t.Errorf("candidates = %v", got.Candidates)
	}
}

func TestJSSortsByUsage(t *testing.T) {
	got, err := runJS(t, NewJavaScript(), `
		var ids = Object.keys(input.runtime);
		ids.sort(function (a, b) {
			return input.runtime[a].request_count - input.runtime[b].request_count;
		});
		return ids;
	`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Candidates[0] != "ark-1/ds" {
		t.Errorf("least used first = %v", got.Candidates)
	}
}

func TestJSEmptyResultIsAllowed(t *testing.T) {
	for _, src := range []string{`return [];`, `return null;`, `return undefined;`, ``} {
		got, err := runJS(t, NewJavaScript(), src)
		if err != nil {
			t.Fatalf("execute %q: %v", src, err)
		}
		if len(got.Candidates) != 0 {
			t.Errorf("%q candidates = %v, want empty", src, got.Candidates)
		}
	}
}

func TestJSCompileErrorCarriesPosition(t *testing.T) {
	_, err := NewJavaScript().Compile("var x = ;\nreturn [];")
	var ce *policy.CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *policy.CompileError", err)
	}
	if ce.Language != policy.LangJavaScript {
		t.Errorf("language = %s", ce.Language)
	}
	if ce.Line != 1 {
		t.Errorf("line = %d, want 1 (wrapper offset must be removed)", ce.Line)
	}
}

func TestJSRuntimeErrorIsReported(t *testing.T) {
	_, err := runJS(t, NewJavaScript(), `throw new Error("bad policy");`)
	if err == nil {
		t.Fatal("a thrown error must surface")
	}
	if errors.Is(err, policy.ErrTimeout) {
		t.Errorf("thrown error must not be reported as a timeout: %v", err)
	}
	if !strings.Contains(err.Error(), "bad policy") {
		t.Errorf("err = %v, want the script message", err)
	}
}

func TestJSWrongReturnTypeIsRejected(t *testing.T) {
	if _, err := runJS(t, NewJavaScript(), `return 42;`); err == nil {
		t.Error("a numeric return must be rejected")
	}
	if _, err := runJS(t, NewJavaScript(), `return [1, 2];`); err == nil {
		t.Error("non-string candidates must be rejected")
	}
}

func TestJSInfiniteLoopTimesOut(t *testing.T) {
	program, err := NewJavaScript().Compile(`while (true) {}`)
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
		t.Fatal("interrupt did not stop the loop")
	}
}

func TestJSSandboxBlocksHostAccess(t *testing.T) {
	blocked := []string{"require", "fetch", "process", "console", "global", "globalThis.require", "XMLHttpRequest"}
	for _, name := range blocked {
		t.Run(name, func(t *testing.T) {
			got, err := runJS(t, NewJavaScript(),
				`return (typeof `+name+` === "undefined") ? ["blocked"] : ["reachable"];`)
			if err != nil {
				// 对 globalThis.require 这类表达式，抛错同样说明不可达。
				return
			}
			if len(got.Candidates) != 1 || got.Candidates[0] != "blocked" {
				t.Errorf("%s must not be reachable from a policy script", name)
			}
		})
	}
}

func TestJSConcurrentExecutionsDoNotShareState(t *testing.T) {
	// 显式写全局对象：若 Runtime 被复用，并发执行会互相看到对方的值。
	program, err := NewJavaScript().Compile(`
		if (typeof globalThis.leaked !== "undefined") { return ["leaked"]; }
		globalThis.leaked = input.request.request_id;
		return [globalThis.leaked];
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

func TestTypeScriptTranspilesAndRuns(t *testing.T) {
	got, err := runJS(t, NewTypeScript(), `
		interface Member { model_id: string; enabled: boolean; }
		const out: string[] = [];
		input.collection.groups.forEach((group: any) => {
			group.members.forEach((member: Member) => {
				if (member.enabled) { out.push(member.model_id); }
			});
		});
		return out;
	`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Join(got.Candidates, ",") != "kimi-1/k3,ark-1/ds,deepseek-1/v4" {
		t.Errorf("candidates = %v", got.Candidates)
	}
}

func TestTypeScriptSyntaxErrorCarriesPosition(t *testing.T) {
	_, err := NewTypeScript().Compile("const x: = 1;\nreturn [];")
	var ce *policy.CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *policy.CompileError", err)
	}
	if ce.Language != policy.LangTypeScript {
		t.Errorf("language = %s", ce.Language)
	}
	if ce.Line == 0 {
		t.Errorf("transpile error must report a line: %+v", ce)
	}
}

func TestTypeScriptTypeAnnotationsAreStripped(t *testing.T) {
	got, err := runJS(t, NewTypeScript(), `
		type Decision = { candidates: string[]; note?: string };
		const d: Decision = { candidates: ["kimi-1/k3"], note: "typed" };
		return d;
	`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Note != "typed" || len(got.Candidates) != 1 {
		t.Errorf("decision = %+v", got)
	}
}

func TestJSLanguageIdentity(t *testing.T) {
	if NewJavaScript().Language() != policy.LangJavaScript {
		t.Error("javascript runtime must report its language")
	}
	if NewTypeScript().Language() != policy.LangTypeScript {
		t.Error("typescript runtime must report its language")
	}
	if NewLua().Language() != policy.LangLua {
		t.Error("lua runtime must report its language")
	}
}
