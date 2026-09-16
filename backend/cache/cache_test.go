package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type row struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

// stub 是可注入失败的内存 backend。
type stub struct {
	mu       sync.Mutex
	data     map[string][]byte
	failGet  bool
	failSet  bool
	failDel  bool
	notReady bool
	gets     int
	sets     int
	dels     int
}

func newStub() *stub { return &stub{data: map[string][]byte{}} }

func (s *stub) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if s.failGet {
		return nil, errors.New("redis down")
	}
	raw, ok := s.data[key]
	if !ok {
		return nil, errors.New("nil")
	}
	return raw, nil
}

func (s *stub) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets++
	if s.failSet {
		return errors.New("redis down")
	}
	s.data[key] = value
	return nil
}

func (s *stub) Del(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dels++
	if s.failDel {
		return errors.New("redis down")
	}
	delete(s.data, key)
	return nil
}

func (s *stub) Ready(context.Context) bool { return !s.notReady }

func (s *stub) counts() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.sets, s.dels
}

func loader(v row, calls *int) func() (row, error) {
	return func() (row, error) {
		*calls++
		return v, nil
	}
}

func TestReadThroughMissThenHit(t *testing.T) {
	ctx := context.Background()
	c := New(newStub(), time.Minute)
	want := row{Name: "a", Value: 1}
	calls := 0

	got, err := ReadThrough(ctx, c, "k", loader(want, &calls))
	if err != nil || got != want {
		t.Fatalf("first read = %+v, %v", got, err)
	}
	if calls != 1 {
		t.Fatalf("loader calls = %d, want 1", calls)
	}

	got, err = ReadThrough(ctx, c, "k", loader(want, &calls))
	if err != nil || got != want {
		t.Fatalf("second read = %+v, %v", got, err)
	}
	if calls != 1 {
		t.Errorf("loader calls = %d, want 1 (second read must hit cache)", calls)
	}
}

func TestReadThroughSurvivesGetFailure(t *testing.T) {
	ctx := context.Background()
	s := newStub()
	s.failGet = true
	c := New(s, time.Minute)
	want := row{Name: "a", Value: 1}
	calls := 0

	for i := 0; i < 2; i++ {
		got, err := ReadThrough(ctx, c, "k", loader(want, &calls))
		if err != nil {
			t.Fatalf("read %d must not fail when cache get fails: %v", i, err)
		}
		if got != want {
			t.Fatalf("read %d = %+v, want %+v", i, got, want)
		}
	}
	if calls != 2 {
		t.Errorf("loader calls = %d, want 2 (get failure counts as miss)", calls)
	}
}

func TestSetFailureIsConvertedToInvalidate(t *testing.T) {
	ctx := context.Background()
	s := newStub()
	s.failSet = true
	c := New(s, time.Minute)
	calls := 0

	if _, err := ReadThrough(ctx, c, "k", loader(row{Name: "a"}, &calls)); err != nil {
		t.Fatalf("read must not fail when cache set fails: %v", err)
	}
	if _, _, dels := s.counts(); dels == 0 {
		t.Error("failed set must trigger a delete so the next read misses")
	}
}

func TestInvalidateSwallowsDeleteFailure(t *testing.T) {
	ctx := context.Background()
	s := newStub()
	s.failDel = true
	c := New(s, time.Minute)
	c.Invalidate(ctx, "k") // 不得 panic 也不得阻塞
	if _, _, dels := s.counts(); dels != 1 {
		t.Errorf("dels = %d, want 1", dels)
	}
}

func TestInvalidateRemovesKey(t *testing.T) {
	ctx := context.Background()
	c := New(newStub(), time.Minute)
	calls := 0
	want := row{Name: "a", Value: 1}

	if _, err := ReadThrough(ctx, c, "k", loader(want, &calls)); err != nil {
		t.Fatal(err)
	}
	c.Invalidate(ctx, "k")
	if _, err := ReadThrough(ctx, c, "k", loader(want, &calls)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("loader calls = %d, want 2 after invalidate", calls)
	}
}

func TestNilBackendReadsThroughEveryTime(t *testing.T) {
	ctx := context.Background()
	c := New(nil, time.Minute)
	calls := 0
	want := row{Name: "a", Value: 1}

	for i := 0; i < 3; i++ {
		got, err := ReadThrough(ctx, c, "k", loader(want, &calls))
		if err != nil || got != want {
			t.Fatalf("read %d = %+v, %v", i, got, err)
		}
	}
	if calls != 3 {
		t.Errorf("loader calls = %d, want 3 without a cache backend", calls)
	}
	if c.Ready(ctx) {
		t.Error("Ready must be false without a backend")
	}
}

func TestLoaderErrorIsPropagated(t *testing.T) {
	ctx := context.Background()
	c := New(newStub(), time.Minute)
	sentinel := errors.New("db down")

	_, err := ReadThrough(ctx, c, "k", func() (row, error) { return row{}, sentinel })
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
}

func TestCorruptCachedValueFallsBackToLoader(t *testing.T) {
	ctx := context.Background()
	s := newStub()
	s.data["k"] = []byte("{not json")
	c := New(s, time.Minute)
	calls := 0
	want := row{Name: "a", Value: 2}

	got, err := ReadThrough(ctx, c, "k", loader(want, &calls))
	if err != nil || got != want {
		t.Fatalf("read = %+v, %v", got, err)
	}
	if calls != 1 {
		t.Errorf("loader calls = %d, want 1", calls)
	}
}

func TestReadyReflectsBackend(t *testing.T) {
	ctx := context.Background()
	s := newStub()
	c := New(s, time.Minute)
	if !c.Ready(ctx) {
		t.Error("Ready must be true for a healthy backend")
	}
	s.notReady = true
	if c.Ready(ctx) {
		t.Error("Ready must be false for an unhealthy backend")
	}
}

func TestKeyspaceIsNamespaced(t *testing.T) {
	cases := []string{CollectionKey("c"), UserModelKey("u"), PolicyKey("p"), CatalogKey()}
	seen := map[string]bool{}
	for _, k := range cases {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			t.Errorf("key %q must carry prefix %q", k, prefix)
		}
		if seen[k] {
			t.Errorf("key %q collides", k)
		}
		seen[k] = true
	}
}
