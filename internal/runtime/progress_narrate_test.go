package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/events"
)

func TestNarrateRunProgress_ToolsAndLifecycle(t *testing.T) {
	cases := []struct {
		name string
		env  events.Envelope
		want string
	}{
		{
			name: "write remapped_from ignored",
			env: events.Envelope{Type: "tool.requested", Payload: map[string]any{
				"tool": "write", "path": "worker.go", "remapped_from": "internal/worker/worker.go",
			}},
			want: "✍ writing · worker.go",
		},
		{
			name: "shell",
			env:  events.Envelope{Type: "tool.requested", Payload: map[string]any{"tool": "shell", "command": "go test ./..."}},
			want: "$ go test ./...",
		},
		{
			name: "node",
			env: events.Envelope{Type: "workflow.node_activated", Payload: map[string]any{
				"assigned_role": "Builder", "title": "Implement Phase 1",
			}},
			want: "▸ Builder · Implement Phase 1",
		},
		{
			name: "blocked",
			env:  events.Envelope{Type: "workflow.phase_advance_blocked", Payload: map[string]any{"reason": "build_failed"}},
			want: "⛔ Phase advance blocked · build_failed",
		},
		{
			name: "impl_started",
			env:  events.Envelope{Type: "builder.code_implementation_started"},
			want: "✍ Builder implementing…",
		},
		{
			name: "path_sanitized",
			env:  events.Envelope{Type: "builder.path_sanitized", Payload: map[string]any{"kept": 3}},
			want: "🛡 sanitized paths · kept 3",
		},
		{
			name: "thinking preview",
			env: events.Envelope{Type: "llm.thinking", Payload: map[string]any{
				"chars":   80,
				"preview": "this looks like a worker pool, I'll write worker.go next",
			}},
			want: "💭 this looks like a worker pool, I'll write worker.go next (80 chars)",
		},
		{
			name: "streaming bytes",
			env: events.Envelope{Type: "llm.streaming", Payload: map[string]any{
				"bytes":   4096,
				"message": "receiving… (4096 bytes)",
			}},
			want: "⏳ receiving… (4096 bytes)",
		},
	}
	for _, tc := range cases {
		got := NarrateRunProgress(tc.env)
		if got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestLivePhasePulseLine(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(`> **Active Phase**: 1
> **Phase Count**: 2
## Phases
### Phase 1: Core
- **Goal**: g
`), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(`> **Phase**: 1 of 2
## Phase 1 Checklist
- [x] a
- [ ] b
`), 0644)
	got := LivePhasePulseLine(root)
	if !strings.Contains(got, "Phase 1/2") || !strings.Contains(got, "todo 1/2") {
		t.Fatalf("pulse=%q", got)
	}
	if !ShouldPulsePhase("workflow.node_activated") {
		t.Fatal("expected pulse on node_activated")
	}
}

func TestThinkingPreviewTail_UsesCollapsedTail(t *testing.T) {
	got := thinkingPreviewTail("alpha\nbeta   gamma", 20)
	if got != "alpha beta gamma" {
		t.Fatalf("got %q", got)
	}
	got = thinkingPreviewTail("one two three four five", 9)
	if got != "…four five" {
		t.Fatalf("tail got %q", got)
	}
}
