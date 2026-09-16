package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"github.com/aceaura/model-surge-relay/backend/policy"
	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
)

// JS 实现 javascript 与 typescript 两种语言：
// TypeScript 先经 esbuild 转译，其后与 JavaScript 共用 goja 执行路径。
type JS struct {
	lang policy.Language
}

func NewJavaScript() *JS { return &JS{lang: policy.LangJavaScript} }

func NewTypeScript() *JS { return &JS{lang: policy.LangTypeScript} }

func (r *JS) Language() policy.Language { return r.lang }

func (r *JS) Compile(source string) (policy.Program, error) {
	script := source
	if r.lang == policy.LangTypeScript {
		transpiled, err := transpileTypeScript(source)
		if err != nil {
			return nil, err
		}
		script = transpiled
	}
	// 包成函数体：脚本用 return 返回决策，与 Lua 侧写法一致。
	wrapped := "(function(input){\n" + script + "\n})"
	program, err := goja.Compile("policy", wrapped, true)
	if err != nil {
		return nil, jsCompileError(r.lang, err)
	}
	return &jsProgram{lang: r.lang, program: program}, nil
}

func transpileTypeScript(source string) (string, error) {
	result := api.Transform(source, api.TransformOptions{
		Loader: api.LoaderTS,
		Target: api.ES2015,
		Format: api.FormatDefault,
	})
	if len(result.Errors) > 0 {
		first := result.Errors[0]
		ce := &policy.CompileError{Language: policy.LangTypeScript, Message: first.Text}
		if first.Location != nil {
			ce.Line = first.Location.Line
			ce.Column = first.Location.Column
		}
		return "", ce
	}
	return string(result.Code), nil
}

// jsProgram 持有可跨执行复用的编译产物；goja.Runtime 每次执行新建，
// 因此并发执行之间不共享任何可变状态。
type jsProgram struct {
	lang    policy.Language
	program *goja.Program
}

func (p *jsProgram) Execute(ctx context.Context, in policy.Input) (out policy.Decision, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			out = policy.Decision{}
			err = fmt.Errorf("%s policy panicked: %v", p.lang, rec)
		}
	}()

	vm := goja.New()
	// 不注入任何宿主对象：goja 默认没有 require / fetch / process / console，
	// 因此脚本没有文件、网络与进程环境能力。

	stop := watchdog(ctx, vm)
	defer stop()

	value, err := vm.RunProgram(p.program)
	if err != nil {
		return policy.Decision{}, p.wrapExecError(ctx, err)
	}
	fn, ok := goja.AssertFunction(value)
	if !ok {
		return policy.Decision{}, fmt.Errorf("%s policy did not compile into a callable", p.lang)
	}

	input, err := toJSValue(vm, in)
	if err != nil {
		return policy.Decision{}, err
	}

	result, err := fn(goja.Undefined(), input)
	if err != nil {
		return policy.Decision{}, p.wrapExecError(ctx, err)
	}
	if result == nil || goja.IsUndefined(result) || goja.IsNull(result) {
		return policy.Decision{Candidates: []string{}}, nil
	}
	return decodeJSDecision(result.Export())
}

// watchdog 在 ctx 结束时打断 VM。goja 没有指令计数上限，
// 中断是唯一能停下纯计算死循环的手段。
func watchdog(ctx context.Context, vm *goja.Runtime) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt(policy.ErrTimeout)
		case <-done:
		}
	}()
	return func() { close(done) }
}

func (p *jsProgram) wrapExecError(ctx context.Context, err error) error {
	var interrupted *goja.InterruptedError
	if asInterrupted(err, &interrupted) {
		return policy.ErrTimeout
	}
	if ctx.Err() != nil {
		return policy.ErrTimeout
	}
	return fmt.Errorf("%s policy failed: %w", p.lang, err)
}

func asInterrupted(err error, target **goja.InterruptedError) bool {
	if e, ok := err.(*goja.InterruptedError); ok {
		*target = e
		return true
	}
	return false
}

// toJSValue 经 JSON 中转把 Input 转成普通 JS 对象，
// 字段视图与 Lua 侧完全一致。
func toJSValue(vm *goja.Runtime, in policy.Input) (goja.Value, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("encode policy input: %w", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("decode policy input: %w", err)
	}
	return vm.ToValue(generic), nil
}

func decodeJSDecision(raw any) (policy.Decision, error) {
	switch v := raw.(type) {
	case []any:
		out := policy.Decision{Candidates: make([]string, 0, len(v))}
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return policy.Decision{}, fmt.Errorf("candidate %d is %T, want string", i, item)
			}
			out.Candidates = append(out.Candidates, s)
		}
		return out, nil
	case map[string]any:
		out := policy.Decision{Candidates: []string{}}
		if note, ok := v["note"].(string); ok {
			out.Note = note
		}
		switch list := v["candidates"].(type) {
		case nil:
			return out, nil
		case []any:
			nested, err := decodeJSDecision(list)
			if err != nil {
				return policy.Decision{}, err
			}
			out.Candidates = nested.Candidates
			return out, nil
		default:
			return policy.Decision{}, fmt.Errorf("candidates is %T, want an array of strings", list)
		}
	default:
		return policy.Decision{}, fmt.Errorf("policy returned %T, want an array or object", raw)
	}
}

// jsPosition 匹配 goja 编译错误里的 "Line 3:5" 位置。
var jsPosition = regexp.MustCompile(`Line (\d+):(\d+)`)

func jsCompileError(lang policy.Language, err error) error {
	ce := &policy.CompileError{Language: lang, Message: err.Error()}
	if m := jsPosition.FindStringSubmatch(err.Error()); m != nil {
		ce.Line, _ = strconv.Atoi(m[1])
		ce.Column, _ = strconv.Atoi(m[2])
		// 包装成函数体引入了一行偏移，还原为用户源码的行号。
		if ce.Line > 1 {
			ce.Line--
		}
	}
	return ce
}
