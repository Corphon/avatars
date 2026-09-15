package main

import (
	"strings"
	"sync"
)

// REPLSession holds the mutable REPL state that was previously stored in
// package-level globals (replSession variable). M1: Extracted to a named
// struct so it can be instantiated per session and passed through command
// handlers — enabling testing and concurrent REPL instances.
type REPLSession struct {
	mu           sync.Mutex
	fileCtx      *fileContextState
	replCtx      string
	lastFilePath string // persists after fileCtx is cleared for follow-up turns
}

// NewREPLSession creates a new REPLSession with empty state. M1.
func NewREPLSession() *REPLSession {
	return &REPLSession{}
}

func (s *REPLSession) SetFileContext(fc *fileContextState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileCtx = fc
	if fc != nil && strings.TrimSpace(fc.Path) != "" {
		s.lastFilePath = fc.Path
	}
}

func (s *REPLSession) GetLastAttachedFilePath() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastFilePath
}

func (s *REPLSession) GetFileContext() *fileContextState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fileCtx
}

func (s *REPLSession) SetContextForRun(ctx string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replCtx = ctx
}

func (s *REPLSession) GetContextForRun() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replCtx
}

// --- Backward-compatible package-level wrappers (bridge) ---

var defaultSession = NewREPLSession()

// Deprecated: Use REPLSession methods directly. These wrappers exist for
// backward compatibility during the migration. M1.
func setReplFileContext(fc *fileContextState) { defaultSession.SetFileContext(fc) }
func getLastAttachedFilePath() string         { return defaultSession.GetLastAttachedFilePath() }
func getReplFileContext() *fileContextState   { return defaultSession.GetFileContext() }
func setReplContextForRun(ctx string)          { defaultSession.SetContextForRun(ctx) }
func getReplContextForRun() string             { return defaultSession.GetContextForRun() }
