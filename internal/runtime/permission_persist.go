// Package runtime — permission persistence and feedback capture.
//
// P9: Adopts Claude Code's interactive permission pattern
// (claude_code_main/hooks/toolPermission/handlers/interactiveHandler.ts)
// and feedback→memory loop (claude_code_main/services/extractMemories/).
//
// Key additions:
//   1. PermissionRule persistence — user's "Always allow" choices survive sessions
//   2. RejectionFeedback — user's rejection reasons are captured and injected
//      into the next Planner/Critic run as learning signals
//   3. FeedbackLog — a lightweight record of all tool rejections for post-hoc
//      analysis and memory extraction

package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PermissionRule is a single persistent permission rule, mirroring
// Claude Code's PermissionRule type.
type PermissionRule struct {
	Tool      string `json:"tool"`
	Behavior  string `json:"behavior"` // "allow" or "deny"
	CreatedAt string `json:"created_at"`
	Reason    string `json:"reason,omitempty"`
}

// PermissionRules stores persistent tool permission rules loaded from disk.
type PermissionRules struct {
	mu    sync.RWMutex
	rules map[string]PermissionRule // key: tool name
	path  string                     // disk path
}

// RejectionFeedback captures the user's reason for rejecting a tool call.
// Mirrors Claude Code's PermissionAllowDecision.acceptFeedback field.
type RejectionFeedback struct {
	Tool      string    `json:"tool"`
	Operation string    `json:"operation"`
	Reason    string    `json:"reason"`
	FilePath  string    `json:"file_path,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// FeedbackLog is a lightweight append-only log of all tool rejections.
type FeedbackLog struct {
	mu       sync.Mutex
	path     string
	entries  []RejectionFeedback
}

// LoadPermissionRules reads persistent permission rules from .avatars/settings.json.
func LoadPermissionRules(projectRoot string) (*PermissionRules, error) {
	pr := &PermissionRules{
		rules: make(map[string]PermissionRule),
		path:  filepath.Join(projectRoot, ".avatars", "settings.json"),
	}
	data, err := os.ReadFile(pr.path)
	if err != nil {
		if os.IsNotExist(err) {
			return pr, nil // no saved rules yet
		}
		return nil, fmt.Errorf("permission persist: read settings: %w", err)
	}
	var settings struct {
		PermissionRules []PermissionRule `json:"permission_rules,omitempty"`
		UpdatedAt       string           `json:"updated_at,omitempty"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("permission persist: parse settings: %w", err)
	}
	for _, rule := range settings.PermissionRules {
		pr.rules[strings.ToLower(strings.TrimSpace(rule.Tool))] = rule
	}
	return pr, nil
}

// Get returns the persistent rule for a tool, if one exists.
func (pr *PermissionRules) Get(toolName string) (PermissionRule, bool) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	rule, ok := pr.rules[strings.ToLower(strings.TrimSpace(toolName))]
	return rule, ok
}

// Set persists a permission rule and saves to disk immediately.
func (pr *PermissionRules) Set(toolName string, behavior string, reason string) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	rule := PermissionRule{
		Tool:      strings.ToLower(strings.TrimSpace(toolName)),
		Behavior:  behavior,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Reason:    reason,
	}
	pr.rules[rule.Tool] = rule
	return pr.flush()
}

// Remove deletes a persistent rule for a tool.
func (pr *PermissionRules) Remove(toolName string) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	delete(pr.rules, strings.ToLower(strings.TrimSpace(toolName)))
	return pr.flush()
}

// flush writes all rules to disk.
func (pr *PermissionRules) flush() error {
	dir := filepath.Dir(pr.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	rules := make([]PermissionRule, 0, len(pr.rules))
	for _, r := range pr.rules {
		rules = append(rules, r)
	}
	settings := struct {
		PermissionRules []PermissionRule `json:"permission_rules"`
		UpdatedAt       string           `json:"updated_at"`
	}{
		PermissionRules: rules,
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(pr.path, data, 0644)
}

// NewFeedbackLog creates or opens a feedback log for the project.
func NewFeedbackLog(projectRoot string) *FeedbackLog {
	return &FeedbackLog{
		path: filepath.Join(projectRoot, ".avatars", "feedback.jsonl"),
	}
}

// Record stores a rejection feedback entry.
func (fl *FeedbackLog) Record(fb RejectionFeedback) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	fl.entries = append(fl.entries, fb)

	// Append to JSONL file.
	dir := filepath.Dir(fl.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(fb)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(fl.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

// RecentFeedback returns feedback from the last N entries.
func (fl *FeedbackLog) RecentFeedback(n int) []RejectionFeedback {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if len(fl.entries) <= n {
		out := make([]RejectionFeedback, len(fl.entries))
		copy(out, fl.entries)
		return out
	}
	start := len(fl.entries) - n
	out := make([]RejectionFeedback, n)
	copy(out, fl.entries[start:])
	return out
}

// CaptureRejectionFeedback records a tool denial as rejection feedback
// and persists it to the FeedbackLog. This is called from the tool denial
// path so that every rejection becomes a learning signal for the next run.
//
// P9: User-in-the-Loop feedback capture — last piece of the loop.
func CaptureRejectionFeedback(projectRoot, toolName, operation, filePath, reason string) {
	if projectRoot == "" {
		return
	}
	fl := NewFeedbackLog(projectRoot)
	_ = fl.Record(RejectionFeedback{
		Tool:      toolName,
		Operation: operation,
		Reason:    reason,
		FilePath:  filePath,
		Timestamp: time.Now().UTC(),
	})
}

// FormatFeedbackForPlanner formats recent rejection feedback as constraints
// for the next Planner run. This prevents the LLM from repeating the same
// mistakes that caused the previous rejection.
func FormatFeedbackForPlanner(feedback []RejectionFeedback) string {
	if len(feedback) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## User Feedback from Previous Runs\n")
	b.WriteString("The user rejected the following tool calls in previous runs. ")
	b.WriteString("Learn from these rejections. Do NOT repeat the same patterns:\n\n")
	for _, fb := range feedback {
		fmt.Fprintf(&b, "- **%s/%s** was rejected", fb.Tool, fb.Operation)
		if fb.FilePath != "" {
			fmt.Fprintf(&b, " on %s", fb.FilePath)
		}
		if fb.Reason != "" {
			fmt.Fprintf(&b, " — reason: %s", fb.Reason)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nBefore generating code that matches any rejected pattern above, ")
	b.WriteString("ask for clarification or choose a different approach.\n")
	return b.String()
}
