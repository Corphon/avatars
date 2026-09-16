package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFakeHarnessTree(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "avatars"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal", "runtime"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module avatars\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	engine := "package runtime\n\nimport \"avatars/internal/workflow\"\n\nfunc Keep() {}\n"
	if err := os.WriteFile(filepath.Join(root, "internal", "runtime", "engine.go"), []byte(engine), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestIsAvatarsHarnessTree_PackageCwdIsHarness(t *testing.T) {
	if !isAvatarsHarnessTree(".") {
		t.Fatal("go test ./internal/runtime cwd must count as harness source tree")
	}
	if !isAvatarsHarnessTree("engine.go") && !fileLivesInHarnessTree("engine.go") {
		t.Fatal("engine.go in this package must live in the harness tree")
	}
}

func TestPurgeUnhealthySourcesOnDisk_DoesNotDeleteHarnessRuntime(t *testing.T) {
	root := t.TempDir()
	writeFakeHarnessTree(t, root)
	enginePath := filepath.Join(root, "internal", "runtime", "engine.go")

	removed := purgeUnhealthySourcesOnDisk(root)
	if len(removed) != 0 {
		t.Fatalf("harness root sweep must be a no-op, removed=%v", removed)
	}
	if _, err := os.Stat(enginePath); err != nil {
		t.Fatalf("engine.go must survive: %v", err)
	}

	pkgDir := filepath.Join(root, "internal", "runtime")
	removed = purgeUnhealthySourcesOnDisk(pkgDir)
	if len(removed) != 0 {
		t.Fatalf("harness package-dir sweep must be a no-op, removed=%v", removed)
	}
	if _, err := os.Stat(enginePath); err != nil {
		t.Fatalf("engine.go must survive package-dir sweep: %v", err)
	}
}

func TestPurgeUnhealthySourcesOnDisk_FixtureIslandStillPurges(t *testing.T) {
	root := t.TempDir()
	writeFakeHarnessTree(t, root)
	appDir := filepath.Join(root, "new_project_for_test", "app")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(appDir, "broken.go")
	if err := os.WriteFile(bad, []byte("package app\n\nfunc Broken() {\n"), 0644); err != nil {
		t.Fatal(err)
	}
	removed := purgeUnhealthySourcesOnDisk(appDir)
	if len(removed) == 0 {
		t.Fatal("fixture island must still purge unhealthy sources")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatalf("broken.go should be deleted in fixture, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "runtime", "engine.go")); err != nil {
		t.Fatalf("harness engine.go must stay: %v", err)
	}
}

func TestHealConflictingGoLayout_SkipsHarness(t *testing.T) {
	root := t.TempDir()
	writeFakeHarnessTree(t, root)
	clean, deleted := healConflictingGoLayout(root)
	if !clean || len(deleted) != 0 {
		t.Fatalf("harness heal must no-op, clean=%v deleted=%v", clean, deleted)
	}
}

func TestSweepRoot_PrefersProjectRoot(t *testing.T) {
	e := &Engine{projectRoot: t.TempDir()}
	if got := e.sweepRoot(); got != e.projectRoot {
		t.Fatalf("sweepRoot=%q want projectRoot", got)
	}
}
