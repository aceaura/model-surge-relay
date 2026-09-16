// Package examples 内嵌五个示例 Lua 策略，覆盖老 replay 的五种固定 Policy 语义。
//
// 这些脚本是运维者的起点模板，也是策略契约的活文档：字段名一旦改动，
// 本包的测试就会失败。
package examples

import (
	"embed"
	"fmt"
)

//go:embed *.lua
var scriptFS embed.FS

// 示例名。
const (
	Preset     = "preset"
	Sticky     = "sticky"
	Failover   = "failover"
	RoundRobin = "round_robin"
	LeastUsed  = "least_used"
)

func Names() []string {
	return []string{Preset, Sticky, Failover, RoundRobin, LeastUsed}
}

// Source 返回示例脚本源码。
func Source(name string) (string, error) {
	raw, err := scriptFS.ReadFile(name + ".lua")
	if err != nil {
		return "", fmt.Errorf("unknown example policy %q", name)
	}
	return string(raw), nil
}
