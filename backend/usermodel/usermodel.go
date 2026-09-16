// Package usermodel 持有对外暴露的 user model：绑定 Collection 与可选策略，
// 并承担调度面的客户端鉴权。
package usermodel

import "time"

// UserModel 是一个对外模型名。Policy 为空表示未绑定策略，调度走兜底顺序。
type UserModel struct {
	Name       string    `json:"name"`
	Collection string    `json:"collection"`
	Policy     string    `json:"policy,omitempty"`
	Protocol   string    `json:"protocol,omitempty"`
	Enabled    bool      `json:"enabled"`
	Note       string    `json:"note,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// ClientKey 是调用方密钥。不出 JSON：读取面与日志都不该带上它。
	ClientKey string `json:"-"`
}
