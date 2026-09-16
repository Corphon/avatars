package arch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanProject_SkipsFixtureAndReferenceTrees(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"cmd/main.go":                         "package main\nfunc main() {}\n",
		"new_project_for_test/go.mod":         "module fixture\n",
		"sheetforge_for_Test/internal/x.go":   "package x\n",
		"claude_code_main/src/query.ts":       "export {}\n",
		"SceneIntruderMCP_for_test/README.md": "# fixture\n",
	})
	scan, err := ScanProject(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(scan.FileTree, "\n")
	for _, leak := range []string{"new_project_for_test", "sheetforge_for_Test", "claude_code_main", "SceneIntruderMCP_for_test"} {
		if strings.Contains(joined, leak) {
			t.Fatalf("%s leaked into arch scan: %v", leak, scan.FileTree)
		}
	}
	if !strings.Contains(joined, "cmd") {
		t.Fatalf("expected real source, got %v", scan.FileTree)
	}
}

func TestScanProject_SkipsHarnessRuntimeLogs(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"lib.go":              "package lib\n",
		"_r50b_turn1.log":     "noise",
		"_r50b_turn1.err.log": "noise",
		"_r50b_turn1.pid":     "1",
		"_r50b_turn1.txt":     "prompt",
	})
	scan, err := ScanProject(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(scan.FileTree, "\n")
	for _, leak := range []string{"_r50b_turn1.log", "_r50b_turn1.err.log", "_r50b_turn1.pid", "_r50b_turn1.txt"} {
		if strings.Contains(joined, leak) {
			t.Fatalf("harness artifact leaked into arch scan: %v", scan.FileTree)
		}
	}
	if !strings.Contains(joined, "lib.go") {
		t.Fatalf("expected lib.go in scan, got %v", scan.FileTree)
	}
}

func TestScanProject_FindsEntriesAndSkipsNoise(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":                 "module example.com/demo\n\ngo 1.22\n",
		"cmd/server/main.go":     "package main\nfunc main() {}\n",
		"internal/handlers/h.go": "package handlers\n",
		"README.md":              "# demo\n",
		"config.yaml":            "port: 8080\n",
		"node_modules/left.js":   "ignored\n",
		".git/config":            "ignored\n",
	})

	scan, err := ScanProject(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(scan.FileTree, "\n")
	if strings.Contains(joined, "node_modules") || strings.Contains(joined, ".git") {
		t.Fatalf("noise dirs leaked into file tree: %v", scan.FileTree)
	}
	if scan.FileStats.ByExtension[".go"] < 2 {
		t.Fatalf("expected go files, stats=%+v", scan.FileStats)
	}
	foundMod := false
	for _, dm := range scan.DepManifests {
		if dm.Kind == "go-mod" {
			foundMod = true
		}
	}
	if !foundMod {
		t.Fatalf("expected go.mod manifest, got %+v", scan.DepManifests)
	}
	foundEntry := false
	for _, ec := range scan.EntryCandidates {
		if strings.Contains(filepath.ToSlash(ec.Path), "cmd/server/main.go") {
			foundEntry = true
		}
	}
	if !foundEntry {
		t.Fatalf("expected cmd entry, got %+v", scan.EntryCandidates)
	}
	keys := scan.KeyFiles()
	if len(keys) == 0 {
		t.Fatal("KeyFiles should include entries/manifests/config")
	}
}

func TestComputeFingerprint_ChangesWithFileContent(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "main.go")
	if err := os.WriteFile(p, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := ComputeFingerprint(root, []string{"main.go"})
	if len(first) != 16 {
		t.Fatalf("fingerprint length=%d", len(first))
	}
	if err := os.WriteFile(p, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := ComputeFingerprint(root, []string{"main.go"})
	if first == second {
		t.Fatal("fingerprint should change when file content changes")
	}
}

func TestWriteReadArchDoc_RoundTrip(t *testing.T) {
	root := t.TempDir()
	doc := NewArchDoc("analyze", root, nil)
	doc.Overview = "Tiny HTTP demo."
	doc.EntryPoints = []EntryPoint{{Path: "cmd/server/main.go", Kind: "http-server", Summary: "listen"}}
	doc.RegPoints = []RegistrationPoint{{
		ChangeType: "add-route",
		Files:      []RegFile{{Path: "cmd/server/main.go", ChangeHint: "register handler", LineRange: "L10-L20"}},
	}}
	if err := WriteArchDoc(root, doc); err != nil {
		t.Fatal(err)
	}
	got, err := ReadArchDoc(root)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !strings.Contains(got.Overview, "Tiny HTTP demo") {
		t.Fatalf("roundtrip overview: %+v", got)
	}
	if len(got.EntryPoints) != 1 || got.EntryPoints[0].Path != "cmd/server/main.go" {
		t.Fatalf("roundtrip entries: %+v", got.EntryPoints)
	}
	if len(got.RegPoints) != 1 || got.RegPoints[0].ChangeType != "add-route" {
		t.Fatalf("roundtrip regpoints: %+v", got.RegPoints)
	}
}

func TestEnsureArchitectureDoc_SkipsSummarizePlaceholder(t *testing.T) {
	root := t.TempDir()
	if err := EnsureArchitectureDoc(root, "summarize this attached file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "architecture.md")); err == nil {
		t.Fatal("placeholder task must not write architecture.md")
	}
}

func TestEnsureArchitectureDoc_CreatesOnce(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":             "module example.com/x\n\ngo 1.22\n",
		"cmd/server/main.go": "package main\nfunc main() { http.HandleFunc(\"/x\", h) }\n",
	})
	if err := EnsureArchitectureDoc(root, "Tiny HTTP demo."); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(root, "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureArchitectureDoc(root, "changed intent"); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(root, "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("second Ensure should be a no-op when the doc already exists")
	}
}

func TestWriteArchDoc_NilRejected(t *testing.T) {
	if err := WriteArchDoc(t.TempDir(), nil); err == nil {
		t.Fatal("expected nil-doc error")
	}
}

func TestReadArchDoc_MissingIsNil(t *testing.T) {
	got, err := ReadArchDoc(t.TempDir())
	if err != nil || got != nil {
		t.Fatalf("missing architecture.md should return nil,nil got %+v err=%v", got, err)
	}
}

func TestParseArchDoc_JSONAndFencedBlock(t *testing.T) {
	raw := `{"overview":"from json","entry_points":[{"path":"main.go","kind":"cli"}]}`
	doc, err := ParseArchDoc(raw)
	if err != nil || doc.Overview != "from json" {
		t.Fatalf("direct json: %+v err=%v", doc, err)
	}
	fenced := "here is the doc\n```json\n" + raw + "\n```\n"
	doc, err = ParseArchDoc(fenced)
	if err != nil || doc.Overview != "from json" {
		t.Fatalf("fenced json: %+v err=%v", doc, err)
	}
}

func TestNewScanForInit_EmptyProject(t *testing.T) {
	scan := NewScanForInit(t.TempDir())
	if scan == nil {
		t.Fatal("nil scan")
	}
}

func TestCheckStale_NilDraftAndConfirmedMtime(t *testing.T) {
	if check := CheckStale(".", nil); check == nil || !check.IsStale {
		t.Fatal("nil doc should be stale")
	}

	draft := NewArchDoc("analyze", ".", nil)
	if check := CheckStale(".", draft); !check.IsStale {
		t.Fatal("draft should be stale")
	}

	root := t.TempDir()
	entry := filepath.Join("cmd", "server", "main.go")
	writeTree(t, root, map[string]string{
		filepath.ToSlash(entry): "package main\nfunc main() {}\n",
		"go.mod":                "module example.com/x\n\ngo 1.22\n",
	})
	doc := NewArchDoc("analyze", root, []string{entry, "go.mod"})
	doc.Meta.Status = "confirmed"
	past := time.Now().Add(-2 * time.Hour)
	for _, rel := range []string{entry, "go.mod"} {
		if err := os.Chtimes(filepath.Join(root, rel), past, past); err != nil {
			t.Fatal(err)
		}
	}
	doc.Meta.GeneratedAt = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	doc.EntryPoints = []EntryPoint{{Path: filepath.ToSlash(entry), Kind: "cli"}}
	if check := CheckStale(root, doc); check.IsStale {
		t.Fatalf("fresh confirmed doc should not be stale: %+v", check)
	}

	if err := os.Chtimes(filepath.Join(root, entry), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if check := CheckStale(root, doc); !check.IsStale {
		t.Fatal("modified entry point should mark stale")
	}
}

func TestMarkStaleAndConfirmArchDoc(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"cmd/server/main.go": "package main\nfunc main() {}\n",
		"go.mod":             "module example.com/x\n\ngo 1.22\n",
	})
	doc := NewArchDoc("analyze", root, []string{"cmd/server/main.go", "go.mod"})
	doc.Overview = "Tiny HTTP demo with SQLite."
	doc.EntryPoints = []EntryPoint{{Path: "cmd/server/main.go", Kind: "cli"}}
	doc.RegPoints = []RegistrationPoint{{
		ChangeType: "add-route",
		Files:      []RegFile{{Path: "cmd/server/main.go", ChangeHint: "register"}},
	}}
	if err := ConfirmArchDoc(root, doc); err != nil {
		t.Fatal(err)
	}
	if doc.Meta.Status != "confirmed" {
		t.Fatalf("status=%s", doc.Meta.Status)
	}
	if err := MarkStale(root, doc); err != nil {
		t.Fatal(err)
	}
	got, err := ReadArchDoc(root)
	if err != nil || got == nil || got.Meta.Status != "stale" {
		t.Fatalf("expected stale on disk, got %+v err=%v", got, err)
	}
}

func TestConfirmArchDoc_RefusesWeakDoc(t *testing.T) {
	doc := NewArchDoc("analyze", t.TempDir(), nil)
	doc.Overview = "ok"
	if err := ConfirmArchDoc(t.TempDir(), doc); err == nil {
		t.Fatal("confirm without regpoints should fail")
	}
}

func TestMaybeMarkStaleAfterEdit_OnlyConfirmedArchFiles(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"cmd/server/main.go": "package main\nfunc main() {}\n",
		"notes.txt":          "x\n",
	})
	doc := NewArchDoc("analyze", root, nil)
	doc.Overview = "Tiny HTTP demo."
	doc.Meta.Status = "confirmed"
	doc.EntryPoints = []EntryPoint{{Path: "cmd/server/main.go", Kind: "cli"}}
	doc.RegPoints = []RegistrationPoint{{
		ChangeType: "add-route",
		Files:      []RegFile{{Path: "cmd/server/main.go", ChangeHint: "register"}},
	}}
	if err := WriteArchDoc(root, doc); err != nil {
		t.Fatal(err)
	}
	if MaybeMarkStaleAfterEdit(root, []string{"notes.txt"}) {
		t.Fatal("untracked file should not stale the doc")
	}
	if !MaybeMarkStaleAfterEdit(root, []string{"cmd/server/main.go"}) {
		t.Fatal("tracked entry should stale the doc")
	}
	got, _ := ReadArchDoc(root)
	if got == nil || got.Meta.Status != "stale" {
		t.Fatalf("expected stale, got %+v", got)
	}
}

func TestMergeArchDoc_AddsAndReplaces(t *testing.T) {
	existing := &ArchDoc{
		Overview:    "old",
		EntryPoints: []EntryPoint{{Path: "main.go", Kind: "cli"}},
		RegPoints: []RegistrationPoint{{
			ChangeType: "add-route",
			Files:      []RegFile{{Path: "old.go", ChangeHint: "old"}},
		}},
	}
	update := &ArchDoc{
		Overview:    "new",
		EntryPoints: []EntryPoint{{Path: "cmd/server/main.go", Kind: "http-server"}},
		RegPoints: []RegistrationPoint{{
			ChangeType: "add-route",
			Files:      []RegFile{{Path: "cmd/server/main.go", ChangeHint: "register"}},
		}},
		Layers: []Layer{{Name: "handlers", Paths: []string{"internal/handlers"}}},
	}
	merged := MergeArchDoc(existing, update)
	if merged.Overview != "new" || merged.Meta.Source != "resume" || merged.Meta.Status != "draft" {
		t.Fatalf("meta %+v overview=%q", merged.Meta, merged.Overview)
	}
	if len(merged.EntryPoints) != 2 {
		t.Fatalf("expected both entries, got %+v", merged.EntryPoints)
	}
	if merged.RegPoints[0].Files[0].Path != "cmd/server/main.go" {
		t.Fatalf("same change_type should replace files: %+v", merged.RegPoints)
	}
}

func TestStaleReasonString(t *testing.T) {
	if StaleNone.String() != "" {
		t.Fatalf("none=%q", StaleNone.String())
	}
	if !strings.Contains(StaleEntryMtime.String(), "entry point") {
		t.Fatalf("entry: %q", StaleEntryMtime.String())
	}
}
