package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsAbsentReadPathError(t *testing.T) {
	if !isAbsentReadPathError(os.ErrNotExist) {
		t.Fatal("os.ErrNotExist should be absent")
	}
	win := errors.New("GetFileAttributesEx README.md: The system cannot find the file specified.")
	if !isAbsentReadPathError(win) {
		t.Fatal("windows missing-file wording should be absent")
	}
	if isAbsentReadPathError(errors.New("permission denied")) {
		t.Fatal("permission denied must stay hard")
	}
	dirErr := errors.New("read tool requires a file path, got directory: docs")
	if !isDirectoryReadError(dirErr) || !isSkippableSurveyReadError(dirErr) {
		t.Fatal("directory read must be a soft survey skip")
	}
}

func TestIsAdvisoryPartialVerdict(t *testing.T) {
	sum := "Verification finished with PARTIAL. 0 passed, 1 partial (0 pre-existing), 0 failed."
	if !isAdvisoryPartialVerdict(strings.ToLower(sum)) {
		t.Fatal("expected advisory for 0-failed PARTIAL")
	}
	if isAdvisoryPartialVerdict("verification finished with partial. 0 passed, 0 partial, 2 failed.") {
		t.Fatal("failed checks must not be advisory")
	}
}

func TestPreferredMigrationsStubPath_HonorsPromptMigrationsDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("fastapi\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "alembic.ini"), []byte("[alembic]\n"), 0644)
	task := "目录：app/main.py、app/api/、migrations/"
	got := preferredMigrationsStubPath(dir, task)
	if got != "migrations/versions/001_init.py" {
		t.Fatalf("prompt migrations/ should win over alembic.ini, got %s", got)
	}
}

func TestCollectBuilderFinalizeGaps_MigrationsRequired(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("fastapi\n"), 0644)
	task := "目录：app/main.py、migrations/；SQLite notes"
	gaps := collectBuilderFinalizeGaps(dir, task, nil)
	if len(gaps) == 0 {
		t.Fatal("expected migrations stub gap")
	}
	found := false
	for _, g := range gaps {
		if strings.Contains(filepath.ToSlash(g.Path), "migrations") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected migrations path in gaps: %+v", gaps)
	}
}
