package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/planner"
)

func TestCriticPreFlight_FormatterNotDestructive(t *testing.T) {
	// NL smoke #4/#7: bare "format" matched "formatter" and blocked all Builders.
	work := workflowNodeWorkContext{
		input: "实现Phase 1：创建 toc.py、toc/parser.py、toc/formatter.py、toc/cli.py",
	}
	risks := criticPreFlightCheck(work)
	for _, r := range risks {
		if strings.Contains(r, "destructive") {
			t.Fatalf("formatter must not be treated as destructive, got risk: %s", r)
		}
	}
}

func TestCriticPreFlight_RealDestructiveStillFlagged(t *testing.T) {
	work := workflowNodeWorkContext{input: "rm -rf the whole project and drop table users"}
	risks := criticPreFlightCheck(work)
	found := false
	for _, r := range risks {
		if strings.Contains(r, "destructive") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected destructive risk for rm -rf / drop table")
	}
}

func TestCriticPreFlight_PhaseMarkersNotBroad(t *testing.T) {
	// Mid Python NL PY8: long prompt with Phase 1..5 must not be flagged as
	// "without phase structure" just because it lacks "## Phase".
	var b strings.Builder
	b.WriteString("在这个空目录中从零构建中大型项目。")
	for i := 0; i < 40; i++ {
		b.WriteString("详细需求描述填充字符以便超过五百字。")
	}
	b.WriteString("Phase 1 脚手架；Phase 2 认证；Phase 3 CRUD；Phase 4 搜索；Phase 5 测试。本轮只要 Phase 1。")
	work := workflowNodeWorkContext{input: b.String(), plan: planner.Plan{Title: "knowledgebase"}}
	risks := criticPreFlightCheck(work)
	for _, r := range risks {
		if strings.Contains(r, "without phase structure") {
			t.Fatalf("Phase 1..5 markers should count as structure, got: %s", r)
		}
	}
}

func TestTaskHasPhaseStructure(t *testing.T) {
	if !taskHasPhaseStructure("Phase 1 scaffold and Phase 2 auth") {
		t.Fatal("expected true for Phase 1/2")
	}
	if taskHasPhaseStructure("just build a hello world app with lots of words but no phases here at all really") {
		t.Fatal("expected false without phase markers")
	}
}

func TestMigrationsLayoutGate(t *testing.T) {
	task := "分层：app/main.py、migrations/、tests/ Phase 1"
	if !taskRequiresMigrations(task) {
		t.Fatal("expected migrations required")
	}
	dir := t.TempDir()
	if got := checkMissingMigrationsLayout(dir, task, nil); got == "" {
		t.Fatal("expected missing layout stub path")
	}
	mig := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(mig, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mig, "001_init.sql"), []byte("CREATE TABLE t(id INT);\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := checkMissingMigrationsLayout(dir, task, nil); got != "" {
		t.Fatalf("expected present, got %q", got)
	}
}

