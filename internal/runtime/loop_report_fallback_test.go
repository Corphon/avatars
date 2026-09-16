package runtime

import (
	"strings"
	"testing"
)

func TestF104_FallbackSnapshotDoesNotGuessExcelFromREADME(t *testing.T) {
	readSummary := "Read README.md successfully.\nRead go.mod successfully.\nRead tokenbucketx.go successfully."
	snap := fallbackImplementationSnapshot(readSummary)
	lower := strings.ToLower(snap)
	for _, bad := range []string{"excel", "wails", "workbench", "workbook", "sql backend"} {
		if strings.Contains(lower, bad) {
			t.Fatalf("F104: README+go.mod must not invent %q, got:\n%s", bad, snap)
		}
	}
	if !strings.Contains(snap, "go.mod") {
		t.Fatalf("expected go.mod evidence line, got %s", snap)
	}
}

func TestF104_FallbackSnapshotKeepsWailsWhenEvidenceExists(t *testing.T) {
	readSummary := "Read wails.json successfully.\nRead frontend/wailsjs/go/main/App.js successfully."
	snap := fallbackImplementationSnapshot(readSummary)
	if !strings.Contains(strings.ToLower(snap), "wails") {
		t.Fatalf("real Wails evidence should keep the shape, got %s", snap)
	}
}

func TestF104_FallbackProjectSummaryNoMainGoWailsGuess(t *testing.T) {
	readSummary := "Read main.go successfully.\nRead README.md successfully."
	sum := fallbackProjectSummary(readSummary, `failed: returned 402: Insufficient Balance`)
	lower := strings.ToLower(sum)
	if strings.Contains(lower, "wails") || strings.Contains(lower, "excel") {
		t.Fatalf("main.go+README must not become Wails/Excel, got:\n%s", sum)
	}
}
