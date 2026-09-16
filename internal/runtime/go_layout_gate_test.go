package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectConflictingGoLayout_RootAndSubpackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/cronnext\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte("// Package cronnext docs.\npackage cronnext\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cronnext"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cronnext", "parse.go"), []byte("package cronnext\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := detectConflictingGoLayout(dir)
	if got == "" || !strings.Contains(got, "conflicting Go layout") {
		t.Fatalf("expected conflict, got %q", got)
	}
}

func TestDetectConflictingGoLayout_HollowInternalDoc(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/cronnext\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cronnext"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cronnext", "parse.go"), []byte("package cronnext\nfunc Parse() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "doc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "doc", "doc.go"), []byte("package doc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := detectConflictingGoLayout(dir)
	if got == "" || !strings.Contains(got, "internal/doc") {
		t.Fatalf("expected hollow internal/doc conflict, got %q", got)
	}
}

func TestPurgeHollowGoDocPackages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/cronnext\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cronnext"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cronnext", "parse.go"), []byte("package cronnext\nfunc Parse() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "doc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "doc", "doc.go"), []byte("package doc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	deleted := purgeHollowGoDocPackages(dir)
	if len(deleted) == 0 {
		t.Fatal("expected purge of internal/doc")
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "doc", "doc.go")); !os.IsNotExist(err) {
		t.Fatal("internal/doc/doc.go should be removed")
	}
}

func TestDetectConflictingGoLayout_CleanNested(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/cronnext\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cronnext"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cronnext", "doc.go"), []byte("package cronnext\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cronnext", "parse.go"), []byte("package cronnext\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := detectConflictingGoLayout(dir); got != "" {
		t.Fatalf("clean layout should pass, got %q", got)
	}
}

func TestDetectConflictingGoLayout_MessageDoesNotInduceDocGo(t *testing.T) {
	// F56: gate text must not suggest e.g. <lib>/doc.go (induces hollow shells).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/urlcanon\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte("package urlcanon\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "urlcanon"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "urlcanon", "urlcanon.go"), []byte("package urlcanon\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := detectConflictingGoLayout(dir)
	if got == "" {
		t.Fatal("expected conflict")
	}
	lower := strings.ToLower(got)
	if strings.Contains(lower, "e.g.") && strings.Contains(lower, "doc.go") {
		t.Fatalf("F56: gate must not induce doc.go via e.g.: %q", got)
	}
	if strings.Contains(got, "/doc.go") || strings.HasSuffix(got, "doc.go") {
		t.Fatalf("F56: gate must not point heal at doc.go: %q", got)
	}
}

func TestHealConflictingGoLayout_PurgesRootDocShell(t *testing.T) {
	// F55: root doc.go + lib/ tree → purge root shell and continue clean.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/urlcanon\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte("// Package urlcanon.\npackage urlcanon\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "urlcanon"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "urlcanon", "urlcanon.go"), []byte("package urlcanon\nfunc Canon(s string) string { return s }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	clean, deleted := healConflictingGoLayout(dir)
	if !clean {
		t.Fatalf("expected clean after heal; leftover=%q deleted=%v", detectConflictingGoLayout(dir), deleted)
	}
	if len(deleted) == 0 {
		t.Fatal("expected root doc.go purged")
	}
	if _, err := os.Stat(filepath.Join(dir, "doc.go")); !os.IsNotExist(err) {
		t.Fatal("root doc.go should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "urlcanon", "urlcanon.go")); err != nil {
		t.Fatal("library impl must remain")
	}
}

func TestRewriteBuilderToolPath_AbsRootDocGoRemaps(t *testing.T) {
	// F55: absolute <wd>/doc.go must not bypass remap when layout.root=="".
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/urlcanon\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "urlcanon"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "urlcanon", "urlcanon.go"), []byte("package urlcanon\n"), 0644); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, "doc.go")
	got := rewriteBuilderToolPath(dir, "golang urlcanon pure library", abs)
	want := filepath.Join(dir, "urlcanon", "doc.go")
	if got != want {
		t.Fatalf("F55 abs root doc.go: got %q want %q", got, want)
	}
}

func TestDetectConflictingGoLayout_PrivateHelperOK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/jobpq\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jobpq.go"), []byte("package jobpq\nfunc New() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "heap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "heap", "heap.go"), []byte("package heap\nfunc Push() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := detectConflictingGoLayout(dir); got != "" {
		t.Fatalf("genuine internal/heap helper must pass, got %q", got)
	}
}

func TestHealLiftsMisplacedPublicPackageUnderInternal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/jobpq\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jobpq.go"), []byte("package jobpq\nfunc New() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "concurrency"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "concurrency", "concurrency_test.go"),
		[]byte("package jobpq\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "heap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "heap", "heap.go"), []byte("package heap\nfunc Push() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := detectConflictingGoLayout(dir); got == "" || !strings.Contains(got, "internal/concurrency") {
		t.Fatalf("expected misplaced public package conflict, got %q", got)
	}
	clean, deleted := healConflictingGoLayout(dir)
	if !clean {
		t.Fatalf("expected clean after lift; leftover=%q deleted=%v", detectConflictingGoLayout(dir), deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, "concurrency_test.go")); err != nil {
		t.Fatalf("expected lift to root concurrency_test.go: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "heap", "heap.go")); err != nil {
		t.Fatal("genuine helper must remain")
	}
}

func TestGoInternalIsServiceTree_HelperIsNotService(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/jobpq\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jobpq.go"), []byte("package jobpq\nfunc New() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "heap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "heap", "heap.go"), []byte("package heap\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if goInternalIsServiceTreeAt(dir) {
		t.Fatal("internal/heap helper must not count as a service tree")
	}

	svc := t.TempDir()
	if err := os.WriteFile(filepath.Join(svc, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(svc, "internal", "handlers"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc, "internal", "handlers", "h.go"), []byte("package handlers\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !goInternalIsServiceTreeAt(svc) {
		t.Fatal("internal/handlers must count as a service tree")
	}

	auto := t.TempDir()
	if err := os.WriteFile(filepath.Join(auto, "go.mod"), []byte("module example.com/focusedtelemetry\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(auto, "internal", "auto"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(auto, "internal", "auto", "runner.go"), []byte("package auto\nfunc Run() string { return \"ok\" }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !goInternalIsServiceTreeAt(auto) {
		t.Fatal("internal/auto without a root public package must count as a service tree")
	}
}
