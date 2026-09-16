package arch

import (
	"context"
	"strings"
	"testing"
)

func TestEnrichArchWithLLM_SkipPaths(t *testing.T) {
	if got := EnrichArchWithLLM(context.Background(), nil, ".", &ArchDoc{}, nil); got.Attempted || !strings.Contains(got.Reason, "no llm") {
		t.Fatalf("nil client: %+v", got)
	}

	doc := sampleDoc()
	doc.Overview = "Task board HTTP API with SQLite."
	got := EnrichArchWithLLM(context.Background(), stubArchLLM{text: `{"overview":"x"}`}, t.TempDir(), doc, nil)
	if got.Attempted || !strings.Contains(got.Reason, "already enriched") {
		t.Fatalf("skip when already useful: %+v", got)
	}

	empty := NewArchDoc("test", t.TempDir(), nil)
	got = EnrichArchWithLLM(context.Background(), stubArchLLM{text: `{"overview":"x"}`}, t.TempDir(), empty, &ProjectScan{
		FileStats: FileStats{ByExtension: map[string]int{}},
	})
	if got.Attempted || !strings.Contains(got.Reason, "empty project") {
		t.Fatalf("empty project: %+v", got)
	}
}

func TestEnrichArchWithLLM_AppliesOverviewAndRegPoints(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":             "module example.com/x\n\ngo 1.22\n",
		"cmd/server/main.go": "package main\nfunc main() {}\n",
	})
	scan, err := ScanProject(root)
	if err != nil {
		t.Fatal(err)
	}
	doc := NewArchDoc("test", root, scan.KeyFiles())
	doc.Overview = "auto-generated from scan"
	payload := `{
  "overview": "Go CLI with a server entrypoint.",
  "entry_points": [{"path": "cmd/server/main.go", "kind": "cli", "summary": "main"}],
  "layers": [{"name": "cmd", "description": "binaries", "paths": []}],
  "registration_points": [{"change_type": "add-command", "files": [{"path": "cmd/server/main.go", "change_hint": "register"}]}],
  "conventions": [{"name": "no-globals", "description": "avoid package state", "mandatory": true}],
  "dependencies": {"internal": [], "external": []}
}`
	got := EnrichArchWithLLM(context.Background(), stubArchLLM{text: payload}, root, doc, scan)
	if !got.Attempted || !got.Applied {
		t.Fatalf("expected applied enrich: %+v", got)
	}
	if !strings.Contains(doc.Overview, "Go CLI") {
		t.Fatalf("overview=%q", doc.Overview)
	}
	if !hasUsefulRegPoints(doc) {
		t.Fatalf("regpoints %+v", doc.RegPoints)
	}
	if doc.Meta.Source != "workflow-auto-enrich" {
		t.Fatalf("source=%q", doc.Meta.Source)
	}
	if FormatEnrichLog(got) != "applied (overview/regpoints enriched)" {
		t.Fatalf("log=%q", FormatEnrichLog(got))
	}
}

func TestFormatEnrichLog_SkippedAndUnused(t *testing.T) {
	if FormatEnrichLog(EnrichResult{Reason: "no llm client or nil doc"}) != "skipped (no llm client or nil doc)" {
		t.Fatal("skipped log")
	}
	if FormatEnrichLog(EnrichResult{Attempted: true, Reason: "llm output unused (failed quality checks)"}) != "attempted-not-applied (llm output unused (failed quality checks))" {
		t.Fatal("unused log")
	}
}

func TestMergeLayersPreferLLM_AttachesScanPaths(t *testing.T) {
	scan := []Layer{{Name: "cmd", Paths: []string{"cmd/server"}}}
	llmLayers := []Layer{{Name: "cmd", Description: "binaries"}}
	got := mergeLayersPreferLLM(scan, llmLayers)
	if len(got) != 1 || got[0].Description != "binaries" || len(got[0].Paths) != 1 {
		t.Fatalf("got %+v", got)
	}
	if mergeLayersPreferLLM(scan, nil)[0].Name != "cmd" {
		t.Fatal("empty llm should keep scan")
	}
}

func TestMergeEntryPoints_KeepsHTTPKind(t *testing.T) {
	scan := []EntryPoint{{Path: "cmd/server/main.go", Kind: "http-server", Summary: "Entry point"}}
	llm := []EntryPoint{{Path: "cmd/server/main.go", Kind: "cli", Summary: "listen on :8080"}}
	got := mergeEntryPoints(scan, llm)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Kind != "http-server" {
		t.Fatalf("http kind must win, got %q", got[0].Kind)
	}
	if got[0].Summary != "listen on :8080" {
		t.Fatalf("summary should fill from llm, got %q", got[0].Summary)
	}
}
