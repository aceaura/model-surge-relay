// Package config 从环境变量装载运行参数。
//
// 必填项缺失即启动失败并列出全部缺失名称：一次报全比逐个试错省一轮部署。
package config

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	PGDSN               string
	RedisAddr           string
	RedisPassword       string
	RedisDB             int
	CacheTTL            time.Duration
	DispatchKey         string
	AdminKey            string
	UpstreamBaseURL     string
	UpstreamDeliveryKey string
	PolicyTimeout       time.Duration
	CooldownThreshold   int
	CooldownDuration    time.Duration
	Listen              string
}

const (
	defaultCacheTTL          = time.Minute
	defaultPolicyTimeout     = 200 * time.Millisecond
	defaultCooldownThreshold = 3
	defaultCooldownDuration  = time.Minute
	defaultListen            = ":8080"
)

func Load() (Config, error) {
	cfg := Config{
		PGDSN:               os.Getenv("MSR_PG_DSN"),
		RedisAddr:           os.Getenv("MSR_REDIS_ADDR"),
		RedisPassword:       os.Getenv("MSR_REDIS_PASSWORD"),
		DispatchKey:         os.Getenv("MSR_DISPATCH_KEY"),
		AdminKey:            os.Getenv("MSR_ADMIN_KEY"),
		UpstreamBaseURL:     os.Getenv("MSR_UPSTREAM_BASE_URL"),
		UpstreamDeliveryKey: os.Getenv("MSR_UPSTREAM_DELIVERY_KEY"),
		Listen:              valueOr("MSR_LISTEN", defaultListen),
	}

	var missing []string
	for name, value := range map[string]string{
		"MSR_PG_DSN":                cfg.PGDSN,
		"MSR_DISPATCH_KEY":          cfg.DispatchKey,
		"MSR_ADMIN_KEY":             cfg.AdminKey,
		"MSR_UPSTREAM_BASE_URL":     cfg.UpstreamBaseURL,
		"MSR_UPSTREAM_DELIVERY_KEY": cfg.UpstreamDeliveryKey,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Config{}, fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	if cfg.DispatchKey == cfg.AdminKey {
		return Config{}, fmt.Errorf("MSR_DISPATCH_KEY and MSR_ADMIN_KEY must differ, otherwise the two planes are not isolated")
	}

	var err error
	if cfg.CacheTTL, err = duration("MSR_CACHE_TTL", defaultCacheTTL); err != nil {
		return Config{}, err
	}
	if cfg.PolicyTimeout, err = duration("MSR_POLICY_TIMEOUT", defaultPolicyTimeout); err != nil {
		return Config{}, err
	}
	if cfg.CooldownDuration, err = duration("MSR_COOLDOWN_DURATION", defaultCooldownDuration); err != nil {
		return Config{}, err
	}
	if cfg.CooldownThreshold, err = integer("MSR_COOLDOWN_THRESHOLD", defaultCooldownThreshold); err != nil {
		return Config{}, err
	}
	if cfg.RedisDB, err = integer("MSR_REDIS_DB", 0); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// CacheEnabled 报告是否配置了 Redis。未配置时缓存层以 nil backend 构建，全程直读 PG。
func (c Config) CacheEnabled() bool { return c.RedisAddr != "" }

func valueOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func duration(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration like 200ms or 60s: %w", name, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s must not be negative", name)
	}
	return d, nil
}

func integer(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must not be negative", name)
	}
	return n, nil
}
