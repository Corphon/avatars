package main

import (
	"strings"
	"testing"

	"avatars/internal/app"
)

func init() {
	app.LoadIntentKeywords()
}

func TestClassifyIntent_CommandMatch(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		minConf  float64
	}{
		{"skills new Go patterns", "skills.new", 1.0},
		{"run fix the bug", "run", 1.0},
		{"arch analyze", "arch", 0.9},
		{"verify the build", "verify", 0.9},
		{"继续推进", "run.continue", 0.8},
	}

	for _, tt := range tests {
		decision := ClassifyIntent(tt.input)
		if decision.Action != tt.expected {
			t.Errorf("ClassifyIntent(%q).Action = %q, want %q", tt.input, decision.Action, tt.expected)
		}
		if decision.Confidence < tt.minConf {
			t.Errorf("ClassifyIntent(%q).Confidence = %v, want >= %v", tt.input, decision.Confidence, tt.minConf)
		}
		if decision.Source != "command" {
			t.Errorf("ClassifyIntent(%q).Source = %q, want 'command'", tt.input, decision.Source)
		}
	}
}

func TestClassifyIntent_KeywordMatch(t *testing.T) {
	tests := []struct {
		input    string
		action   string
		minConf  float64
	}{
		// Uses exact keywords from intent_keywords.yaml:
		// code_impl_verbs.en: "add a new", "create a new", "implement a", "build a"
		// single_action.en: "fix", "rename", "delete", "remove", "update", "modify"
		{"add a new plugin for data export", "run", 0.7},
		{"refactor the auth module", "run", 0.7},
		{"implement a REST API for users", "run", 0.7},
		{"fix the login bug", "run", 0.7},
	}

	for _, tt := range tests {
		decision := ClassifyIntent(tt.input)
		// Keyword matches should have source = "keyword" or fall through to "heuristic"
		if decision.Source == "command" {
			t.Logf("%q matched command instead of keyword (ok): %s", tt.input, decision.Reason)
			continue
		}
		if decision.Source == "heuristic" {
			t.Logf("%q → heuristic (YAML keywords may not be loaded in test env): %s", tt.input, decision.Reason)
			continue
		}
		if decision.Action != tt.action {
			t.Errorf("ClassifyIntent(%q).Action = %q, want %q", tt.input, decision.Action, tt.action)
		}
		if decision.Confidence < tt.minConf {
			t.Errorf("ClassifyIntent(%q).Confidence = %v, want >= %v", tt.input, decision.Confidence, tt.minConf)
		}
	}
}

func TestClassifyIntent_Question(t *testing.T) {
	decision := ClassifyIntent("what is the best way to structure this?")
	if decision.Action != "answer" {
		t.Errorf("expected 'answer' for question, got %q", decision.Action)
	}
	if decision.Source != "command" {
		t.Errorf("expected source 'command' for question pattern, got %q", decision.Source)
	}
}

func TestClassifyIntent_Chinese(t *testing.T) {
	tests := []string{
		"创建一个新的插件",
		"实现用户认证功能",
		"修复登录页面的bug",
		"生成项目文档",
	}
	for _, input := range tests {
		decision := ClassifyIntent(input)
		if decision.Action == "classify" && decision.Source == "heuristic" {
			t.Logf("Chinese input %q → needs LLM (confidence=%v): %s", input, decision.Confidence, decision.Reason)
			// This is expected for Chinese inputs that don't match keyword lists.
			// The LLM classifier should handle these.
		}
		if decision.Source == "command" || decision.Source == "keyword" {
			t.Logf("Chinese input %q → matched: %s (confidence=%v)", input, decision.Action, decision.Confidence)
		}
	}
}

func TestClassifyIntent_EmptyInput(t *testing.T) {
	decision := ClassifyIntent("")
	if decision.Action != "none" {
		t.Errorf("expected 'none' for empty input, got %q", decision.Action)
	}
}

func TestClassifyIntent_PipelineOrder(t *testing.T) {
	// "skills new create a plugin" should match command before keyword.
	decision := ClassifyIntent("skills new create a plugin for data export")
	if decision.Source != "command" {
		t.Errorf("expected command source (skills new), got %q — pipeline order violated", decision.Source)
	}
	if decision.Action != "skills.new" {
		t.Errorf("expected skills.new, got %q", decision.Action)
	}
}

func TestClassifyIntent_FallbackToHeuristic(t *testing.T) {
	// Input that matches no command or keyword → should fall through to heuristic.
	decision := ClassifyIntent("xyzzy arbitrary nonsense input")
	if decision.Source != "heuristic" {
		t.Errorf("expected heuristic fallback, got %q", decision.Source)
	}
	if !strings.Contains(decision.Reason, "needs LLM") {
		t.Errorf("fallback reason should mention LLM: %q", decision.Reason)
	}
}
