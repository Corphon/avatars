package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectPreferredDB_NegatedSQLiteIsNone(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  PreferredDB
	}{
		{
			name:  "cidrkit_style",
			input: "领域固定叫 cidrkit（不是网站、不是 CSV、不是数据库）。不要做成 HTTP；不是 SQLite。",
			want:  DBNone,
		},
		{
			name:  "explicit_not_sqlite",
			input: "纯库项目，不是 SQLite，标准库即可",
			want:  DBNone,
		},
		{
			name:  "still_wants_sqlite",
			input: "存储用 SQLite（不要 PostgreSQL）",
			want:  DBSQLite,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectPreferredDB(tc.input, t.TempDir())
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if tc.want == DBNone && StackConstraintPrompt(tc.input, t.TempDir()) != "" {
				t.Fatalf("expected empty STACK LOCK, got %q", StackConstraintPrompt(tc.input, t.TempDir()))
			}
		})
	}
}

func TestDetectPreferredDB_UserSQLiteBeatsPlanPostgres(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module x\n\nrequire github.com/jackc/pgx/v5 v5.0.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := DetectPreferredDB("存储用 SQLite（不要 PostgreSQL）", tmp)
	if got != DBSQLite {
		t.Fatalf("got %q want sqlite", got)
	}
}

func TestAlignStackDecisions_RewritesPlanAndPhase(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Plan
## Key Decisions
| Date | Decision | Rationale |
|------|----------|-----------|
| 2026-07-13 | Database: PostgreSQL with sqlx or pgx | production |
## Risks
`
	if err := os.WriteFile(filepath.Join(docs, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	phase := "# Phase 1\nSuccess: connect to PostgreSQL via pgx.\n"
	if err := os.WriteFile(filepath.Join(docs, "phase1.md"), []byte(phase), 0644); err != nil {
		t.Fatal(err)
	}
	summary, err := AlignStackDecisions(tmp, "强制 SQLite，不要 PostgreSQL")
	if err != nil {
		t.Fatal(err)
	}
	if summary == "" {
		t.Fatal("expected changes")
	}
	planOut, _ := os.ReadFile(filepath.Join(docs, "avatars_plan.md"))
	if strings.Contains(string(planOut), "PostgreSQL") {
		t.Fatalf("plan still has PostgreSQL:\n%s", planOut)
	}
	if !strings.Contains(strings.ToLower(string(planOut)), "sqlite") {
		t.Fatalf("plan missing sqlite:\n%s", planOut)
	}
	phaseOut, _ := os.ReadFile(filepath.Join(docs, "phase1.md"))
	if strings.Contains(string(phaseOut), "PostgreSQL") || strings.Contains(string(phaseOut), "pgx") {
		t.Fatalf("phase still postgres:\n%s", phaseOut)
	}
}
