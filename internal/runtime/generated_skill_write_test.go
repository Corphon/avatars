package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedSkillWritePath_KeepsHomeAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AVATARS_HOME", home)
	project := t.TempDir()
	prepared := filepath.Join(home, "skills", "generated", "run-1-task-skill.md")
	got := generatedSkillWritePath(project, prepared)
	if filepath.Clean(got) != filepath.Clean(prepared) {
		t.Fatalf("HOME skill must stay absolute, got %q want %q", got, prepared)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("expected absolute HOME path, got %q", got)
	}
}

func TestGeneratedSkillWritePath_RelocatesProjectBasenameToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AVATARS_HOME", home)
	project := t.TempDir()
	leaked := filepath.Join(project, "run-1-task-prompt.md")
	got := generatedSkillWritePath(project, leaked)
	want := filepath.Join(home, "skills", "generated", "run-1-task-prompt.md")
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("basename leak must relocate to HOME, got %q want %q", got, want)
	}
}

func TestGeneratedSkillWritePath_ProjectSkillsStayRelative(t *testing.T) {
	t.Setenv("AVATARS_HOME", "")
	project := t.TempDir()
	prepared := filepath.Join(project, "skills", "generated", "local-skill.md")
	got := generatedSkillWritePath(project, prepared)
	slash := filepath.ToSlash(got)
	if !strings.Contains(slash, "skills/generated/local-skill.md") {
		t.Fatalf("in-project skills store should stay under skills/generated, got %q", got)
	}
	if filepath.Base(slash) == slash {
		t.Fatalf("must not collapse to basename: %q", got)
	}
}

func TestRecoverGeneratedSkillIfMissing_MovesLeakedRootCopy(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	prepared := filepath.Join(home, "skills", "generated", "skill.md")
	leaked := filepath.Join(project, "skill.md")
	if err := os.WriteFile(leaked, []byte("# skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recoverGeneratedSkillIfMissing(prepared, "skill.md", project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(prepared); err != nil {
		t.Fatalf("expected skill at HOME: %v", err)
	}
	if _, err := os.Stat(leaked); err == nil {
		t.Fatal("leaked project-root copy should be moved")
	}
}
