package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestS3_LoadFeaturesMemory(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "agent.yaml")
	if err := os.WriteFile(path, []byte("features:\n  memory: true\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !LoadFeaturesMemory(path) {
		t.Fatal("expected memory=true")
	}
	if err := os.WriteFile(path, []byte("features:\n  memory: false\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if LoadFeaturesMemory(path) {
		t.Fatal("expected memory=false")
	}
	if LoadFeaturesMemory(filepath.Join(tmp, "missing.yaml")) {
		t.Fatal("missing config should default false")
	}
}
