// Package collection 持有 Collection / Group / 成员三层配置，
// 并把成员引用与 upstream 目录属性连接成调度快照。
package collection

import (
	"encoding/json"
	"time"
)

type Collection struct {
	Name      string    `json:"name"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Group 的 Type 是数据库中的自由文本，不是代码枚举：
// 策略脚本按业务语义读取它，服务本身不解释其含义。
type Group struct {
	Collection string          `json:"collection"`
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Position   int             `json:"position"`
	Config     json.RawMessage `json:"config,omitempty"`
	Members    []string        `json:"members"`
}

// Snapshot 是策略输入与调度过滤共用的视图：成员已带目录属性。
type Snapshot struct {
	Name   string          `json:"name"`
	Groups []GroupSnapshot `json:"groups"`
}

// MarshalJSON 保证 groups 恒为数组而非 null：策略脚本直接遍历这个字段，
// null 会让每个脚本都得先判空。空集合的表示形态是类型的责任，不是脚本的。
func (s Snapshot) MarshalJSON() ([]byte, error) {
	type alias Snapshot
	if s.Groups == nil {
		s.Groups = []GroupSnapshot{}
	}
	return json.Marshal(alias(s))
}

type GroupSnapshot struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Position int             `json:"position"`
	Config   json.RawMessage `json:"config,omitempty"`
	Members  []Member        `json:"members"`
}

// MarshalJSON 同理保证 members 恒为数组。
func (g GroupSnapshot) MarshalJSON() ([]byte, error) {
	type alias GroupSnapshot
	if g.Members == nil {
		g.Members = []Member{}
	}
	return json.Marshal(alias(g))
}

// Member 是成员引用叠加 upstream 目录属性的结果。
// Known 为假表示该引用在当前目录中已消失（账号或模型被删）。
type Member struct {
	ModelID       string `json:"model_id"`
	Account       string `json:"account"`
	ProviderID    string `json:"provider_id"`
	Protocol      string `json:"protocol"`
	NativeModel   string `json:"native_model"`
	ContextWindow int    `json:"context_window,omitempty"`
	Enabled       bool   `json:"enabled"`
	Known         bool   `json:"known"`
	Position      int    `json:"position"`
}

// Has 报告某引用是否属于该快照，用于拒绝策略返回的越界引用。
func (s Snapshot) Has(modelID string) bool {
	for _, g := range s.Groups {
		for _, m := range g.Members {
			if m.ModelID == modelID {
				return true
			}
		}
	}
	return false
}

// Locate 返回某引用所属的组，便于决策溯源标注命中组。
func (s Snapshot) Locate(modelID string) (GroupSnapshot, bool) {
	for _, g := range s.Groups {
		for _, m := range g.Members {
			if m.ModelID == modelID {
				return g, true
			}
		}
	}
	return GroupSnapshot{}, false
}

// Member 返回某引用的目录属性。
func (s Snapshot) Member(modelID string) (Member, bool) {
	for _, g := range s.Groups {
		for _, m := range g.Members {
			if m.ModelID == modelID {
				return m, true
			}
		}
	}
	return Member{}, false
}
