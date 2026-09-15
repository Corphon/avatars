package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/app"
	"avatars/internal/llm"
	"avatars/internal/skills"
)

// summarizeSkillsStatus lists approved/pending skills without launching a run. S6.3.
func summarizeSkillsStatus(root string) (string, error) {
	store := skills.NewStore(filepath.Join(root, ".avatars", "skills"))
	approved, _ := store.ListApproved()
	pending, _ := store.ListGenerated()
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Skills: %d approved, %d pending approval\n", len(approved), len(pending)))
	limit := 8
	for i, s := range approved {
		if i >= limit {
			b.WriteString(fmt.Sprintf("… and %d more approved\n", len(approved)-limit))
			break
		}
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = filepath.Base(s.Path)
		}
		b.WriteString("- " + name + "\n")
	}
	if len(approved) == 0 && len(pending) == 0 {
		b.WriteString("No skills yet. Generate with: avatars skills generate \"…\"\n")
	}
	return strings.TrimSpace(b.String()), nil
}

// summarizeMemoryPitfalls answers “踩过哪些坑” via workflow/memory docs. S6.3.
func summarizeMemoryPitfalls(root string) (string, error) {
	for _, rel := range []string{
		filepath.Join("docs", "workflow", "project_lessons.md"),
		filepath.Join(".avatars", "memory", "project_lessons.md"),
		filepath.Join("docs", "workflow", "process_record.md"),
	} {
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		lines := strings.Split(text, "\n")
		if len(lines) > 40 {
			lines = lines[:40]
			return strings.Join(lines, "\n") + "\n… (truncated)", nil
		}
		return text, nil
	}
	return "No recorded pitfalls/lessons found yet. After runs with features.memory=true, lessons land under docs/workflow or .avatars/memory.", nil
}

// summarizeLLMConfig surfaces effective LLM defaults without keyword routing. S6.3.
func summarizeLLMConfig(root string) (string, error) {
	_ = root
	_ = app.ResolveRuntimePath("configs/agent.yaml")
	maxTok := llm.DefaultMaxTokensForCode()
	fb := llm.GlobalFallbackProviders
	shell := llm.TimeoutConfigOrDefault().ShellDone
	var b strings.Builder
	b.WriteString(fmt.Sprintf("LLM defaults (effective):\n- max_tokens (code): %d\n", maxTok))
	if len(fb) == 0 {
		b.WriteString("- fallback_providers: (none configured — primary failure returns error)\n")
	} else {
		b.WriteString("- fallback_providers: " + strings.Join(fb, ", ") + "\n")
	}
	b.WriteString(fmt.Sprintf("- shell_done timeout: %s\n", shell))
	b.WriteString("- Genkit ToolCalls: rebuilt from conversation history when tools run (S5.1)\n")
	b.WriteString("Edit configs/agent.yaml llm_defaults to change these.\n")
	return strings.TrimSpace(b.String()), nil
}
