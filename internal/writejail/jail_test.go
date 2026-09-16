package writejail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfine_ProjectOK(t *testing.T) {
	root := t.TempDir()
	got, err := Confine(root, "app/main.py")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "app", "main.py")
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestConfine_RejectsParentEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := Confine(root, "../evil.py"); err == nil {
		t.Fatal("expected escape error")
	}
}

func TestConfine_AllowsAvatarsHomeSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AVATARS_HOME", home)
	skillsFile := filepath.Join(home, "skills", "generated", "builder-skill.md")
	_ = os.MkdirAll(filepath.Dir(skillsFile), 0755)

	project := t.TempDir()
	got, err := Confine(project, skillsFile)
	if err != nil {
		t.Fatalf("skills under AVATARS_HOME must be allowed: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(skillsFile) {
		t.Fatalf("got %q want %q", got, skillsFile)
	}
}

func TestConfine_EmptyRootRejectsAbsolute(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "pollute.py")
	if _, err := Confine("", outside); err == nil {
		t.Fatal("empty project root must reject absolute paths")
	}
}

func TestConfine_EmptyRootPermissiveOptIn(t *testing.T) {
	t.Setenv("AVATARS_WRITEJAIL_PERMISSIVE", "1")
	outside := filepath.Join(t.TempDir(), "ok.py")
	got, err := Confine("", outside)
	if err != nil {
		t.Fatalf("permissive opt-in should allow abs path: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(outside) {
		t.Fatalf("got %q want %q", got, outside)
	}
}

func TestConfine_RejectsArbitraryOutside(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AVATARS_HOME", home)
	project := t.TempDir()
	outside := filepath.Join(t.TempDir(), "pollute.py")
	_, err := Confine(project, outside)
	if err == nil {
		t.Fatal("expected reject for arbitrary outside path")
	}
	if !strings.Contains(err.Error(), "escapes sandbox") {
		t.Fatalf("got %v", err)
	}
}
