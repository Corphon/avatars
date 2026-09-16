// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package evaluation

import "testing"

func TestBuildDiagnosticRecords_EmitsPassiveFeedback(t *testing.T) {
	records := BuildDiagnosticRecords([]Diagnostic{{
		Tool:     "go-lsp",
		Severity: "error",
		Message:  "undefined: missingSymbol",
		Path:     "internal/runtime/tools.go",
		Line:     12,
		Column:   4,
		Code:     "undefined-name",
		Source:   "gopls",
	}})
	if len(records) != 1 {
		t.Fatalf("expected 1 diagnostic record, got %d", len(records))
	}
	record := records[0]
	if record.Tool != "go-lsp" {
		t.Fatalf("expected tool go-lsp, got %q", record.Tool)
	}
	if record.Kind != "passive_feedback" {
		t.Fatalf("expected passive_feedback kind, got %q", record.Kind)
	}
	if record.Verdict != "FAIL" {
		t.Fatalf("expected FAIL verdict for error diagnostic, got %q", record.Verdict)
	}
	if record.Cause != "diagnostic_error" {
		t.Fatalf("expected diagnostic_error cause, got %q", record.Cause)
	}
	if record.Source != "gopls" {
		t.Fatalf("expected gopls source, got %q", record.Source)
	}
	if record.Summary != "Imported diagnostic ERROR for go-lsp at internal/runtime/tools.go:12:4: undefined: missingSymbol" {
		t.Fatalf("unexpected diagnostic summary %q", record.Summary)
	}
	if len(record.Details) != 4 {
		t.Fatalf("expected 4 diagnostic details, got %d", len(record.Details))
	}
}

func TestBuildDiagnosticRecords_DefaultsWarningSeverity(t *testing.T) {
	records := BuildDiagnosticRecords([]Diagnostic{{Message: "unused import", Path: "main.go"}})
	if len(records) != 1 {
		t.Fatalf("expected 1 diagnostic record, got %d", len(records))
	}
	record := records[0]
	if record.Tool != "diagnostic" {
		t.Fatalf("expected default tool diagnostic, got %q", record.Tool)
	}
	if record.Verdict != "PARTIAL" {
		t.Fatalf("expected PARTIAL verdict for default warning severity, got %q", record.Verdict)
	}
	if record.Cause != "diagnostic_warning" {
		t.Fatalf("expected diagnostic_warning cause, got %q", record.Cause)
	}
	if record.Source != "diagnostic_import" {
		t.Fatalf("expected default diagnostic_import source, got %q", record.Source)
	}
}

func TestBuildDiagnosticWarmLesson_PrefersFailingDiagnostics(t *testing.T) {
	lesson, ok := BuildDiagnosticWarmLesson([]Record{
		{Tool: "eslint", Kind: "passive_feedback", Verdict: "PARTIAL", Summary: "Imported diagnostic WARNING for eslint at src/app.ts:1:1: unused variable", Source: "diagnostic_import"},
		{Tool: "go-lsp", Kind: "passive_feedback", Verdict: "FAIL", Summary: "Imported diagnostic ERROR for go-lsp at internal/runtime/tools.go:12:4: undefined: missingSymbol", Source: "diagnostic_import"},
	})
	if !ok {
		t.Fatal("expected imported diagnostics warm lesson")
	}
	if lesson.Kind != "diagnostic_import" {
		t.Fatalf("expected diagnostic_import warm lesson kind, got %q", lesson.Kind)
	}
	if lesson.Confidence != "high" {
		t.Fatalf("expected high confidence diagnostics warm lesson, got %q", lesson.Confidence)
	}
	if lesson.Source != "diagnostic_import" {
		t.Fatalf("expected diagnostic_import source, got %q", lesson.Source)
	}
	if lesson.Summary != "Before repeating go-lsp, resolve this imported diagnostic: Imported diagnostic ERROR for go-lsp at internal/runtime/tools.go:12:4: undefined: missingSymbol" {
		t.Fatalf("unexpected diagnostics warm lesson summary %q", lesson.Summary)
	}
}
