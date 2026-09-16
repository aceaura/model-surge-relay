package config

import (
	"strings"
	"testing"
	"time"
)

// setRequired 填齐必填项，便于各用例只关注自己要改的那一项。
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("MSR_PG_DSN", "postgres://localhost/msr")
	t.Setenv("MSR_DISPATCH_KEY", "dk")
	t.Setenv("MSR_ADMIN_KEY", "ak")
	t.Setenv("MSR_UPSTREAM_BASE_URL", "http://upstream:8080")
	t.Setenv("MSR_UPSTREAM_DELIVERY_KEY", "uk")
}

func TestLoadAppliesDefaults(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.CacheTTL != defaultCacheTTL {
		t.Errorf("cache ttl = %s, want %s", cfg.CacheTTL, defaultCacheTTL)
	}
	if cfg.PolicyTimeout != defaultPolicyTimeout {
		t.Errorf("policy timeout = %s, want %s", cfg.PolicyTimeout, defaultPolicyTimeout)
	}
	if cfg.CooldownThreshold != defaultCooldownThreshold {
		t.Errorf("threshold = %d, want %d", cfg.CooldownThreshold, defaultCooldownThreshold)
	}
	if cfg.CooldownDuration != defaultCooldownDuration {
		t.Errorf("cooldown = %s, want %s", cfg.CooldownDuration, defaultCooldownDuration)
	}
	if cfg.Listen != defaultListen {
		t.Errorf("listen = %s, want %s", cfg.Listen, defaultListen)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("MSR_CACHE_TTL", "30s")
	t.Setenv("MSR_POLICY_TIMEOUT", "500ms")
	t.Setenv("MSR_COOLDOWN_THRESHOLD", "5")
	t.Setenv("MSR_COOLDOWN_DURATION", "2m")
	t.Setenv("MSR_LISTEN", ":9090")
	t.Setenv("MSR_REDIS_ADDR", "redis:6379")
	t.Setenv("MSR_REDIS_DB", "3")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.CacheTTL != 30*time.Second || cfg.PolicyTimeout != 500*time.Millisecond ||
		cfg.CooldownThreshold != 5 || cfg.CooldownDuration != 2*time.Minute ||
		cfg.Listen != ":9090" || cfg.RedisDB != 3 {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestLoadFailsAndNamesEveryMissingRequirement(t *testing.T) {
	t.Setenv("MSR_PG_DSN", "postgres://localhost/msr")
	t.Setenv("MSR_UPSTREAM_BASE_URL", "http://upstream:8080")
	t.Setenv("MSR_UPSTREAM_DELIVERY_KEY", "uk")
	t.Setenv("MSR_DISPATCH_KEY", "")
	t.Setenv("MSR_ADMIN_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected startup failure")
	}
	for _, want := range []string{"MSR_DISPATCH_KEY", "MSR_ADMIN_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err, want)
		}
	}
}

func TestLoadRejectsBlankRequirement(t *testing.T) {
	setRequired(t)
	t.Setenv("MSR_ADMIN_KEY", "   ")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MSR_ADMIN_KEY") {
		t.Fatalf("error = %v, want a complaint about MSR_ADMIN_KEY", err)
	}
}

// TestLoadRejectsIdenticalKeys 相同密钥等于两面没有隔离，属于配置错误而非口味问题。
func TestLoadRejectsIdenticalKeys(t *testing.T) {
	setRequired(t)
	t.Setenv("MSR_ADMIN_KEY", "dk")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("error = %v, want the two keys to be rejected as identical", err)
	}
}

func TestLoadRejectsMalformedDuration(t *testing.T) {
	setRequired(t)
	t.Setenv("MSR_POLICY_TIMEOUT", "soon")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MSR_POLICY_TIMEOUT") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsNegativeValues(t *testing.T) {
	t.Run("duration", func(t *testing.T) {
		setRequired(t)
		t.Setenv("MSR_CACHE_TTL", "-5s")
		if _, err := Load(); err == nil {
			t.Fatal("expected rejection")
		}
	})
	t.Run("integer", func(t *testing.T) {
		setRequired(t)
		t.Setenv("MSR_COOLDOWN_THRESHOLD", "-1")
		if _, err := Load(); err == nil {
			t.Fatal("expected rejection")
		}
	})
}

func TestLoadRejectsMalformedInteger(t *testing.T) {
	setRequired(t)
	t.Setenv("MSR_COOLDOWN_THRESHOLD", "three")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MSR_COOLDOWN_THRESHOLD") {
		t.Fatalf("error = %v", err)
	}
}

func TestCacheDisabledWithoutRedisAddr(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.CacheEnabled() {
		t.Fatal("cache should be disabled when MSR_REDIS_ADDR is unset")
	}
}

func TestCacheEnabledWithRedisAddr(t *testing.T) {
	setRequired(t)
	t.Setenv("MSR_REDIS_ADDR", "127.0.0.1:6379")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.CacheEnabled() {
		t.Fatal("cache should be enabled")
	}
}
