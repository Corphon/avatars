package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRequestedPhase(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"只做 Phase 1：按 phase1.md 搭脚手架", 1},
		{"确认计划并立刻按 Phase 1 实现", 1},
		{"implement phase 2 auth", 2},
		{"阶段 3 完成路由", 3},
		{"随便写点代码", 0},
		// Full course with Phase titles must NOT lock/target Phase 1 from first match.
		{"Phase 1 脚手架；Phase 2 鉴；Phase 3 CRUD。三段都跑通，允许升相。", 0},
		{"第一段脚手架，第二段鉴权，第三段 CRUD，做到尾。", 0},
	}
	for _, tc := range cases {
		if got := ParseRequestedPhase(tc.in); got != tc.want {
			t.Errorf("ParseRequestedPhase(%q)=%d want %d", tc.in, got, tc.want)
		}
	}
}

func TestParsePhaseLock(t *testing.T) {
	if got := ParsePhaseLock("只做 Phase 1"); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
	if got := ParsePhaseLock("本轮只要 Phase 2"); got != 2 {
		t.Fatalf("got %d want 2", got)
	}
	if got := ParsePhaseLock("Phase 1 脚手架；Phase 2 鉴；三段都跑通"); got != 0 {
		t.Fatalf("full course must not lock, got %d", got)
	}
	if got := ParsePhaseLock("确认计划并立刻按 Phase 1 实现"); got != 0 {
		t.Fatalf("confirm+implement is not a hard lock, got %d", got)
	}
}

func TestResolveLockedPhase_OnlyExplicitLock(t *testing.T) {
	if got := ResolveLockedPhase("只做 Phase 1", 2); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
	// No explicit lock → 0 (allow advance), even when Active=2.
	if got := ResolveLockedPhase("继续实现", 2); got != 0 {
		t.Fatalf("got %d want 0 (no hard lock)", got)
	}
	if got := ResolveLockedPhase("第一段脚手架，第二段鉴权，第三段做到尾", 1); got != 0 {
		t.Fatalf("full course got %d want 0", got)
	}
}

func TestResolveImplementPhase(t *testing.T) {
	if got := ResolveImplementPhase("只做 Phase 1", 2); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
	if got := ResolveImplementPhase("进入 Phase 2", 1); got != 2 {
		t.Fatalf("got %d want 2", got)
	}
	if got := ResolveImplementPhase("三段都跑通", 1); got != 1 {
		t.Fatalf("full course implements Active, got %d want 1", got)
	}
	if got := ResolveImplementPhase("确认计划并立刻按 Phase 1 实现", 1); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
}

func TestWantsFullCourse(t *testing.T) {
	if !WantsFullCourse("三段都跑通，允许升相") {
		t.Fatal("expected full course")
	}
	if WantsFullCourse("只做 Phase 1") {
		t.Fatal("explicit lock alone is not full course")
	}
}

func TestCountTodoProgress_ActivePhaseOnly(t *testing.T) {
	todo := `> **Phase**: 2 of 5
## Phase 1 Checklist
- [x] done
- [ ] leftover phase1
## Phase 2 Checklist (active)
- [x] a
- [ ] b
## Completed
`
	completed, total := CountTodoProgressFromContent(todo)
	if completed != 1 || total != 2 {
		t.Fatalf("active phase only: completed=%d total=%d want 1/2", completed, total)
	}
}

func TestGetRemainingChecklistItems_ActivePhaseOnly(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	todo := `> **Phase**: 2 of 5
## Phase 1 Checklist
- [ ] leftover phase1
## Phase 2 Checklist (active)
- [ ] Authentication Handlers (0/2)
## Completed
`
	if err := os.WriteFile(filepath.Join(docs, "avatars_todo.md"), []byte(todo), 0644); err != nil {
		t.Fatal(err)
	}
	items := GetRemainingChecklistItems(tmp)
	if len(items) != 1 || !strings.Contains(items[0], "Authentication") {
		t.Fatalf("want only phase2 gap, got %#v", items)
	}
}

func TestExtractPhaseChecklistSection(t *testing.T) {
	todo := `> **Phase**: 2
## Phase 1 Checklist
- [x] old
## Phase 2 Checklist (active)
- [ ] cur
## Completed
`
	sec := extractPhaseChecklistSection(todo, "2")
	if !strings.Contains(sec, "## Phase 2 Checklist") || !strings.Contains(sec, "- [ ] cur") {
		t.Fatalf("bad section: %s", sec)
	}
	if strings.Contains(sec, "- [x] old") {
		t.Fatalf("phase1 leaked into phase2 section: %s", sec)
	}
}
