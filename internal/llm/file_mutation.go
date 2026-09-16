package llm

import (
	"sync"
)

// FileMutationHandler is notified after a successful tool write/edit.
// Runtime uses this to feed Engine.changedFiles for end-of-run footers.
type FileMutationHandler func(path string)

var (
	fileMutationMu      sync.Mutex
	fileMutationHandler FileMutationHandler
)

// SetFileMutationHandler registers (or replaces) the process-wide mutation hook.
func SetFileMutationHandler(h FileMutationHandler) {
	fileMutationMu.Lock()
	defer fileMutationMu.Unlock()
	fileMutationHandler = h
}

// ClearFileMutationHandler removes the current mutation hook.
func ClearFileMutationHandler() {
	SetFileMutationHandler(nil)
}

// NotifyFileMutation reports a path that was successfully written by a tool.
func NotifyFileMutation(path string) {
	if path == "" {
		return
	}
	fileMutationMu.Lock()
	h := fileMutationHandler
	fileMutationMu.Unlock()
	if h != nil {
		h(path)
	}
}
