package app

import (
	"os"
	"path/filepath"
	"testing"

	"avatars/internal/runtime"
	"avatars/internal/verification"
)

func TestLoadPersonalityConfig_OperatorContextYAMLTag(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll("configs", 0755)
	body := `personality:
  style: concise
  language: zh-CN
  safety: standard
  operator_context: "Go backend on avatars"
`
	if err := os.WriteFile(filepath.Join("configs", "personality.yaml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadPersonalityConfig()
	if cfg.Language != "zh-CN" {
		t.Fatalf("language=%q", cfg.Language)
	}
	if cfg.Style != "concise" {
		t.Fatalf("style=%q", cfg.Style)
	}
	if cfg.OperatorContext != "Go backend on avatars" {
		t.Fatalf("operator_context not loaded (yaml tag?): %q", cfg.OperatorContext)
	}
}

func TestS7B_PersonalityInjectedWithoutAutoApprove(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("configs", 0755); err != nil {
		t.Fatal(err)
	}
	body := `personality:
  style: concise
  language: zh-CN
  operator_context: "7B personality"
`
	if err := os.WriteFile(filepath.Join("configs", "personality.yaml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("configs", "agent.yaml"), []byte("skills:\n  auto_approve_generated: false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewEngine(nil, nil, nil, "s7b", verification.DefaultPolicy(), nil, nil, nil)
	engine = applyEngineOperatorSettings(engine, "configs/agent.yaml")
	if engine.AutoApproveSkillsEnabled() {
		t.Fatal("auto_approve=false must not enable skill auto-approve")
	}
	if engine.Personality().Language != "zh-CN" {
		t.Fatalf("personality language=%q", engine.Personality().Language)
	}
}
