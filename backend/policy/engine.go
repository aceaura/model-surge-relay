package policy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
)

// DefaultTimeout 是单次策略执行的墙钟上限。
const DefaultTimeout = 200 * time.Millisecond

// Engine 持有编译缓存并统一错误映射。缓存键是策略名加版本，
// 因此更新源码时版本递增即自然失效，无需显式清理。
type Engine struct {
	registry *Registry
	timeout  time.Duration

	mu       sync.RWMutex
	programs map[string]Program
}

func NewEngine(registry *Registry, timeout time.Duration) *Engine {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Engine{registry: registry, timeout: timeout, programs: map[string]Program{}}
}

// Validate 在保存前校验语言受支持且源码可编译。编译产物随即丢弃：
// 保存后的版本号才是缓存键，这里预热会缓存到错误的键上。
func (e *Engine) Validate(lang Language, source string) error {
	_, err := e.compile(lang, source)
	return err
}

// Execute 执行策略一次。编译错误、运行时错误与超时都映射为带
// 可重试标志的 apperr，调用方无需再判别。
func (e *Engine) Execute(ctx context.Context, p Policy, in Input) (Decision, error) {
	program, err := e.program(p)
	if err != nil {
		return Decision{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	d, err := program.Execute(ctx, in)
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return Decision{}, apperr.New(apperr.PolicyTimeout,
				fmt.Sprintf("policy %q version %d timed out after %s", p.Name, p.Version, e.timeout))
		}
		return Decision{}, apperr.New(apperr.PolicyError,
			fmt.Sprintf("policy %q version %d failed: %s", p.Name, p.Version, err))
	}
	if d.Candidates == nil {
		d.Candidates = []string{}
	}
	return d, nil
}

func (e *Engine) program(p Policy) (Program, error) {
	key := cacheKey(p)
	e.mu.RLock()
	program, ok := e.programs[key]
	e.mu.RUnlock()
	if ok {
		return program, nil
	}
	program, err := e.compile(p.Language, p.Source)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// 并发首次执行可能各自编译一份，保留先落地的那份让所有调用方共享。
	if existing, ok := e.programs[key]; ok {
		return existing, nil
	}
	e.evictOtherVersionsLocked(p.Name, key)
	e.programs[key] = program
	return program, nil
}

func (e *Engine) evictOtherVersionsLocked(name, keep string) {
	prefix := name + "@"
	for k := range e.programs {
		if k != keep && strings.HasPrefix(k, prefix) {
			delete(e.programs, k)
		}
	}
}

func (e *Engine) compile(lang Language, source string) (Program, error) {
	rt, ok := e.registry.Get(lang)
	if !ok {
		return nil, apperr.Field(apperr.InvalidRequest, "language",
			fmt.Sprintf("unsupported policy language %q, supported: %s",
				lang, strings.Join(e.registry.Supported(), ", ")))
	}
	program, err := rt.Compile(source)
	if err != nil {
		var ce *CompileError
		if errors.As(err, &ce) {
			return nil, apperr.Field(apperr.InvalidRequest, "source", ce.Error())
		}
		return nil, apperr.Field(apperr.InvalidRequest, "source", err.Error())
	}
	return program, nil
}

func cacheKey(p Policy) string { return fmt.Sprintf("%s@%d", p.Name, p.Version) }
