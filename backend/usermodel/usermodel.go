// Package usermodel 持有对外暴露的 user model：绑定 Collection，
// 并承担调度面的客户端鉴权。策略组合挂在 Collection 上，本层不再绑策略。
package usermodel

import "time"

// UserModel 是一个对外模型名。
type UserModel struct {
	Name       string    `json:"name"`
	Collection string    `json:"collection"`
	Protocol   string    `json:"protocol,omitempty"`
	Enabled    bool      `json:"enabled"`
	Note       string    `json:"note,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// ClientKey 是调用方密钥。不出 JSON：读取面与日志都不该带上它。
	ClientKey string `json:"-"`
}
