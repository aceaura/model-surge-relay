package policy

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/cache"
	"github.com/aceaura/model-surge-relay/backend/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool 打开测试库并清空本包关心的表；未配置 TEST_PG_DSN 则跳过。
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE user_models, policies, group_members, groups, collections RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s.Pool()
}

// okValidator 接受一切源码，用于与编译无关的 repo 行为测试。
type okValidator struct{ calls int }

func (v *okValidator) Validate(Language, string) error { v.calls++; return nil }

type failValidator struct{ err error }

func (v failValidator) Validate(Language, string) error { return v.err }

func newRepo(t *testing.T, v Validator) *Repo {
	return NewRepo(testPool(t), cache.New(nil, time.Minute), v)
}

func TestRepoCreateReturnsCompleteRecord(t *testing.T) {
	r := newRepo(t, &okValidator{})
	ctx := context.Background()
	got, err := r.Create(ctx, Policy{Name: "preset", Language: LangLua, Source: "return {}", Note: "first"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Name != "preset" || got.Language != LangLua || got.Source != "return {}" || got.Note != "first" {
		t.Fatalf("record = %+v", got)
	}
	if got.Version != 1 {
		t.Fatalf("version = %d, want 1", got.Version)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("timestamps missing: %+v", got)
	}
}

func TestRepoCreateRunsValidatorBeforeInsert(t *testing.T) {
	v := failValidator{err: apperr.Field(apperr.InvalidRequest, "source", "line 1:1: boom")}
	r := newRepo(t, v)
	ctx := context.Background()
	if _, err := r.Create(ctx, Policy{Name: "bad", Language: LangLua, Source: "@@@"}); err == nil {
		t.Fatal("expected compile failure to reject create")
	}
	if _, err := r.load(ctx, "bad"); !isNotFound(err) {
		t.Fatalf("rejected policy must not be persisted, got %v", err)
	}
}

func TestRepoCreateRejectsUnsupportedLanguage(t *testing.T) {
	engine := NewEngine(NewRegistry(&fakeRuntime{lang: LangLua}), time.Second)
	r := newRepo(t, engine)
	_, err := r.Create(context.Background(), Policy{Name: "py", Language: "python", Source: "pass"})
	e := asAppErr(t, err)
	if e.Code != apperr.InvalidRequest || e.Field != "language" {
		t.Fatalf("error = %+v, want invalid_request on language", e)
	}
}

func TestRepoCreateNormalizesLanguageCase(t *testing.T) {
	r := newRepo(t, &okValidator{})
	got, err := r.Create(context.Background(), Policy{Name: "p", Language: "LUA", Source: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Language != LangLua {
		t.Fatalf("language = %q, want lua", got.Language)
	}
}

func TestRepoCreateRejectsDuplicateName(t *testing.T) {
	r := newRepo(t, &okValidator{})
	ctx := context.Background()
	if _, err := r.Create(ctx, Policy{Name: "dup", Language: LangLua, Source: "a"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := r.Create(ctx, Policy{Name: "dup", Language: LangLua, Source: "b"})
	if e := asAppErr(t, err); e.Code != apperr.Conflict {
		t.Fatalf("code = %s, want conflict", e.Code)
	}
}

func TestRepoCreateRequiresName(t *testing.T) {
	r := newRepo(t, &okValidator{})
	_, err := r.Create(context.Background(), Policy{Name: "  ", Language: LangLua, Source: "a"})
	if e := asAppErr(t, err); e.Code != apperr.InvalidRequest || e.Field != "name" {
		t.Fatalf("error = %+v", e)
	}
}

func TestRepoUpdateBumpsVersionOnSourceChange(t *testing.T) {
	r := newRepo(t, &okValidator{})
	ctx := context.Background()
	if _, err := r.Create(ctx, Policy{Name: "p", Language: LangLua, Source: "v1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := r.Update(ctx, Policy{Name: "p", Language: LangLua, Source: "v2"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Version != 2 {
		t.Fatalf("version = %d, want 2", got.Version)
	}
	if got.Source != "v2" {
		t.Fatalf("source = %q, want v2", got.Source)
	}
}

func TestRepoUpdateKeepsVersionWhenOnlyNoteChanges(t *testing.T) {
	r := newRepo(t, &okValidator{})
	ctx := context.Background()
	if _, err := r.Create(ctx, Policy{Name: "p", Language: LangLua, Source: "same"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := r.Update(ctx, Policy{Name: "p", Language: LangLua, Source: "same", Note: "edited"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Version != 1 {
		t.Fatalf("version = %d, want 1 (note-only edit must not invalidate compile cache)", got.Version)
	}
	if got.Note != "edited" {
		t.Fatalf("note = %q", got.Note)
	}
}

func TestRepoUpdateBumpsVersionOnLanguageChange(t *testing.T) {
	r := newRepo(t, &okValidator{})
	ctx := context.Background()
	if _, err := r.Create(ctx, Policy{Name: "p", Language: LangLua, Source: "s"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := r.Update(ctx, Policy{Name: "p", Language: LangJavaScript, Source: "s"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Version != 2 {
		t.Fatalf("version = %d, want 2", got.Version)
	}
}

func TestRepoUpdateRejectsUncompilableSource(t *testing.T) {
	pool := testPool(t)
	ok := &okValidator{}
	r := NewRepo(pool, cache.New(nil, time.Minute), ok)
	ctx := context.Background()
	if _, err := r.Create(ctx, Policy{Name: "p", Language: LangLua, Source: "good"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	strict := NewRepo(pool, cache.New(nil, time.Minute),
		failValidator{err: apperr.Field(apperr.InvalidRequest, "source", "broken")})
	if _, err := strict.Update(ctx, Policy{Name: "p", Language: LangLua, Source: "bad"}); err == nil {
		t.Fatal("expected update rejection")
	}
	after, err := r.load(ctx, "p")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if after.Source != "good" || after.Version != 1 {
		t.Fatalf("record mutated by rejected update: %+v", after)
	}
}

func TestRepoUpdateMissingPolicyIsNotFound(t *testing.T) {
	r := newRepo(t, &okValidator{})
	_, err := r.Update(context.Background(), Policy{Name: "ghost", Language: LangLua, Source: "s"})
	if e := asAppErr(t, err); e.Code != apperr.NotFound {
		t.Fatalf("code = %s, want not_found", e.Code)
	}
}

func TestRepoGetAndList(t *testing.T) {
	r := newRepo(t, &okValidator{})
	ctx := context.Background()
	for _, name := range []string{"b", "a"} {
		if _, err := r.Create(ctx, Policy{Name: name, Language: LangLua, Source: name}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	got, err := r.Get(ctx, "a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Source != "a" {
		t.Fatalf("source = %q", got.Source)
	}
	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].Name != "a" || list[1].Name != "b" {
		t.Fatalf("list = %+v, want name order", list)
	}
}

func TestRepoGetMissingIsNotFound(t *testing.T) {
	r := newRepo(t, &okValidator{})
	_, err := r.Get(context.Background(), "ghost")
	if e := asAppErr(t, err); e.Code != apperr.NotFound {
		t.Fatalf("code = %s, want not_found", e.Code)
	}
}

func TestRepoDeleteBlockedByBoundUserModels(t *testing.T) {
	pool := testPool(t)
	r := NewRepo(pool, cache.New(nil, time.Minute), &okValidator{})
	ctx := context.Background()
	if _, err := r.Create(ctx, Policy{Name: "shared", Language: LangLua, Source: "s"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO collections (name) VALUES ('c1')`); err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	for _, name := range []string{"um-b", "um-a"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO user_models (name, collection, policy, client_key) VALUES ($1, 'c1', 'shared', 'k')`,
			name); err != nil {
			t.Fatalf("seed user model: %v", err)
		}
	}

	err := r.Delete(ctx, "shared")
	e := asAppErr(t, err)
	if e.Code != apperr.Conflict {
		t.Fatalf("code = %s, want conflict", e.Code)
	}
	if !strings.Contains(e.Message, "um-a") || !strings.Contains(e.Message, "um-b") {
		t.Fatalf("message %q should list every referencing user model", e.Message)
	}
	if _, err := r.load(ctx, "shared"); err != nil {
		t.Fatalf("policy must survive a blocked delete: %v", err)
	}
}

func TestRepoDeleteUnreferencedPolicy(t *testing.T) {
	r := newRepo(t, &okValidator{})
	ctx := context.Background()
	if _, err := r.Create(ctx, Policy{Name: "solo", Language: LangLua, Source: "s"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := r.Delete(ctx, "solo"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.load(ctx, "solo"); !isNotFound(err) {
		t.Fatalf("policy should be gone, got %v", err)
	}
}

func TestRepoDeleteMissingIsNotFound(t *testing.T) {
	r := newRepo(t, &okValidator{})
	if e := asAppErr(t, r.Delete(context.Background(), "ghost")); e.Code != apperr.NotFound {
		t.Fatalf("code = %s, want not_found", e.Code)
	}
}

// TestRepoUpdateInvalidatesCachedCopy 断言更新后读到的是新版本，
// 而不是缓存里的旧副本。
func TestRepoUpdateInvalidatesCachedCopy(t *testing.T) {
	backend := newMemBackend()
	r := NewRepo(testPool(t), cache.New(backend, time.Hour), &okValidator{})
	ctx := context.Background()
	if _, err := r.Create(ctx, Policy{Name: "p", Language: LangLua, Source: "v1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.Get(ctx, "p"); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	if _, err := r.Update(ctx, Policy{Name: "p", Language: LangLua, Source: "v2"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := r.Get(ctx, "p")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Source != "v2" || got.Version != 2 {
		t.Fatalf("stale cached copy served: %+v", got)
	}
}

type memBackend struct{ data map[string][]byte }

func newMemBackend() *memBackend { return &memBackend{data: map[string][]byte{}} }

func (b *memBackend) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := b.data[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	return v, nil
}

func (b *memBackend) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	b.data[key] = value
	return nil
}

func (b *memBackend) Del(_ context.Context, key string) error {
	delete(b.data, key)
	return nil
}

func (b *memBackend) Ready(context.Context) bool { return true }

func isNotFound(err error) bool {
	var e *apperr.Error
	return errors.As(err, &e) && e.Code == apperr.NotFound
}
