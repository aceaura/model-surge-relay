// Package runtime 实现策略语言的进程内嵌入式引擎。
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/aceaura/model-surge-relay/backend/policy"
	lua "github.com/yuin/gopher-lua"
	luaparse "github.com/yuin/gopher-lua/parse"
)

// maxLuaInstructions 是单次执行的指令上限，防止脚本用纯计算耗死 worker。
// 超时是第二道防线，但指令上限能在无系统调用的死循环里更快生效。
const maxLuaInstructions = 20_000_000

type Lua struct{}

func NewLua() *Lua { return &Lua{} }

func (*Lua) Language() policy.Language { return policy.LangLua }

func (*Lua) Compile(source string) (policy.Program, error) {
	chunk, err := luaparse.Parse(strings.NewReader(source), "policy")
	if err != nil {
		return nil, luaCompileError(err)
	}
	proto, err := lua.Compile(chunk, "policy")
	if err != nil {
		return nil, luaCompileError(err)
	}
	return &luaProgram{proto: proto}, nil
}

// luaProgram 持有可跨执行复用的编译产物；LState 每次执行新建，
// 因此并发执行之间不共享任何可变状态。
type luaProgram struct {
	proto *lua.FunctionProto
}

func (p *luaProgram) Execute(ctx context.Context, in policy.Input) (out policy.Decision, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			out = policy.Decision{}
			err = fmt.Errorf("lua policy panicked: %v", rec)
		}
	}()

	state := lua.NewState(lua.Options{
		SkipOpenLibs:        true,
		RegistrySize:        1024,
		CallStackSize:       200,
		IncludeGoStackTrace: false,
	})
	defer state.Close()

	openSafeLuaLibs(state)
	state.SetContext(ctx)
	state.SetMx(maxLuaInstructions)

	input, err := toLuaValue(state, in)
	if err != nil {
		return policy.Decision{}, err
	}
	state.SetGlobal("input", input)

	state.Push(state.NewFunctionFromProto(p.proto))
	if err := state.PCall(0, 1, nil); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return policy.Decision{}, policy.ErrTimeout
		}
		if strings.Contains(err.Error(), "instructions") {
			return policy.Decision{}, policy.ErrTimeout
		}
		return policy.Decision{}, fmt.Errorf("lua policy failed: %w", err)
	}

	result := state.Get(-1)
	state.Pop(1)
	return decodeLuaDecision(result)
}

// openSafeLuaLibs 只装载纯计算库：不装 io / os / package / debug，
// 因此脚本没有文件、网络、进程环境与动态加载能力。
func openSafeLuaLibs(state *lua.LState) {
	for _, lib := range []struct {
		name string
		fn   lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		state.Push(state.NewFunction(lib.fn))
		state.Push(lua.LString(lib.name))
		state.Call(1, 0)
	}
	// 即便 base 库也带有加载能力，逐一摘掉。
	for _, name := range []string{"dofile", "loadfile", "load", "loadstring", "require", "collectgarbage", "print"} {
		state.SetGlobal(name, lua.LNil)
	}
}

// toLuaValue 经 JSON 中转把 Input 转成 Lua table。
// 中转带来一次序列化开销，换来的是与 JS 运行时完全一致的字段视图。
func toLuaValue(state *lua.LState, in policy.Input) (lua.LValue, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("encode policy input: %w", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("decode policy input: %w", err)
	}
	return goToLua(state, generic), nil
}

func goToLua(state *lua.LState, v any) lua.LValue {
	switch value := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(value)
	case float64:
		return lua.LNumber(value)
	case string:
		return lua.LString(value)
	case []any:
		table := state.NewTable()
		for _, item := range value {
			table.Append(goToLua(state, item))
		}
		return table
	case map[string]any:
		table := state.NewTable()
		for k, item := range value {
			table.RawSetString(k, goToLua(state, item))
		}
		return table
	default:
		return lua.LNil
	}
}

func decodeLuaDecision(v lua.LValue) (policy.Decision, error) {
	switch value := v.(type) {
	case *lua.LNilType:
		return policy.Decision{Candidates: []string{}}, nil
	case *lua.LTable:
		return decodeLuaTable(value)
	default:
		return policy.Decision{}, fmt.Errorf("lua policy returned %s, want table", v.Type())
	}
}

func decodeLuaTable(table *lua.LTable) (policy.Decision, error) {
	out := policy.Decision{Candidates: []string{}}

	// 形态一：候选数组。
	if table.Len() > 0 {
		var badType error
		table.ForEach(func(key, value lua.LValue) {
			if badType != nil {
				return
			}
			if _, ok := key.(lua.LNumber); !ok {
				return
			}
			s, ok := value.(lua.LString)
			if !ok {
				badType = fmt.Errorf("candidate at index %s is %s, want string", key.String(), value.Type())
				return
			}
			out.Candidates = append(out.Candidates, string(s))
		})
		if badType != nil {
			return policy.Decision{}, badType
		}
		return out, nil
	}

	// 形态二：{candidates = {...}, note = "..."}。
	if note, ok := table.RawGetString("note").(lua.LString); ok {
		out.Note = string(note)
	}
	switch candidates := table.RawGetString("candidates").(type) {
	case *lua.LNilType:
		return out, nil
	case *lua.LTable:
		nested, err := decodeLuaTable(candidates)
		if err != nil {
			return policy.Decision{}, err
		}
		out.Candidates = nested.Candidates
		return out, nil
	default:
		return policy.Decision{}, fmt.Errorf("candidates is %s, want table", candidates.Type())
	}
}

// luaPosition 匹配 gopher-lua 的两种位置格式：
// 解析期 "policy line:2(column:7)"，运行期 "policy:2:"。
var luaPosition = regexp.MustCompile(`line:(\d+)\(column:(\d+)\)|policy:(\d+):`)

func luaCompileError(err error) error {
	ce := &policy.CompileError{Language: policy.LangLua, Message: err.Error()}
	var apiErr *lua.ApiError
	if errors.As(err, &apiErr) {
		ce.Message = apiErr.Object.String()
	}
	if m := luaPosition.FindStringSubmatch(err.Error()); m != nil {
		if m[1] != "" {
			ce.Line, _ = strconv.Atoi(m[1])
			ce.Column, _ = strconv.Atoi(m[2])
		} else {
			ce.Line, _ = strconv.Atoi(m[3])
		}
	}
	return ce
}
