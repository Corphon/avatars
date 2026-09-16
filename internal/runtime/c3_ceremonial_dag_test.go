package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/events"
	"avatars/internal/llm"
	memstore "avatars/internal/memory"
	"avatars/internal/planner"
	"avatars/internal/registry"
	"avatars/internal/skillbuilder"
	"avatars/internal/skills"
	"avatars/internal/tools"
	"avatars/internal/transcript"
	"avatars/internal/verification"
)

func TestC3_EngineRunOmitsCeremonialDAGNodes(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() { _ = os.Chdir(originalWD) }()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Readme\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatal(err)
	}
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-c3")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = memoryStore.Close() }()
	skillStore := skills.NewStore(filepath.Join(tempDir, "skills"))
	task := "Analyze the current repository and propose a refactoring plan"
	proposal := skillbuilder.Build(task, planner.Build(task), "ok")
	generatedPath, err := skillStore.Generate("seed-run", "seed-task", proposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skillStore.Approve(generatedPath); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(toolRegistry, writer, events.NewStore(), "session-c3", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic"}), nil, skillStore).
		WithMemory(memoryStore).
		WithTaskWorkspace("c3-task", filepath.Join(tempDir, ".avatars", "tasks", "c3-task"))
	result, err := engine.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("engine run failed: %v", err)
	}
	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.Contains(line, `"type":"workflow.node_created"`) {
			continue
		}
		if strings.Contains(line, `"assigned_role":"Planner"`) || strings.Contains(line, `"assigned_role":"Synthesizer"`) {
			t.Fatalf("ceremonial DAG node created: %s", line)
		}
	}
	text := string(content)
	if !strings.Contains(text, `"type":"task.decomposed"`) {
		t.Fatal("missing Planner lifecycle task.decomposed")
	}
	if !strings.Contains(text, `"type":"synthesis.completed"`) || !strings.Contains(text, `"lifecycle":"tail_synthesis"`) {
		t.Fatal("missing Synthesizer tail lifecycle")
	}
}
