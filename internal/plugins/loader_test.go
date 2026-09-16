package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLocalPluginsFromMarketplace_ReadsStandardizedSkills(t *testing.T) {
	tempDir := t.TempDir()
	marketplaceDir := filepath.Join(tempDir, ".agents", "plugins")
	pluginRoot := filepath.Join(tempDir, "caveman", "plugins", "caveman")
	if err := os.MkdirAll(filepath.Join(pluginRoot, ".codex-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir plugin manifest dir failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(pluginRoot, "skills", "caveman"), 0o755); err != nil {
		t.Fatalf("mkdir plugin skill dir failed: %v", err)
	}
	marketplaceJSON := []byte(`{
  "name": "my-project-marketplace",
  "plugins": [
    {
      "name": "caveman",
      "source": { "source": "local", "path": "../../caveman/plugins/caveman" },
      "policy": { "installation": "AVAILABLE" },
      "category": "Productivity"
    }
  ]
}`)
	manifestJSON := []byte(`{
  "name": "caveman",
  "version": "0.1.0",
  "description": "Ultra-compressed communication mode.",
  "skills": "./skills/"
}`)
	skillMarkdown := []byte(`---
name: caveman
description: ultra compressed mode
when_to_use: use when terse output is required
---
Say less.
`)
	if err := os.MkdirAll(marketplaceDir, 0o755); err != nil {
		t.Fatalf("mkdir marketplace dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, "marketplace.json"), marketplaceJSON, 0o644); err != nil {
		t.Fatalf("write marketplace failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"), manifestJSON, 0o644); err != nil {
		t.Fatalf("write manifest failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "skills", "caveman", "SKILL.md"), skillMarkdown, 0o644); err != nil {
		t.Fatalf("write skill failed: %v", err)
	}

	result, err := LoadLocalPluginsFromMarketplace(filepath.Join(marketplaceDir, "marketplace.json"))
	if err != nil {
		t.Fatalf("load local plugins failed: %v", err)
	}
	if len(result.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(result.Plugins))
	}
	if len(result.Plugins[0].Skills) != 1 {
		t.Fatalf("expected 1 plugin skill, got %d", len(result.Plugins[0].Skills))
	}
	if result.Plugins[0].Skills[0].Name != "caveman:caveman" {
		t.Fatalf("expected namespaced plugin skill name, got %q", result.Plugins[0].Skills[0].Name)
	}
	if !strings.Contains(result.Plugins[0].Skills[0].WhenToUse, "terse output") {
		t.Fatalf("expected skill metadata to be read, got %q", result.Plugins[0].Skills[0].WhenToUse)
	}
}
