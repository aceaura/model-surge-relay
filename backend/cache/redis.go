package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisBackend struct {
	client *redis.Client
}

func NewRedisBackend(addr, password string, db int) *RedisBackend {
	return &RedisBackend{client: redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})}
}

func (b *RedisBackend) Get(ctx context.Context, key string) ([]byte, error) {
	return b.client.Get(ctx, key).Bytes()
}

func (b *RedisBackend) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return b.client.Set(ctx, key, value, ttl).Err()
}

func (b *RedisBackend) Del(ctx context.Context, key string) error {
	return b.client.Del(ctx, key).Err()
}

func (b *RedisBackend) Ready(ctx context.Context) bool {
	return b.client.Ping(ctx).Err() == nil
}

func (b *RedisBackend) Close() error { return b.client.Close() }
