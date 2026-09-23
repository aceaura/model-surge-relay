// Package examples 内嵌可直接套用的示例策略：五个 Lua 覆盖老 replay 的固定
// Policy 语义，一个 JavaScript 演示分组分流与窗口判定。
//
// 这些脚本是运维者的起点模板，也是策略契约的活文档：字段名一旦改动，
// 本包的测试就会失败。
package examples

import (
	"embed"
	"fmt"

	"github.com/aceaura/model-surge-relay/backend/policy"
)

//go:embed *.lua *.js
var scriptFS embed.FS

// Lua 示例名，覆盖老 replay 的五种固定 Policy 语义。
const (
	Preset     = "preset"
	Sticky     = "sticky"
	Failover   = "failover"
	RoundRobin = "round_robin"
	LeastUsed  = "least_used"
)

// JavaScript 示例名。CompactOverflow 同时是首个 JS 范例，演示按 group.type
// 分流、用 group.config 传参、以及 est_tokens 对比 context_window 的窗口判定。
const (
	CompactOverflow = "compact_overflow"
)

// example 把一个示例名绑定到它的源码文件与语言。
type example struct {
	name string
	file string
	lang policy.Language
}

// registry 的顺序即 Names() 的顺序。
var registry = []example{
	{name: Preset, file: Preset + ".lua", lang: policy.LangLua},
	{name: Sticky, file: Sticky + ".lua", lang: policy.LangLua},
	{name: Failover, file: Failover + ".lua", lang: policy.LangLua},
	{name: RoundRobin, file: RoundRobin + ".lua", lang: policy.LangLua},
	{name: LeastUsed, file: LeastUsed + ".lua", lang: policy.LangLua},
	{name: CompactOverflow, file: CompactOverflow + ".js", lang: policy.LangJavaScript},
}

func lookup(name string) (example, bool) {
	for _, e := range registry {
		if e.name == name {
			return e, true
		}
	}
	return example{}, false
}

// Names 返回全部示例名，顺序即注册顺序。
func Names() []string {
	out := make([]string, 0, len(registry))
	for _, e := range registry {
		out = append(out, e.name)
	}
	return out
}

// Language 返回示例的策略语言，调用方据此选择运行时。
func Language(name string) (policy.Language, error) {
	e, ok := lookup(name)
	if !ok {
		return "", fmt.Errorf("unknown example policy %q", name)
	}
	return e.lang, nil
}

// Source 返回示例脚本源码。
func Source(name string) (string, error) {
	e, ok := lookup(name)
	if !ok {
		return "", fmt.Errorf("unknown example policy %q", name)
	}
	raw, err := scriptFS.ReadFile(e.file)
	if err != nil {
		return "", fmt.Errorf("read example policy %q: %w", name, err)
	}
	return string(raw), nil
}
