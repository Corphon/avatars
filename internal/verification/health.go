package verification

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
)

// HealthService memoizes go build / go test (and other exec fingerprints)
// for the duration of one Engine run so Builder, Critic, footer, and
// verification.Runner do not spawn duplicate processes (F48 / B5).
type HealthService struct {
	mu    sync.Mutex
	memo  map[string]string
	execs map[string]cachedExec
}

type cachedExec struct {
	output string
	err    error
}

var (
	currentMu     sync.Mutex
	currentHealth *HealthService
)

func NewHealthService() *HealthService {
	return &HealthService{
		memo:  map[string]string{},
		execs: map[string]cachedExec{},
	}
}

func (h *HealthService) Reset() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.memo = map[string]string{}
	h.execs = map[string]cachedExec{}
}

func SetCurrentHealth(h *HealthService) {
	currentMu.Lock()
	currentHealth = h
	currentMu.Unlock()
}

func CurrentHealth() *HealthService {
	currentMu.Lock()
	defer currentMu.Unlock()
	return currentHealth
}

func healthFingerprint(wd string, parts ...string) string {
	return filepath.Clean(wd) + "\x00" + strings.Join(parts, "\x00")
}

func (h *HealthService) Memo(wd, kind string, fn func() string) string {
	if h == nil {
		return fn()
	}
	key := healthFingerprint(wd, kind)
	h.mu.Lock()
	if v, ok := h.memo[key]; ok {
		h.mu.Unlock()
		return v
	}
	h.mu.Unlock()
	v := fn()
	h.mu.Lock()
	h.memo[key] = v
	h.mu.Unlock()
	return v
}

func (h *HealthService) Exec(ctx context.Context, exec Executor, wd string, cmd []string) (string, error) {
	if exec == nil {
		return "", nil
	}
	if h == nil {
		return exec.Run(ctx, wd, cmd)
	}
	key := healthFingerprint(wd, cmd...)
	h.mu.Lock()
	if hit, ok := h.execs[key]; ok {
		h.mu.Unlock()
		return hit.output, hit.err
	}
	h.mu.Unlock()
	out, err := exec.Run(ctx, wd, cmd)
	h.mu.Lock()
	h.execs[key] = cachedExec{output: out, err: err}
	h.mu.Unlock()
	return out, err
}
