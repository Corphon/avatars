package llm

import (
	"sync"
)

// PathRewriteHandler rewrites a tool write/edit path before it hits disk.
// Runtime uses this to enforce declared project layout (R7-2).
type PathRewriteHandler func(path string) string

// PathContentRewriteHandler is the content-aware sibling of PathRewriteHandler.
// Write tools pass the file body so public-API files can be lifted out of
// private helper directories (e.g. package jobpq under internal/concurrency/).
type PathContentRewriteHandler func(path, content string) string

var (
	pathRewriteMu             sync.Mutex
	pathRewriteHandler        PathRewriteHandler
	pathContentRewriteHandler PathContentRewriteHandler
)

// SetPathRewriteHandler registers (or replaces) the process-wide path rewrite hook.
func SetPathRewriteHandler(h PathRewriteHandler) {
	pathRewriteMu.Lock()
	defer pathRewriteMu.Unlock()
	pathRewriteHandler = h
}

// SetPathContentRewriteHandler registers (or replaces) the content-aware rewrite hook.
func SetPathContentRewriteHandler(h PathContentRewriteHandler) {
	pathRewriteMu.Lock()
	defer pathRewriteMu.Unlock()
	pathContentRewriteHandler = h
}

// ClearPathRewriteHandler removes the current path rewrite hooks.
func ClearPathRewriteHandler() {
	pathRewriteMu.Lock()
	defer pathRewriteMu.Unlock()
	pathRewriteHandler = nil
	pathContentRewriteHandler = nil
}

// RewriteToolPath applies the registered rewrite hook (identity when unset).
func RewriteToolPath(path string) string {
	return RewriteToolPathWithContent(path, "")
}

// RewriteToolPathWithContent prefers the content-aware hook when content is
// present, then falls back to the path-only hook.
func RewriteToolPathWithContent(path, content string) string {
	if path == "" {
		return path
	}
	pathRewriteMu.Lock()
	ch := pathContentRewriteHandler
	h := pathRewriteHandler
	pathRewriteMu.Unlock()
	if content != "" && ch != nil {
		if out := ch(path, content); out != "" {
			return out
		}
	}
	if h == nil {
		return path
	}
	if out := h(path); out != "" {
		return out
	}
	return path
}
