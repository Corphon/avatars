package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanRecentSourceFiles_IncludesRust(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "src", "main.rs"), []byte("fn main() {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname=\"x\"\nedition=\"2021\"\n"), 0644)
	files := scanRecentSourceFiles(dir)
	found := false
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f), ".rs") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected .rs in scan, got %#v", files)
	}
}

func TestCrossLangHealthCheck_CargoWithoutEntrypoint(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname=\"x\"\nversion=\"0.1.0\"\nedition=\"2021\"\n"), 0644)
	got := crossLangHealthCheck(dir)
	if got == "" || !strings.Contains(got, "main.rs") {
		t.Fatalf("expected missing entrypoint failure, got %q", got)
	}
}

func TestCrossLangHealthCheck_CargoBrokenDeps(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(`[package]
name = "x"
version = "0.1.0"
edition = "2021"
[dependencies]
sqlx = { version = "0.7", features = ["bundled"] }
`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "src", "main.rs"), []byte("fn main() {}\n"), 0644)
	got := crossLangHealthCheck(dir)
	if got == "" {
		t.Fatal("expected cargo check failure for bogus sqlx feature")
	}
	if !strings.Contains(got, "cargo") {
		t.Fatalf("expected cargo-related error, got %q", got)
	}
}

func TestCodeCompletenessIssues_TruncatedRustSignature(t *testing.T) {
	content := "use axum::{Json, http::StatusCode};\n\npub async fn health_check() -> (StatusCode, Json"
	issues := codeCompletenessIssues(content)
	if len(issues) == 0 {
		t.Fatal("expected truncation issues for mid-signature Rust fn")
	}
}

func TestCheckMissingRustEntrypoint(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname=\"x\"\nversion=\"0.1.0\"\nedition=\"2021\"\n"), 0644)
	if got := checkMissingRustEntrypoint(dir, nil); got != "src/main.rs" {
		t.Fatalf("got %q", got)
	}
	gaps := collectBuilderFinalizeGaps(dir, "rust axum service", nil)
	found := false
	for _, g := range gaps {
		if g.Path == "src/main.rs" && strings.Contains(g.Content, "fn main") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected main.rs stub in gaps: %+v", gaps)
	}
}

func TestCheckFileHealth_RejectsTruncatedRust(t *testing.T) {
	content := "pub async fn health_check() -> (StatusCode, Json"
	if reason := checkFileHealth("src/routes/health.rs", content); reason == "" {
		t.Fatal("expected truncated .rs to fail checkFileHealth")
	}
}
