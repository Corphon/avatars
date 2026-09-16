package arch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"avatars/internal/llm"
)

type stubArchLLM struct {
	text string
	err  error
}

func (s stubArchLLM) Provider() string { return "stub" }

func (s stubArchLLM) Supports(llm.Capability) bool { return false }

func (s stubArchLLM) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	if s.err != nil {
		return llm.Response{}, s.err
	}
	if strings.TrimSpace(req.SystemPrompt) == "" || strings.TrimSpace(req.UserPrompt) == "" {
		return llm.Response{}, errors.New("empty prompts")
	}
	if !req.StructuredOutput {
		return llm.Response{}, errors.New("expected structured output")
	}
	return llm.Response{Text: s.text, Provider: "stub", Model: "stub"}, nil
}

func TestParseArchDoc_ProseWrappedAndInvalid(t *testing.T) {
	wrapped := `sure, here you go:
{"overview":"wrapped","entry_points":[{"path":"main.go","kind":"cli"}]}
thanks`
	doc, err := ParseArchDoc(wrapped)
	if err != nil || doc.Overview != "wrapped" {
		t.Fatalf("prose-wrapped json: %+v err=%v", doc, err)
	}
	if _, err := ParseArchDoc("not architecture json at all"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestValidateArchDocPaths_DropsHallucinations(t *testing.T) {
	scan := &ProjectScan{
		FileTree: []string{"cmd/server/main.go", "internal/handlers/h.go"},
	}
	doc := &ArchDoc{
		EntryPoints: []EntryPoint{
			{Path: "cmd/server/main.go", Kind: "cli"},
			{Path: "internal/provider/foo.go", Kind: "library"},
			{Path: "wrong/handlers/h.go", Kind: "library", Summary: "handlers"},
		},
		RegPoints: []RegistrationPoint{
			{ChangeType: "add-route", Files: []RegFile{{Path: "cmd/server/main.go"}}},
			{ChangeType: "ghost", Files: []RegFile{{Path: "pkg/does-not-exist.go"}}},
			{ChangeType: "glob", Files: []RegFile{{Path: "internal/**"}}},
		},
	}
	got := validateArchDocPaths(scan, doc)
	if len(got.EntryPoints) != 2 {
		t.Fatalf("expected real + fuzzy entries, got %+v", got.EntryPoints)
	}
	foundFuzzy := false
	for _, ep := range got.EntryPoints {
		if strings.Contains(ep.Path, "h.go") && strings.Contains(ep.Summary, "approximate") {
			foundFuzzy = true
		}
		if strings.Contains(ep.Path, "provider") {
			t.Fatalf("hallucinated entry survived: %+v", ep)
		}
	}
	if !foundFuzzy {
		t.Fatalf("expected fuzzy filename keep, got %+v", got.EntryPoints)
	}
	if len(got.RegPoints) != 2 {
		t.Fatalf("expected add-route + glob, got %+v", got.RegPoints)
	}
	for _, rp := range got.RegPoints {
		if rp.ChangeType == "ghost" {
			t.Fatal("hallucinated regpoint survived")
		}
	}
}

func TestGenerateArchDoc_StubLLMAndPathCleanup(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":             "module example.com/x\n\ngo 1.22\n",
		"cmd/server/main.go": "package main\nfunc main() {}\n",
	})
	scan, err := ScanProject(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{
  "overview": "Small CLI server.",
  "entry_points": [
    {"path": "cmd/server/main.go", "kind": "cli", "summary": "main"},
    {"path": "internal/ghost/x.go", "kind": "library", "summary": "nope"}
  ],
  "layers": [{"name": "cmd", "description": "entry", "paths": ["cmd"]}],
  "registration_points": [{"change_type": "add-command", "files": [{"path": "cmd/server/main.go", "change_hint": "register"}]}],
  "conventions": [],
  "dependencies": {"internal": [], "external": []}
}`
	doc, err := GenerateArchDoc(context.Background(), stubArchLLM{text: payload}, scan, "analyze", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Meta.Source != "analyze" || doc.Meta.Status != "draft" {
		t.Fatalf("meta %+v", doc.Meta)
	}
	if doc.Overview != "Small CLI server." {
		t.Fatalf("overview=%q", doc.Overview)
	}
	if len(doc.EntryPoints) != 1 || doc.EntryPoints[0].Path != "cmd/server/main.go" {
		t.Fatalf("hallucinated entry should drop: %+v", doc.EntryPoints)
	}
	if len(doc.Meta.LastModifiedFiles) == 0 || doc.Meta.FileFingerprint == "" {
		t.Fatalf("expected fingerprint/key files, meta=%+v", doc.Meta)
	}
}

func TestGenerateArchDoc_LLMError(t *testing.T) {
	_, err := GenerateArchDoc(context.Background(), stubArchLLM{err: errors.New("boom")}, &ProjectScan{}, "init", "make a cli", nil, "")
	if err == nil || !strings.Contains(err.Error(), "LLM call failed") {
		t.Fatalf("expected llm wrap error, got %v", err)
	}
}

func TestBuildArchPrompts_ModeAndScan(t *testing.T) {
	sys := buildArchSystemPrompt("init", "HTTP routes")
	if !strings.Contains(sys, "Mode: INIT") {
		t.Fatalf("init mode missing: %s", sys[len(sys)-400:])
	}
	if !strings.Contains(sys, "FOCUS TOPIC: HTTP routes") {
		t.Fatalf("focus topic missing")
	}
	scan := &ProjectScan{
		Root:            "/tmp/p",
		FileTree:        []string{"cmd/main.go", "internal/x.go"},
		EntryCandidates: []EntryCandidate{{Path: "cmd/main.go", Reason: "in cmd/ directory"}},
		DepManifests:    []DepManifest{{Path: "go.mod", Kind: "go-mod"}},
		KeyDirNames:     []string{"cmd", "internal"},
		FileStats:       FileStats{TotalFiles: 2, ByExtension: map[string]int{".go": 2}},
	}
	analyze := buildArchUserPrompt(scan, "analyze", "ignored in analyze mode", nil)
	for _, want := range []string{"cmd/main.go", "go.mod"} {
		if !strings.Contains(analyze, want) {
			t.Fatalf("analyze prompt missing %q:\n%s", want, analyze)
		}
	}
	initPrompt := buildArchUserPrompt(scan, "init", "make a tiny HTTP CLI", nil)
	if !strings.Contains(initPrompt, "make a tiny HTTP CLI") {
		t.Fatalf("init prompt missing intent:\n%s", initPrompt)
	}
}

func TestExtractDirTreeAndCountPrefix(t *testing.T) {
	dirs := extractDirTree([]string{"cmd/server/main.go", "internal/handlers/h.go"})
	joined := strings.Join(dirs, "\n")
	if !strings.Contains(joined, "cmd/") || !strings.Contains(joined, "internal/") {
		t.Fatalf("dir tree=%v", dirs)
	}
	files := []string{"cmd/a.go", "cmd/b.go", "internal/c.go"}
	if countFilesWithPrefix(files, "cmd/") != 2 {
		t.Fatalf("count=%d", countFilesWithPrefix(files, "cmd/"))
	}
}
