package arch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleDoc() *ArchDoc {
	return &ArchDoc{
		Meta:     ArchMeta{Status: "confirmed", GeneratedAt: "2026-08-13T00:00:00Z"},
		Overview: "Task board HTTP API.",
		EntryPoints: []EntryPoint{
			{Path: "cmd/server/main.go", Kind: "http-server", Summary: "listen"},
		},
		Layers: []Layer{{Name: "handlers", Description: "HTTP", Paths: []string{"internal/handlers"}}},
		RegPoints: []RegistrationPoint{{
			ChangeType: "add-route",
			Files:      []RegFile{{Path: "cmd/server/main.go", LineRange: "L10-L20", ChangeHint: "register handler"}},
		}},
		Conventions: []Convention{{Name: "no-globals", Mandatory: true}},
	}
}

func TestBuildInjection_Levels(t *testing.T) {
	if BuildInjection(nil, InjectFull) != "" || BuildInjection(sampleDoc(), InjectNone) != "" {
		t.Fatal("nil/none should be empty")
	}
	overview := BuildInjection(sampleDoc(), InjectOverview)
	if !strings.Contains(overview, "Task board HTTP API") {
		t.Fatalf("overview missing project: %s", overview)
	}
	if !strings.Contains(overview, "add-route") || !strings.Contains(overview, "cmd/server/main.go") {
		t.Fatalf("overview missing regpoints: %s", overview)
	}
	if !strings.Contains(overview, "Mandatory conventions: no-globals") {
		t.Fatalf("overview missing conventions: %s", overview)
	}
	full := BuildInjection(sampleDoc(), InjectFull)
	if !strings.Contains(full, "# Project Architecture") {
		t.Fatalf("full should be formatted markdown, got %q", full)
	}
	if BuildInjection(sampleDoc(), InjectionLevel(99)) != "" {
		t.Fatal("unknown level should be empty")
	}
}

func TestInjectionLevelStringAndForOp(t *testing.T) {
	if InjectNone.String() != "none" || InjectOverview.String() != "overview" || InjectFull.String() != "full" {
		t.Fatal("level labels")
	}
	if InjectionLevel(9).String() != "unknown" {
		t.Fatal("unknown label")
	}
	if InjectionLevelForOp("run") != InjectOverview || InjectionLevelForOp("arch") != InjectFull || InjectionLevelForOp("ask") != InjectNone {
		t.Fatal("InjectionLevelForOp mapping")
	}
	if !NeedsArchDoc("bootstrap") || NeedsArchDoc("clarify") {
		t.Fatal("NeedsArchDoc mapping")
	}
}

func TestPlannerInjection_RequiresReadableDoc(t *testing.T) {
	if PlannerInjection(t.TempDir()) != "" {
		t.Fatal("missing doc should be empty")
	}
	root := t.TempDir()
	doc := sampleDoc()
	if err := WriteArchDoc(root, doc); err != nil {
		t.Fatal(err)
	}
	got := PlannerInjection(root)
	if !strings.Contains(got, "add-route") || !strings.Contains(got, "cmd/server/main.go") {
		t.Fatalf("planner injection: %s", got)
	}

	doc.Meta.Status = "stale"
	if err := WriteArchDoc(root, doc); err != nil {
		t.Fatal(err)
	}
	if PlannerInjection(root) != "" {
		t.Fatal("stale doc should not inject")
	}
}

func TestSuggestArchCommand_MentionsAnalyze(t *testing.T) {
	if !strings.Contains(SuggestArchCommand(), "arch --analyze") {
		t.Fatalf("got %q", SuggestArchCommand())
	}
}

func TestIsArchFile_MatchesEntriesAndRegFiles(t *testing.T) {
	doc := sampleDoc()
	if !IsArchFile(doc, "cmd/server/main.go") {
		t.Fatal("entry should match")
	}
	if IsArchFile(doc, "notes.txt") || IsArchFile(nil, "cmd/server/main.go") {
		t.Fatal("untracked/nil")
	}
}

func TestInjectionWorthy_EmptyHTTPDraftSkipped(t *testing.T) {
	root := t.TempDir()
	doc := &ArchDoc{
		Meta:     ArchMeta{Status: "draft"},
		Overview: "Go backend + HTTP API for a cache service.",
		EntryPoints: []EntryPoint{
			{Path: "cmd/server/main.go", Kind: "http-server", Summary: "HTTP API"},
		},
	}
	if InjectionWorthy(doc, root) {
		t.Fatal("F116: empty tree HTTP hallucination must not be injection-worthy")
	}
}

func TestInjectionWorthy_GroundedLibraryOK(t *testing.T) {
	root := t.TempDir()
	lib := filepath.Join(root, "ttlcache")
	if err := os.MkdirAll(lib, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "cache.go"), []byte("package ttlcache\n"), 0644); err != nil {
		t.Fatal(err)
	}
	doc := &ArchDoc{
		Meta:     ArchMeta{Status: "draft"},
		Overview: "In-process LRU+TTL cache library.",
		RegPoints: []RegistrationPoint{{
			ChangeType: "add-method",
			Files:      []RegFile{{Path: "ttlcache/cache.go", ChangeHint: "add Get/Set"}},
		}},
	}
	if !InjectionWorthy(doc, root) {
		t.Fatal("grounded library arch with sources should inject")
	}
}
