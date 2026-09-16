package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRuntimePath_UsesAvatarsHome(t *testing.T) {
	tempDir := t.TempDir()
	avatarsHome := filepath.Join(tempDir, "avatars")
	if err := os.MkdirAll(filepath.Join(avatarsHome, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir avatars configs failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(avatarsHome, "configs", "agent.yaml"), []byte("llm:\n  provider: genkit\n"), 0o644); err != nil {
		t.Fatalf("write agent config failed: %v", err)
	}
	t.Setenv(runtimeHomeEnv, avatarsHome)

	resolved := ResolveRuntimePath(filepath.Join("configs", "agent.yaml"))
	if resolved != filepath.Join(avatarsHome, "configs", "agent.yaml") {
		t.Fatalf("expected runtime home config path, got %q", resolved)
	}
}

func TestResolveRuntimeHome_PrefersAvatarsHome(t *testing.T) {
	tempDir := t.TempDir()
	avatarsHome := filepath.Join(tempDir, "avatars")
	if err := os.MkdirAll(avatarsHome, 0o755); err != nil {
		t.Fatalf("mkdir avatars home failed: %v", err)
	}
	t.Setenv(runtimeHomeEnv, avatarsHome)

	if got := ResolveRuntimeHome(); got != avatarsHome {
		t.Fatalf("expected runtime home %q, got %q", avatarsHome, got)
	}
}
