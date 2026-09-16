package store

import (
	"context"
	"os"
	"testing"
)

// dsn 返回测试库 DSN；未配置则跳过（与 upstream 项目同惯例）。
func dsn(t *testing.T) string {
	t.Helper()
	v := os.Getenv("TEST_PG_DSN")
	if v == "" {
		t.Skip("TEST_PG_DSN not set")
	}
	return v
}

func TestOpenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	first, err := Open(ctx, dsn(t))
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	first.Close()

	second, err := Open(ctx, dsn(t))
	if err != nil {
		t.Fatalf("second open must not fail on existing tables: %v", err)
	}
	defer second.Close()

	if err := second.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestSchemaCreatesAllTables(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, dsn(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	want := []string{
		"collections", "groups", "group_members", "policies",
		"user_models", "target_runtime", "result_reports",
	}
	for _, table := range want {
		var exists bool
		err := s.Pool().QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			 WHERE table_schema = current_schema() AND table_name = $1)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("probe %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s missing", table)
		}
	}
}
