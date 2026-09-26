package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestF105_ApplyWritePathRewriteLiftsPublicAPI(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/workqueuex\n\ngo 1.22\n"), 0644)
	task := "pure golang library workqueuex public API at repo root"
	content := "package workqueuex\n\nfunc New() {}\n"
	final, from := applyWritePathRewrite(dir, task, "internal/worker/worker.go", content)
	if final != "worker.go" {
		t.Fatalf("want worker.go, got %q (from %q)", final, from)
	}
	if from != "internal/worker/worker.go" {
		t.Fatalf("want remapped_from internal/worker/worker.go, got %q", from)
	}
}

func TestF106_PurgeEmptyInternalAndSrcInternal(t *testing.T) {
	dir := t.TempDir()
	empty := []string{
		filepath.Join(dir, "internal", "worker"),
		filepath.Join(dir, "_internal", "clock"),
		filepath.Join(dir, "src", "internal", "queue"),
	}
	for _, d := range empty {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(dir, "internal", "fakeclock")
	if err := os.MkdirAll(keep, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keep, "clock.go"), []byte("package fakeclock\n"), 0644); err != nil {
		t.Fatal(err)
	}
	removed := purgeEmptyBurialDirs(dir)
	if len(removed) == 0 {
		t.Fatal("expected empty burial dirs purged")
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "worker")); err == nil {
		t.Fatal("empty internal/worker should be gone")
	}
	if _, err := os.Stat(filepath.Join(keep, "clock.go")); err != nil {
		t.Fatal("internal/fakeclock must stay")
	}
}

func TestF110_UnevidencedDrainAndHumanDelivery(t *testing.T) {
	dir := t.TempDir()
	src := `package workqueuex

type Queue struct{}

func (q *Queue) Peek() {}
func (q *Queue) Pop() {}
`
	if err := os.WriteFile(filepath.Join(dir, "queue.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	synth := "DeadLetter Peek/Drain is ready. Usage: q.Drain()"
	got := detectUnevidencedClaimedSymbols(dir, synth)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "Drain") {
		t.Fatalf("expected Drain unevidenced, got %q", joined)
	}
	if strings.Contains(joined, "F110") {
		t.Fatalf("ticket id leaked into the note: %q", joined)
	}
	if strings.Contains(joined, "Peek") {
		t.Fatalf("Peek exists on disk, should not flag: %q", joined)
	}

	if err := os.WriteFile(filepath.Join(dir, "DELIVERY.md"), []byte("# Analysis Report\n\n## Proven Findings\n- fake\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := reconcileFinalDeliveryAnswer(dir, "completed", synth, true, true, "", []string{"queue.go"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "DELIVERY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(body)), "# Analysis Report") {
		t.Fatalf("F110: DELIVERY.md must not stay Analysis Report:\n%s", body)
	}
	if !strings.Contains(string(body), "Delivery") {
		t.Fatalf("expected delivery summary in DELIVERY.md, got:\n%s", body)
	}
}

func TestF110_ResolveAnalysisReportPathSkipsDelivery(t *testing.T) {
	got := resolveAnalysisReportPath("用人话写交付说明到 DELIVERY.md，并写入 analysis.md")
	if strings.EqualFold(got, "DELIVERY.md") {
		t.Fatalf("must not hijack DELIVERY.md as analysis report, got %q", got)
	}
	if got != "" && isHumanDeliveryDocPath(got) {
		t.Fatalf("analysis report path must not be a human delivery doc: %q", got)
	}
}

func TestQualifiedPackageTypeIsNotAnUnevidencedExport(t *testing.T) {
	dir := t.TempDir()
	src := "package counter\n\nimport \"io\"\n\nfunc Count(r io.Reader) ([]string, error) { return nil, nil }\n"
	if err := os.WriteFile(filepath.Join(dir, "counter.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := detectUnevidencedClaimedSymbols(dir, "Call Count with an io.Reader. Also q.Drain().")
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "Reader") {
		t.Fatalf("package-qualified Reader must not be flagged:\n%s", joined)
	}
	if !strings.Contains(joined, "Drain") {
		t.Fatalf("unqualified Drain call must still be flagged:\n%s", joined)
	}
	if strings.Contains(joined, "F110") {
		t.Fatalf("ticket id leaked: %s", joined)
	}
}

func TestHTTPMethodsAreNotUnevidencedExports(t *testing.T) {
	dir := t.TempDir()
	src := "package httpapi\n\nfunc Add(title string) error { return nil }\n"
	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	synth := "Routes: POST /bookmarks, GET /bookmarks?tag=, PUT /bookmarks, PATCH /bookmarks, DELETE /bookmarks/{id}. Also MissingFunc()."
	got := detectUnevidencedClaimedSymbols(dir, synth)
	joined := strings.Join(got, "\n")
	for _, verb := range []string{"POST", "GET", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		if strings.Contains(joined, "`"+verb+"`") {
			t.Fatalf("HTTP method %s must not be an export claim:\n%s", verb, joined)
		}
	}
	if !strings.Contains(joined, "MissingFunc") {
		t.Fatalf("real missing call must still be flagged:\n%s", joined)
	}
}

func TestPathSegmentsAndHeadingsAreNotUnevidencedExports(t *testing.T) {
	dir := t.TempDir()
	src := "package ledger\n\nfunc Add(cents int64) error { return nil }\n"
	if err := os.WriteFile(filepath.Join(dir, "ledger.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	synth := "Wrote files under D:/SKF/dev/ledger. CLI/entrypoint stays. See **Risks/Notes**. MissingFunc() is not real."
	got := detectUnevidencedClaimedSymbols(dir, synth)
	joined := strings.Join(got, "\n")
	for _, name := range []string{"SKF", "CLI", "Risks", "Notes"} {
		if strings.Contains(joined, "`"+name+"`") {
			t.Fatalf("%s must not be an export claim:\n%s", name, joined)
		}
	}
	if !strings.Contains(joined, "MissingFunc") {
		t.Fatalf("real missing call must still be flagged:\n%s", joined)
	}
}
