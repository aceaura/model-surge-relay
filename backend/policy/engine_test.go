package policy

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
)

type fakeRuntime struct {
	lang     Language
	compiles atomic.Int64
	// compileErr 非空时 Compile 直接失败。
	compileErr error
	exec       func(ctx context.Context, in Input) (Decision, error)
}

func (r *fakeRuntime) Language() Language { return r.lang }

func (r *fakeRuntime) Compile(source string) (Program, error) {
	r.compiles.Add(1)
	if r.compileErr != nil {
		return nil, r.compileErr
	}
	return &fakeProgram{source: source, exec: r.exec}, nil
}

type fakeProgram struct {
	source string
	exec   func(ctx context.Context, in Input) (Decision, error)
}

func (p *fakeProgram) Execute(ctx context.Context, in Input) (Decision, error) {
	if p.exec != nil {
		return p.exec(ctx, in)
	}
	return Decision{Candidates: []string{p.source}}, nil
}

func newTestEngine(rt *fakeRuntime) *Engine {
	return NewEngine(NewRegistry(rt), 100*time.Millisecond)
}

func TestEngineReusesCompiledProgramForSameNameAndVersion(t *testing.T) {
	rt := &fakeRuntime{lang: LangLua}
	e := newTestEngine(rt)
	p := Policy{Name: "preset", Language: LangLua, Source: "v1", Version: 1}

	for i := 0; i < 5; i++ {
		if _, err := e.Execute(context.Background(), p, Input{}); err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
	}
	if got := rt.compiles.Load(); got != 1 {
		t.Fatalf("compiles = %d, want 1", got)
	}
}

func TestEngineRecompilesAfterVersionBump(t *testing.T) {
	rt := &fakeRuntime{lang: LangLua}
	e := newTestEngine(rt)

	d, err := e.Execute(context.Background(), Policy{Name: "p", Language: LangLua, Source: "v1", Version: 1}, Input{})
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	if d.Candidates[0] != "v1" {
		t.Fatalf("candidates = %v, want v1", d.Candidates)
	}

	d, err = e.Execute(context.Background(), Policy{Name: "p", Language: LangLua, Source: "v2", Version: 2}, Input{})
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	if d.Candidates[0] != "v2" {
		t.Fatalf("candidates = %v, want v2", d.Candidates)
	}
	if got := rt.compiles.Load(); got != 2 {
		t.Fatalf("compiles = %d, want 2", got)
	}
}

func TestEngineDropsStaleVersionsFromCache(t *testing.T) {
	rt := &fakeRuntime{lang: LangLua}
	e := newTestEngine(rt)
	for v := 1; v <= 3; v++ {
		p := Policy{Name: "p", Language: LangLua, Source: fmt.Sprintf("v%d", v), Version: v}
		if _, err := e.Execute(context.Background(), p, Input{}); err != nil {
			t.Fatalf("version %d: %v", v, err)
		}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.programs) != 1 {
		t.Fatalf("cached programs = %d, want 1 (only newest version)", len(e.programs))
	}
	if _, ok := e.programs["p@3"]; !ok {
		t.Fatalf("cache keys = %v, want p@3", e.programs)
	}
}

func TestEngineCachesPerPolicyName(t *testing.T) {
	rt := &fakeRuntime{lang: LangLua}
	e := newTestEngine(rt)
	a := Policy{Name: "a", Language: LangLua, Source: "sa", Version: 1}
	b := Policy{Name: "b", Language: LangLua, Source: "sb", Version: 1}
	for _, p := range []Policy{a, b, a, b} {
		if _, err := e.Execute(context.Background(), p, Input{}); err != nil {
			t.Fatalf("execute %s: %v", p.Name, err)
		}
	}
	if got := rt.compiles.Load(); got != 2 {
		t.Fatalf("compiles = %d, want 2", got)
	}
}

func TestEngineConcurrentExecuteSharesOneProgram(t *testing.T) {
	rt := &fakeRuntime{lang: LangJavaScript}
	e := NewEngine(NewRegistry(rt), time.Second)
	p := Policy{Name: "hot", Language: LangJavaScript, Source: "src", Version: 7}

	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 20; j++ {
				d, err := e.Execute(context.Background(), p, Input{})
				if err != nil {
					errs <- err
					return
				}
				if len(d.Candidates) != 1 || d.Candidates[0] != "src" {
					errs <- fmt.Errorf("candidates = %v", d.Candidates)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent execute: %v", err)
	}
	// 并发首次执行允许各自编译，但最终只保留一份共享产物。
	e.mu.RLock()
	cached := len(e.programs)
	e.mu.RUnlock()
	if cached != 1 {
		t.Fatalf("cached programs = %d, want 1", cached)
	}
}

func TestEngineValidateAcceptsCompilableSource(t *testing.T) {
	rt := &fakeRuntime{lang: LangLua}
	e := newTestEngine(rt)
	if err := e.Validate(LangLua, "return {}"); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestEngineValidateDoesNotPrimeCache(t *testing.T) {
	rt := &fakeRuntime{lang: LangLua}
	e := newTestEngine(rt)
	if err := e.Validate(LangLua, "src"); err != nil {
		t.Fatalf("validate: %v", err)
	}
	e.mu.RLock()
	cached := len(e.programs)
	e.mu.RUnlock()
	if cached != 0 {
		t.Fatalf("cached programs = %d, want 0", cached)
	}
}

func TestEngineValidateRejectsUnsupportedLanguage(t *testing.T) {
	rt := &fakeRuntime{lang: LangLua}
	e := newTestEngine(rt)
	err := e.Validate("python", "print(1)")
	appErr := asAppErr(t, err)
	if appErr.Code != apperr.InvalidRequest {
		t.Fatalf("code = %s, want invalid_request", appErr.Code)
	}
	if appErr.Field != "language" {
		t.Fatalf("field = %q, want language", appErr.Field)
	}
	if !strings.Contains(appErr.Message, "lua") {
		t.Fatalf("message %q should list supported languages", appErr.Message)
	}
}

func TestEngineValidateSurfacesCompileErrorPosition(t *testing.T) {
	rt := &fakeRuntime{
		lang:       LangLua,
		compileErr: &CompileError{Language: LangLua, Line: 4, Column: 9, Message: "unexpected symbol"},
	}
	e := newTestEngine(rt)
	err := e.Validate(LangLua, "bad source")
	appErr := asAppErr(t, err)
	if appErr.Code != apperr.InvalidRequest {
		t.Fatalf("code = %s, want invalid_request", appErr.Code)
	}
	if appErr.Field != "source" {
		t.Fatalf("field = %q, want source", appErr.Field)
	}
	if !strings.Contains(appErr.Message, "line 4:9") {
		t.Fatalf("message %q should carry position", appErr.Message)
	}
}

func TestEngineExecuteMapsTimeoutToRetryable(t *testing.T) {
	rt := &fakeRuntime{lang: LangLua, exec: func(context.Context, Input) (Decision, error) {
		return Decision{}, fmt.Errorf("wrapped: %w", ErrTimeout)
	}}
	e := newTestEngine(rt)
	_, err := e.Execute(context.Background(), Policy{Name: "slow", Language: LangLua, Version: 1}, Input{})
	appErr := asAppErr(t, err)
	if appErr.Code != apperr.PolicyTimeout {
		t.Fatalf("code = %s, want policy_timeout", appErr.Code)
	}
	if !appErr.Retryable {
		t.Fatal("policy_timeout should be retryable")
	}
	if !strings.Contains(appErr.Message, "slow") {
		t.Fatalf("message %q should name the policy", appErr.Message)
	}
}

func TestEngineExecuteMapsRuntimeErrorToNonRetryable(t *testing.T) {
	rt := &fakeRuntime{lang: LangLua, exec: func(context.Context, Input) (Decision, error) {
		return Decision{}, errors.New("attempt to index a nil value")
	}}
	e := newTestEngine(rt)
	_, err := e.Execute(context.Background(), Policy{Name: "buggy", Language: LangLua, Version: 3}, Input{})
	appErr := asAppErr(t, err)
	if appErr.Code != apperr.PolicyError {
		t.Fatalf("code = %s, want policy_error", appErr.Code)
	}
	if appErr.Retryable {
		t.Fatal("policy_error should not be retryable")
	}
	if !strings.Contains(appErr.Message, "version 3") {
		t.Fatalf("message %q should carry the version", appErr.Message)
	}
}

func TestEngineExecuteAppliesTimeoutToContext(t *testing.T) {
	var deadlineSet bool
	rt := &fakeRuntime{lang: LangLua, exec: func(ctx context.Context, _ Input) (Decision, error) {
		_, deadlineSet = ctx.Deadline()
		return Decision{Candidates: []string{"a"}}, nil
	}}
	e := NewEngine(NewRegistry(rt), 50*time.Millisecond)
	if _, err := e.Execute(context.Background(), Policy{Name: "p", Language: LangLua, Version: 1}, Input{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !deadlineSet {
		t.Fatal("execute should pass a deadline-bearing context to the program")
	}
}

func TestEngineExecuteNormalizesNilCandidates(t *testing.T) {
	rt := &fakeRuntime{lang: LangLua, exec: func(context.Context, Input) (Decision, error) {
		return Decision{Note: "none"}, nil
	}}
	e := newTestEngine(rt)
	d, err := e.Execute(context.Background(), Policy{Name: "p", Language: LangLua, Version: 1}, Input{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if d.Candidates == nil {
		t.Fatal("candidates should be an empty slice, not nil")
	}
}

func TestNewEngineFallsBackToDefaultTimeout(t *testing.T) {
	e := NewEngine(NewRegistry(), 0)
	if e.timeout != DefaultTimeout {
		t.Fatalf("timeout = %s, want %s", e.timeout, DefaultTimeout)
	}
}

// TestInputCarriesNoCredentialFields 断言脚本入口结构里不存在凭据类字段。
// 这条不变式是"策略读不到凭据"的编译期保证，因此用反射逐层核查。
func TestInputCarriesNoCredentialFields(t *testing.T) {
	// 按标识符片段精确比对：EstTokens 这类计数字段不该被误判。
	banned := map[string]bool{
		"credential": true, "credentials": true, "apikey": true, "key": true,
		"token": true, "accesstoken": true, "refreshtoken": true,
		"secret": true, "password": true, "headers": true, "header": true,
		"authorization": true, "auth": true, "clientkey": true, "bearer": true,
	}
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			for _, name := range identifierForms(f) {
				if banned[name] {
					t.Fatalf("%s.%s exposes credential-shaped field %q", path, f.Name, name)
				}
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	walk(reflect.TypeOf(Input{}), "Input")
}

// identifierForms 返回字段名的可比对形态：完整名、json 名与末段驼峰词。
// 只比对末段是因为凭据字段惯例上以类型词结尾（Credential / APIKey / Headers）。
func identifierForms(f reflect.StructField) []string {
	out := []string{strings.ToLower(f.Name)}
	if tag := strings.Split(f.Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
		out = append(out, strings.ToLower(strings.ReplaceAll(tag, "_", "")))
		if parts := strings.Split(tag, "_"); len(parts) > 1 {
			out = append(out, strings.ToLower(parts[len(parts)-1]))
		}
	}
	start := 0
	for i, r := range f.Name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			start = i
		}
	}
	out = append(out, strings.ToLower(f.Name[start:]))
	return out
}

func asAppErr(t *testing.T, err error) *apperr.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var e *apperr.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not *apperr.Error", err)
	}
	return e
}
