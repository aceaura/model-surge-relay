// Package cache 把 Redis 当作 PostgreSQL 的只读投影。
//
// 关键取舍：Redis 故障从不向上层产出错误。读失败当 miss，写失败转为失效，
// 让下次读取走回填路径；TTL 是最终兜底。这样缓存层的可用性不会传导成请求失败。
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrMiss 表示键不存在或后端不可用，两者对调用方等价。
var ErrMiss = errors.New("cache miss")

// Backend 抽象出 Redis 的四个操作，便于注入失败做测试。
type Backend interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Del(ctx context.Context, key string) error
	Ready(ctx context.Context) bool
}

type Cache struct {
	backend Backend
	ttl     time.Duration
}

// New 接受 nil backend，表示未配置 Redis：全程直读 PG。
func New(backend Backend, ttl time.Duration) *Cache {
	return &Cache{backend: backend, ttl: ttl}
}

func (c *Cache) enabled() bool { return c != nil && c.backend != nil }

// Ready 报告缓存后端是否可用，仅供健康检查展示，不影响服务就绪判定。
func (c *Cache) Ready(ctx context.Context) bool {
	if !c.enabled() {
		return false
	}
	return c.backend.Ready(ctx)
}

func (c *Cache) get(ctx context.Context, key string) ([]byte, error) {
	if !c.enabled() {
		return nil, ErrMiss
	}
	raw, err := c.backend.Get(ctx, key)
	if err != nil || len(raw) == 0 {
		return nil, ErrMiss
	}
	return raw, nil
}

func (c *Cache) set(ctx context.Context, key string, raw []byte) {
	if !c.enabled() {
		return
	}
	// 写失败转为失效：宁可下次 miss，也不留下可能过期的副本。
	if err := c.backend.Set(ctx, key, raw, c.ttl); err != nil {
		_ = c.backend.Del(ctx, key)
	}
}

// Invalidate 删除键。删除本身失败时不报错——TTL 会兜住。
func (c *Cache) Invalidate(ctx context.Context, keys ...string) {
	if !c.enabled() {
		return
	}
	for _, key := range keys {
		_ = c.backend.Del(ctx, key)
	}
}

// ReadThrough 先读缓存；miss 则 load、回填并返回 load 结果。
// 缓存抖动只影响命中率，不影响正确性与可用性。
func ReadThrough[T any](ctx context.Context, c *Cache, key string, load func() (T, error)) (T, error) {
	var zero T
	if raw, err := c.get(ctx, key); err == nil {
		var v T
		if json.Unmarshal(raw, &v) == nil {
			return v, nil
		}
		// 反序列化失败说明副本已不可信（如结构变更），删掉后走回源。
		c.Invalidate(ctx, key)
	}
	v, err := load()
	if err != nil {
		return zero, err
	}
	if raw, mErr := json.Marshal(v); mErr == nil {
		c.set(ctx, key, raw)
	}
	return v, nil
}
