package arch

import (
	"strings"
	"testing"
)

func TestOverviewLooksLikePrompt(t *testing.T) {
	short := "Task board HTTP API with SQLite and JWT auth."
	if OverviewLooksLikePrompt(short) {
		t.Fatalf("short overview should not look like prompt")
	}
	prompt := strings.Repeat("Phase 1 must implement JWT SQLite checklist acceptance criteria you must create a build a do not skip. ", 20)
	if !OverviewLooksLikePrompt(prompt) {
		t.Fatalf("long multi-cue dump should look like prompt")
	}
	oneLine := strings.Repeat("x", 450)
	if !OverviewLooksLikePrompt(oneLine) {
		t.Fatalf("single long line should look like prompt")
	}
}

func TestSanitizeTaskOverview_OmitsPrompt(t *testing.T) {
	prompt := "Phase 1 must implement JWT and SQLite. Phase 2 must implement. You must create a checklist with acceptance criteria. Build a full API. Do not skip requirements:"
	prompt = prompt + strings.Repeat(" more requirements.", 30)
	got := SanitizeTaskOverview(prompt)
	if OverviewLooksLikePrompt(got) {
		t.Fatalf("sanitized overview still looks like prompt: %q", got)
	}
	if strings.Contains(got, "Phase 1 must implement") {
		t.Fatalf("sanitized overview still contains prompt body")
	}
}

func TestApplyAutoStatus_BlocksConfirmed(t *testing.T) {
	doc := NewArchDoc("test", ".", nil)
	doc.Overview = strings.Repeat("Phase 1 must implement JWT SQLite you must create a checklist. ", 25)
	issues := ApplyAutoStatus(doc, true, true)
	if doc.Meta.Status != "draft" {
		t.Fatalf("expected draft for prompt overview, got %s (issues=%v)", doc.Meta.Status, issues)
	}

	doc2 := NewArchDoc("test", ".", nil)
	doc2.Overview = "Small HTTP task API with SQLite."
	doc2.EntryPoints = []EntryPoint{{Path: "cmd/server/main.go", Kind: "http-server"}}
	// No RegPoints → draft even when healthy.
	ApplyAutoStatus(doc2, true, true)
	if doc2.Meta.Status != "draft" {
		t.Fatalf("expected draft without regpoints, got %s", doc2.Meta.Status)
	}

	doc3 := NewArchDoc("test", ".", nil)
	doc3.Overview = "Small HTTP task API with SQLite."
	doc3.EntryPoints = []EntryPoint{{Path: "cmd/server/main.go", Kind: "http-server"}}
	doc3.RegPoints = []RegistrationPoint{{
		ChangeType: "add-route",
		Files:      []RegFile{{Path: "cmd/server/main.go", ChangeHint: "register handler"}},
	}}
	ApplyAutoStatus(doc3, true, true)
	if doc3.Meta.Status != "confirmed" {
		t.Fatalf("expected confirmed when quality+health+regpoints OK, got %s", doc3.Meta.Status)
	}

	ApplyAutoStatus(doc3, false, true)
	if doc3.Meta.Status != "draft" {
		t.Fatalf("expected draft when unhealthy, got %s", doc3.Meta.Status)
	}
}

func TestQualityIssues_NilEmptyAndHardBlocks(t *testing.T) {
	issues := QualityIssues(nil)
	if len(issues) != 1 || issues[0].Code != "nil_doc" || !issues[0].Hard {
		t.Fatalf("nil doc: %+v", issues)
	}
	doc := NewArchDoc("test", ".", nil)
	issues = QualityIssues(doc)
	codes := map[string]bool{}
	for _, iss := range issues {
		codes[iss.Code] = true
	}
	if !codes["empty_overview"] || !codes["empty_regpoints"] || !codes["empty_entrypoints"] {
		t.Fatalf("expected empty-* codes, got %+v", issues)
	}
	blocks := HardQualityBlocks(doc)
	if len(blocks) == 0 {
		t.Fatal("empty overview should hard-block")
	}

	ok, reasons := CanConfirm(doc, false)
	if ok {
		t.Fatal("unhealthy empty doc must not confirm")
	}
	joined := strings.Join(reasons, "\n")
	if !strings.Contains(joined, "unhealthy") || !strings.Contains(joined, "Registration Points") {
		t.Fatalf("reasons=%v", reasons)
	}
}

func TestSanitizeTaskOverview_EmptyAndShort(t *testing.T) {
	empty := SanitizeTaskOverview("  ")
	if !strings.Contains(empty, "auto-generated from scan") {
		t.Fatalf("empty: %q", empty)
	}
	short := SanitizeTaskOverview("Tiny HTTP demo.")
	if short != "Tiny HTTP demo." {
		t.Fatalf("short intent should pass through, got %q", short)
	}
}

func TestCanConfirm_RequiresRegPoints(t *testing.T) {
	doc := NewArchDoc("test", ".", nil)
	doc.Overview = "OK overview."
	ok, reasons := CanConfirm(doc, true)
	if ok {
		t.Fatalf("expected refuse without regpoints, reasons=%v", reasons)
	}
}
