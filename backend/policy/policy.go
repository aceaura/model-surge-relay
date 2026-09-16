// Package policy 持有具名动态策略：语言运行时注册、编译缓存与沙箱执行。
//
// 策略只做排序，不做解析：输出是有序的 upstream model 引用列表，
// 取凭据与合并参数始终由调度层在策略返回后完成。这条边界让凭据
// 永不进入脚本运行时。
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aceaura/model-surge-relay/backend/collection"
)

type Language string

const (
	LangLua        Language = "lua"
	LangJavaScript Language = "javascript"
	LangTypeScript Language = "typescript"
)

// Languages 是受支持语言的封闭集合。集合外的标识在保存时即被拒绝，
// 热路径上不会出现未实现的分支。
func Languages() []Language {
	return []Language{LangLua, LangJavaScript, LangTypeScript}
}

func LanguageNames() []string {
	out := make([]string, 0, 3)
	for _, l := range Languages() {
		out = append(out, string(l))
	}
	return out
}

// Policy 是持久化的策略记录。
type Policy struct {
	Name      string    `json:"name"`
	Language  Language  `json:"language"`
	Source    string    `json:"source"`
	Version   int       `json:"version"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RequestContext 是本次请求的上下文，刻意不含消息内容与客户端密钥。
type RequestContext struct {
	UserModel       string   `json:"user_model"`
	InboundProtocol string   `json:"inbound_protocol"`
	EstTokens       int      `json:"est_tokens"`
	TriedIDs        []string `json:"tried_ids"`
	RequestID       string   `json:"request_id"`
}

// State 是单个目标的运行态视图。
type State struct {
	Cooling             bool  `json:"cooling"`
	CoolingUntil        int64 `json:"cooling_until,omitempty"` // Unix 秒，0 = 未冷却
	ConsecutiveFailures int   `json:"consecutive_failures"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	RequestCount        int64 `json:"request_count"`
}

// Input 是传给脚本的唯一入口。结构里没有任何凭据字段，
// 因此"策略读不到凭据"由类型定义保证，而非运行时过滤。
type Input struct {
	Request    RequestContext      `json:"request"`
	Collection collection.Snapshot `json:"collection"`
	Runtime    map[string]State    `json:"runtime"`
}

// MarshalJSON 保证 runtime 与 tried_ids 恒为容器而非 null。
// 脚本直接索引 input.runtime[id]、遍历 tried_ids，null 会让它们炸在第一行。
func (in Input) MarshalJSON() ([]byte, error) {
	type alias Input
	if in.Runtime == nil {
		in.Runtime = map[string]State{}
	}
	if in.Request.TriedIDs == nil {
		in.Request.TriedIDs = []string{}
	}
	return json.Marshal(alias(in))
}

// Decision 是脚本返回值。
type Decision struct {
	Candidates []string `json:"candidates"`
	Note       string   `json:"note,omitempty"`
}

// Program 是编译产物，必须支持并发执行且各次执行不共享可变状态。
type Program interface {
	Execute(ctx context.Context, in Input) (Decision, error)
}

// Runtime 是语言 provider 的统一契约。
type Runtime interface {
	Language() Language
	Compile(source string) (Program, error)
}

// CompileError 携带出错位置，供管理面回显。
type CompileError struct {
	Language Language
	Line     int
	Column   int
	Message  string
}

func (e *CompileError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s compile error at line %d:%d: %s", e.Language, e.Line, e.Column, e.Message)
	}
	return fmt.Sprintf("%s compile error: %s", e.Language, e.Message)
}

// ErrTimeout 是执行超时。调度层映射为可重试错误。
var ErrTimeout = errors.New("policy execution timed out")

// Registry 按语言查找 provider。
type Registry struct {
	runtimes map[Language]Runtime
}

func NewRegistry(runtimes ...Runtime) *Registry {
	r := &Registry{runtimes: make(map[Language]Runtime, len(runtimes))}
	for _, rt := range runtimes {
		r.runtimes[rt.Language()] = rt
	}
	return r
}

func (r *Registry) Get(lang Language) (Runtime, bool) {
	rt, ok := r.runtimes[Language(strings.ToLower(string(lang)))]
	return rt, ok
}

// Supported 返回本部署实际注册的语言标识，按字典序排列以便稳定回显。
func (r *Registry) Supported() []string {
	out := make([]string, 0, len(r.runtimes))
	for lang := range r.runtimes {
		out = append(out, string(lang))
	}
	sort.Strings(out)
	return out
}

// decodeDecision 把脚本返回的松散结构收成 Decision。
// 允许两种形态：候选字符串数组，或含 candidates 字段的对象。
func decodeDecision(raw any) (Decision, error) {
	switch v := raw.(type) {
	case nil:
		return Decision{Candidates: []string{}}, nil
	case []any:
		return decisionFromSlice(v)
	case []string:
		return Decision{Candidates: v}, nil
	case map[string]any:
		return decisionFromMap(v)
	default:
		encoded, err := json.Marshal(raw)
		if err != nil {
			return Decision{}, fmt.Errorf("policy returned unsupported value of type %T", raw)
		}
		var d Decision
		if err := json.Unmarshal(encoded, &d); err != nil {
			return Decision{}, fmt.Errorf("policy returned unsupported value of type %T", raw)
		}
		if d.Candidates == nil {
			d.Candidates = []string{}
		}
		return d, nil
	}
}

func decisionFromSlice(items []any) (Decision, error) {
	out := Decision{Candidates: make([]string, 0, len(items))}
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			return Decision{}, fmt.Errorf("candidate %d is %T, want string", i, item)
		}
		out.Candidates = append(out.Candidates, s)
	}
	return out, nil
}

func decisionFromMap(m map[string]any) (Decision, error) {
	out := Decision{Candidates: []string{}}
	if note, ok := m["note"].(string); ok {
		out.Note = note
	}
	switch list := m["candidates"].(type) {
	case nil:
		return out, nil
	case []any:
		parsed, err := decisionFromSlice(list)
		if err != nil {
			return Decision{}, err
		}
		out.Candidates = parsed.Candidates
	case []string:
		out.Candidates = list
	default:
		return Decision{}, fmt.Errorf("candidates is %T, want array of strings", list)
	}
	return out, nil
}
