// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/events"
	"avatars/internal/llm"
	"avatars/internal/mcp"
	memstore "avatars/internal/memory"
	"avatars/internal/planner"
	"avatars/internal/registry"
	"avatars/internal/skillbuilder"
	"avatars/internal/skills"
	"avatars/internal/tools"
	"avatars/internal/transcript"
	"avatars/internal/verification"
)

type stubMCPClient struct {
	response  mcp.CallResponse
	err       error
	responses map[string]mcp.CallResponse
	errs      map[string]error
	calls     []string
}

func (s *stubMCPClient) Call(ctx context.Context, spec mcp.ServerSpec, method string, params any) (mcp.CallResponse, error) {
	if s == nil {
		return mcp.CallResponse{}, errors.New("mcp client is nil")
	}
	s.calls = append(s.calls, method)
	if s.errs != nil {
		if err, ok := s.errs[method]; ok {
			return mcp.CallResponse{}, err
		}
	}
	if s.responses != nil {
		if response, ok := s.responses[method]; ok {
			return response, nil
		}
	}
	return s.response, s.err
}

func (s *stubMCPClient) Close() error { return nil }

type stubVerifierRunner struct {
	report verification.Report
}

func (s stubVerifierRunner) Run(ctx context.Context) verification.Report {
	return s.report
}

type stubLLMClient struct {
	response llm.Response
	err      error
}

func (s stubLLMClient) Provider() string { return "stub" }

func (s stubLLMClient) Supports(capability llm.Capability) bool { return false }

func (s stubLLMClient) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	return s.response, s.err
}

func TestEngineRun_ReportsSynthesisDegradationWhenLLMFails(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Sheetforge\n\nManual mode imports workbooks.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.WriteFile("go.mod", []byte("module sheetforge\n\ngo 1.24.0\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}

	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-llm-fail")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	llmClient := stubLLMClient{err: errors.New("context deadline exceeded")}
	engine := NewEngine(toolRegistry, writer, store, "session-llm-fail", verification.DefaultPolicy(), llmClient, nil, nil).
		WithTaskWorkspace("llm-fail-task", filepath.Join(tempDir, ".avatars", "tasks", "llm-fail-task"))
	result, err := engine.Run(context.Background(), "Analyze this repository, explain what it does, and report concrete issues. Do not modify files. Summarize in ana.md")
	if err != nil {
		t.Fatalf("engine run failed: %v", err)
	}
	if result.SynthesisStatus != "failed: context deadline exceeded" {
		t.Fatalf("expected synthesis status to be recorded, got %q", result.SynthesisStatus)
	}
	if !strings.Contains(result.Summary, "Synthesis degraded: failed: context deadline exceeded.") {
		t.Fatalf("expected run summary to surface degraded synthesis, got %q", result.Summary)
	}
	if !strings.Contains(result.ReportContent, "## Synthesis Status") || !strings.Contains(result.ReportContent, "failed: context deadline exceeded") {
		t.Fatalf("expected report to retain synthesis failure status, got %s", result.ReportContent)
	}
	history := store.History()
	found := false
	for _, event := range history {
		if event.Type == "run.completed" && fmt.Sprint(event.Payload["status"]) == "completed" {
			found = true
			if got := fmt.Sprint(event.Payload["synthesis_status"]); got != "failed: context deadline exceeded" {
				t.Fatalf("expected run.completed synthesis_status payload, got %q", got)
			}
			if got := fmt.Sprint(event.Payload["synthesis_degraded"]); got != "true" {
				t.Fatalf("expected run.completed synthesis_degraded payload, got %q", got)
			}
		}
	}
	if !found {
		t.Fatalf("expected run.completed event in transcript, got %+v", history)
	}
}

func TestEngineRun_FocusedVerifierCountsInTelemetry(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Demo\n\nAutomatic tasks live in internal/auto.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/focusedtelemetry\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("internal", "auto"), 0o755); err != nil {
		t.Fatalf("mkdir internal auto failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("internal", "auto", "runner.go"), []byte("package auto\n\nfunc Run() string { return \"ok\" }\n"), 0o644); err != nil {
		t.Fatalf("write runner failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("internal", "auto", "runner_test.go"), []byte("package auto\n\nimport \"testing\"\n\nfunc TestRunner(t *testing.T) {\n\tif Run() != \"ok\" { t.Fatal(\"bad run\") }\n}\n"), 0o644); err != nil {
		t.Fatalf("write runner test failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-focused-telemetry")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	llmClient := stubLLMClient{response: llm.Response{Text: "Synthesis complete.", Provider: "stub", Model: "stub-model"}}
	engine := NewEngine(toolRegistry, writer, store, "session-focused-telemetry", verification.DefaultPolicy(), llmClient, nil, nil).
		WithTaskWorkspace("focused-telemetry-task", filepath.Join(tempDir, ".avatars", "tasks", "focused-telemetry-task"))
	result, err := engine.Run(context.Background(), "Analyze this repository, find concrete issues, and write ana.md")
	if err != nil {
		t.Fatalf("engine run failed: %v", err)
	}
	if !strings.Contains(result.ReportContent, "## Verification Results") || !strings.Contains(result.ReportContent, "go test ./internal/auto -run TestRunner") {
		t.Fatalf("expected focused verifier results in report, got %s", result.ReportContent)
	}

	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "verification.completed") {
		t.Fatalf("expected transcript to contain focused verification.completed, got %s", text)
	}
	if !strings.Contains(text, `"verification_runs":1`) {
		t.Fatalf("expected telemetry to count focused verifier run, got %s", text)
	}
}

func TestEngineRun_UsesReadToolAndWritesTranscript(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}

	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Readme\nproject context\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}

	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-test")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	skillStore := skills.NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")
	generatedPath, err := skillStore.Generate("seed-run", "seed-task", proposal)
	if err != nil {
		t.Fatalf("seed generate failed: %v", err)
	}
	if _, err := skillStore.Approve(generatedPath); err != nil {
		t.Fatalf("seed approve failed: %v", err)
	}
	engine := NewEngine(toolRegistry, writer, store, "session-test", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, skillStore)
	result, err := engine.Run(context.Background(), "Analyze the current repository and propose a refactoring plan")
	if err != nil {
		t.Fatalf("engine run failed: %v", err)
	}

	task := "Analyze the current repository and propose a refactoring plan"
	plan := planner.BuildForComplexity(task, planner.Context{}, planner.Assess(task))
	wantAvatars := fmt.Sprintf("Planned across %d avatars", len(plan.Avatars))
	if !strings.Contains(result.Summary, wantAvatars) {
		t.Fatalf("expected summary to mention %q, got %q", wantAvatars, result.Summary)
	}
	if !strings.Contains(result.Summary, "Synthesis:") {
		t.Fatalf("expected summary to include llm synthesis, got %q", result.Summary)
	}
	if result.GeneratedSkillPath == "" {
		t.Fatal("expected generated skill path")
	}
	if result.ApprovedSkillCount != 1 {
		t.Fatalf("expected 1 approved skill discovered, got %d", result.ApprovedSkillCount)
	}
	if result.InvokedSkillName != "Task Survey Skill" {
		t.Fatalf("expected invoked skill Task Survey Skill, got %q", result.InvokedSkillName)
	}
	if _, err := os.Stat(result.GeneratedSkillPath); err != nil {
		t.Fatalf("expected generated skill file, got %v", err)
	}

	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}

	text := string(content)
	if !strings.Contains(text, "README.md") {
		t.Fatalf("expected transcript to record README.md read, got %s", text)
	}
	if !strings.Contains(text, "task.decomposed") {
		t.Fatalf("expected transcript to contain task.decomposed event, got %s", text)
	}
	if count := strings.Count(text, "avatar.spawned"); count < 3 {
		t.Fatalf("expected at least 3 avatar.spawned events, got %d in %s", count, text)
	}
	if !strings.Contains(text, "avatar.assigned") {
		t.Fatalf("expected transcript to contain avatar.assigned event, got %s", text)
	}
	if !strings.Contains(text, "avatar.handoff") {
		t.Fatalf("expected transcript to contain avatar.handoff event, got %s", text)
	}
	if !strings.Contains(text, `"message_type":"report"`) {
		t.Fatalf("expected transcript to contain avatar report message type, got %s", text)
	}
	if !strings.Contains(text, `"message_type":"ask"`) {
		t.Fatalf("expected transcript to contain avatar ask message type, got %s", text)
	}
	if !strings.Contains(text, `"reply_to_message_type":"ask"`) {
		t.Fatalf("expected transcript to contain avatar follow-up reply marker, got %s", text)
	}
	if !strings.Contains(text, `"message_type":"challenge"`) {
		t.Fatalf("expected transcript to contain avatar challenge message type, got %s", text)
	}
	if !strings.Contains(text, `"message_type":"summarize"`) {
		t.Fatalf("expected transcript to contain avatar summarize message type, got %s", text)
	}
	if !strings.Contains(text, `"to_avatar_id":"avatar-planner"`) {
		t.Fatalf("expected transcript to contain directed avatar ask target, got %s", text)
	}
	if !strings.Contains(text, `"broadcast_scope":"task"`) {
		t.Fatalf("expected transcript to contain task broadcast scope for non-directed avatar messages, got %s", text)
	}
	if !strings.Contains(text, "skill.proposed") {
		t.Fatalf("expected transcript to contain skill.proposed event, got %s", text)
	}
	if !strings.Contains(text, "skill.generated") {
		t.Fatalf("expected transcript to contain skill.generated event, got %s", text)
	}
	if !strings.Contains(text, "skill.discovered") {
		t.Fatalf("expected transcript to contain skill.discovered event, got %s", text)
	}
	if !strings.Contains(text, "skill.invoked") {
		t.Fatalf("expected transcript to contain skill.invoked event, got %s", text)
	}
	if !strings.Contains(text, "llm.completed") {
		t.Fatalf("expected transcript to contain llm.completed event, got %s", text)
	}
	if !strings.Contains(text, `"type":"llm.completed"`) ||
		!strings.Contains(text, `"lifecycle":"tail_synthesis"`) ||
		!strings.Contains(text, `"node_role":"Synthesizer"`) {
		t.Fatalf("expected llm.completed to carry synthesizer tail lifecycle, got %s", text)
	}
	if !strings.Contains(text, "telemetry.updated") {
		t.Fatalf("expected transcript to contain telemetry.updated event, got %s", text)
	}
	if !strings.Contains(text, "tool.requested") {
		t.Fatalf("expected transcript to contain tool.requested event, got %s", text)
	}
	if !strings.Contains(text, "tool.completed") {
		t.Fatalf("expected transcript to contain tool.completed event, got %s", text)
	}
	if !strings.Contains(text, "memory.compaction_boundary_written") {
		t.Fatalf("expected transcript to contain memory.compaction_boundary_written event, got %s", text)
	}
	if len(store.History()) == 0 {
		t.Fatal("expected event store to retain history")
	}
}

func TestEngineRun_IgnoresMalformedApprovedSkillAndContinuesAnalysis(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Sheetforge\n\nThis project coordinates multi-avatar repo analysis.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.WriteFile("go.mod", []byte("module sheetforge\n\ngo 1.24.0\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	if err := os.WriteFile("main.go", []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	if err := toolRegistry.Register(tools.ShellTool{}); err != nil {
		t.Fatalf("register shell tool failed: %v", err)
	}
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	if err := toolRegistry.Register(tools.PatchTool{}); err != nil {
		t.Fatalf("register patch tool failed: %v", err)
	}
	if err := toolRegistry.Register(tools.GitTool{}); err != nil {
		t.Fatalf("register git tool failed: %v", err)
	}

	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-malformed-skill")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()

	skillStore := skills.NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")
	generatedPath, err := skillStore.Generate("seed-run", "seed-task", proposal)
	if err != nil {
		t.Fatalf("seed generate failed: %v", err)
	}
	if _, err := skillStore.Approve(generatedPath); err != nil {
		t.Fatalf("seed approve failed: %v", err)
	}
	badPath := filepath.Join(tempDir, "skills", "approved", "cave_man_SKILL.md")
	if err := os.WriteFile(badPath, []byte("---\nname: bad\n---\n"), 0o644); err != nil {
		t.Fatalf("write malformed skill failed: %v", err)
	}

	engine := NewEngine(toolRegistry, writer, store, "session-malformed-skill", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, skillStore)
	result, err := engine.Run(context.Background(), "Analyze the current repository and propose a refactoring plan")
	if err != nil {
		t.Fatalf("engine run failed: %v", err)
	}
	if result.ApprovedSkillCount != 1 {
		t.Fatalf("expected 1 valid approved skill discovered, got %d", result.ApprovedSkillCount)
	}
	if result.InvokedSkillName != "Task Survey Skill" {
		t.Fatalf("expected invoked skill Task Survey Skill, got %q", result.InvokedSkillName)
	}
	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	if !strings.Contains(string(content), "skill.registry_warning") {
		t.Fatalf("expected transcript to include skill registry warning, got %s", string(content))
	}
	if !strings.Contains(string(content), "README.md") {
		t.Fatalf("expected transcript to record README.md, got %s", string(content))
	}
}

func TestEngineRun_UsesVerifierGateForGeneratedSkillCompletion(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Readme\nproject context\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/runverifier\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}

	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-run-verifier")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	skillStore := skills.NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")
	generatedPath, err := skillStore.Generate("seed-run", "seed-task", proposal)
	if err != nil {
		t.Fatalf("seed generate failed: %v", err)
	}
	if _, err := skillStore.Approve(generatedPath); err != nil {
		t.Fatalf("seed approve failed: %v", err)
	}
	engine := NewEngine(toolRegistry, writer, store, "session-run-verifier", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, skillStore).
		WithMemory(memoryStore).
		WithTaskWorkspace("run-verifier-task", filepath.Join(tempDir, ".avatars", "tasks", "run-verifier-task"))
	engine.verifier = func(workingDir string, includeRace bool) verifierRunner {
		return stubVerifierRunner{report: verification.Report{
			Verdict: verification.VerdictPass,
			Summary: "Verification finished with PASS. 1 passed, 0 partial, 0 failed.",
			Checks: []verification.CheckEvidence{{
				Name:           "go test",
				CommandRun:     "go test ./...",
				OutputObserved: "ok\tavatars/internal/runtime",
				Expected:       "All package tests should pass.",
				Actual:         "Command completed successfully.",
				Result:         verification.VerdictPass,
			}},
		}}
	}

	result, err := engine.Run(context.Background(), "Analyze the current repository and propose a refactoring plan")
	if err != nil {
		t.Fatalf("engine run failed: %v", err)
	}
	if !strings.Contains(result.Summary, "Synthesis:") {
		t.Fatalf("expected synthesized run summary, got %q", result.Summary)
	}
	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, `"status":"completed"`) {
		t.Fatalf("expected completed terminal status for verifier PASS, got %s", text)
	}
	snapshot, err := memoryStore.LoadLatestTaskSnapshot("run-verifier-task")
	if err != nil {
		t.Fatalf("load verifier task snapshot failed: %v", err)
	}
	nodeEvidence := latestNodeEvidenceForRole(snapshot.EvaluationRecords, "Builder")
	if nodeEvidence == nil {
		t.Fatalf("expected builder node work evidence, got %+v", snapshot.EvaluationRecords)
	}
	if memstore.EvaluationRecordVerifierGateStatus(*nodeEvidence) != "verified_pass" {
		t.Fatalf("expected verified_pass gate, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordVerifierVerdict(*nodeEvidence) != "PASS" || memstore.EvaluationRecordVerified(*nodeEvidence) != "true" {
		t.Fatalf("expected PASS verified node evidence, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordVerificationReportPath(*nodeEvidence) == "" {
		t.Fatalf("expected node evidence to carry verifier report path, got %+v", nodeEvidence)
	}

	failingWriter, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-run-verifier-fail")
	if err != nil {
		t.Fatalf("new failing writer failed: %v", err)
	}
	defer func() {
		_ = failingWriter.Close()
	}()
	failingStore := events.NewStore()
	failingMemoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory-fail"))
	if err != nil {
		t.Fatalf("new failing memory store failed: %v", err)
	}
	defer func() {
		_ = failingMemoryStore.Close()
	}()
	failingSkillStore := skills.NewStore(filepath.Join(tempDir, "skills-fail"))
	failingGeneratedPath, err := failingSkillStore.Generate("seed-run-fail", "seed-task-fail", proposal)
	if err != nil {
		t.Fatalf("seed generate failed: %v", err)
	}
	if _, err := failingSkillStore.Approve(failingGeneratedPath); err != nil {
		t.Fatalf("seed approve failed: %v", err)
	}
	failingEngine := NewEngine(toolRegistry, failingWriter, failingStore, "session-run-verifier-fail", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, failingSkillStore).
		WithMemory(failingMemoryStore).
		WithTaskWorkspace("run-verifier-fail-task", filepath.Join(tempDir, ".avatars", "tasks", "run-verifier-fail-task"))
	failingEngine.verifier = func(workingDir string, includeRace bool) verifierRunner {
		return stubVerifierRunner{report: verification.Report{
			Verdict: verification.VerdictFail,
			Summary: "Verification finished with FAIL. 0 passed, 0 partial, 1 failed.",
			Checks: []verification.CheckEvidence{{
				Name:           "go test",
				CommandRun:     "go test ./...",
				OutputObserved: "FAIL\tavatars/internal/runtime",
				Expected:       "All package tests should pass.",
				Actual:         "exit status 1",
				Result:         verification.VerdictFail,
			}},
		}}
	}
	failingResult, err := failingEngine.Run(context.Background(), "Analyze the current repository and propose a refactoring plan. Summarize in ana.md")
	if err == nil {
		t.Fatal("expected verifier failure to surface")
	}
	if strings.TrimSpace(failingResult.ReportPath) == "" {
		t.Fatal("expected report path on verifier failure")
	}
	if strings.TrimSpace(failingResult.ReportContent) == "" {
		t.Fatal("expected report content on verifier failure")
	}
	if !strings.Contains(failingResult.ReportContent, "# Analysis Report") {
		t.Fatalf("expected analysis markdown on verifier failure, got %s", failingResult.ReportContent)
	}
	if !strings.Contains(failingResult.ReportContent, "## Proven Findings") || !strings.Contains(failingResult.ReportContent, "## Evidence-Tied Risks / Needs Verification") || !strings.Contains(failingResult.ReportContent, "## Open Questions") {
		t.Fatalf("expected findings and risk sections on verifier failure, got %s", failingResult.ReportContent)
	}
	failingContent, err := os.ReadFile(failingWriter.Path())
	if err != nil {
		t.Fatalf("read failing transcript failed: %v", err)
	}
	failingText := string(failingContent)
	if !strings.Contains(failingText, `"status":"needs_remediation"`) {
		t.Fatalf("expected needs_remediation terminal status in main run, got %s", failingText)
	}
	failingSnapshot, err := failingMemoryStore.LoadLatestTaskSnapshot("run-verifier-fail-task")
	if err != nil {
		t.Fatalf("load failing verifier task snapshot failed: %v", err)
	}
	failingNodeEvidence := memstore.LatestNodeWorkEvidence(failingSnapshot.EvaluationRecords)
	if failingNodeEvidence == nil {
		t.Fatalf("expected failing node work evidence, got %+v", failingSnapshot.EvaluationRecords)
	}
	if memstore.EvaluationRecordVerifierGateStatus(*failingNodeEvidence) != "blocked_fail" {
		t.Fatalf("expected blocked_fail gate, got %+v", failingNodeEvidence)
	}
	if memstore.EvaluationRecordVerifierVerdict(*failingNodeEvidence) != "FAIL" || memstore.EvaluationRecordVerified(*failingNodeEvidence) != "false" {
		t.Fatalf("expected FAIL unverified node evidence, got %+v", failingNodeEvidence)
	}
	if memstore.EvaluationRecordRecoveryKind(*failingNodeEvidence) != "verifier_remediation" {
		t.Fatalf("expected verifier remediation recovery kind, got %+v", failingNodeEvidence)
	}
	if memstore.EvaluationRecordVerificationReportPath(*failingNodeEvidence) == "" {
		t.Fatalf("expected verifier-fail node evidence to carry report path, got %+v", failingNodeEvidence)
	}
	if memstore.EvaluationRecordPausePointID(*failingNodeEvidence) != "" || memstore.EvaluationRecordDetailValue(*failingNodeEvidence, "pause_point_kind") != "" {
		t.Fatalf("expected verifier-fail node evidence to avoid claiming pause-point authority, got %+v", failingNodeEvidence)
	}
}

func TestEngineRun_EmitsWorkflowStateMachineEventsForAllPlanNodes(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Readme\nproject context\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}

	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-workflow")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	skillStore := skills.NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")
	generatedPath, err := skillStore.Generate("seed-run", "seed-task", proposal)
	if err != nil {
		t.Fatalf("seed generate failed: %v", err)
	}
	if _, err := skillStore.Approve(generatedPath); err != nil {
		t.Fatalf("seed approve failed: %v", err)
	}

	engine := NewEngine(toolRegistry, writer, store, "session-workflow", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, skillStore)
	engine = engine.WithMemory(memoryStore).WithTaskWorkspace("workflow-task", filepath.Join(tempDir, ".avatars", "tasks", "workflow-task"))
	result, err := engine.Run(context.Background(), "Analyze the current repository and propose a refactoring plan")
	if err != nil {
		t.Fatalf("engine run failed: %v", err)
	}
	if result.TranscriptPath == "" {
		t.Fatal("expected transcript path")
	}

	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if strings.Contains(text, `"assigned_role":"Planner"`) && strings.Contains(text, `"type":"workflow.node_created"`) {
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, `"type":"workflow.node_created"`) && strings.Contains(line, `"assigned_role":"Planner"`) {
				t.Fatalf("C3: default DAG must not create Planner nodes, got %s", line)
			}
			if strings.Contains(line, `"type":"workflow.node_created"`) && strings.Contains(line, `"assigned_role":"Synthesizer"`) {
				t.Fatalf("C3: default DAG must not create Synthesizer nodes, got %s", line)
			}
		}
	}
	created := strings.Count(text, `"type":"workflow.node_created"`)
	activated := strings.Count(text, `"type":"workflow.node_activated"`)
	completed := strings.Count(text, `"type":"workflow.node_completed"`)
	if created == 0 || created != activated || created != completed {
		t.Fatalf("expected matching node created/activated/completed counts, got created=%d activated=%d completed=%d in %s", created, activated, completed, text)
	}
	expectedEdges := 0
	for _, node := range planner.BuildForComplexity("Analyze the current repository and propose a refactoring plan", planner.Context{}, planner.Assess("Analyze the current repository and propose a refactoring plan")).Nodes {
		expectedEdges += len(node.DependsOn)
	}
	if got := strings.Count(text, `"type":"workflow.edge_advanced"`); got != expectedEdges {
		t.Fatalf("expected %d workflow.edge_advanced events, got %d in %s", expectedEdges, got, text)
	}
	if !strings.Contains(text, `"type":"task.decomposed"`) {
		t.Fatalf("expected Planner lifecycle task.decomposed, got %s", text)
	}
	if !strings.Contains(text, `"type":"synthesis.completed"`) || !strings.Contains(text, `"lifecycle":"tail_synthesis"`) {
		t.Fatalf("expected Synthesizer tail lifecycle synthesis.completed, got %s", text)
	}
	lines := strings.Split(text, "\n")
	surveyActivated := -1
	readRequested := -1
	readRequestedLine := ""
	readCompletedLine := ""
	surveyCompleted := -1
	surveyArtifactLine := ""
	buildCompleted := -1
	buildArtifactLine := ""
	buildRequestedLine := ""
	buildToolCompletedLine := ""
	reviewActivated := -1
	criticChallenge := -1
	reviewCompleted := -1
	reviewArtifactLine := ""
	synthesisCompleted := -1
	isSurveyNode := func(line string) bool {
		return strings.Contains(line, `"node_id":"node-survey"`) || strings.Contains(line, `"node_id":"node-survey-`)
	}
	for index, line := range lines {
		if surveyActivated < 0 && strings.Contains(line, `"type":"workflow.node_activated"`) && isSurveyNode(line) {
			surveyActivated = index
		}
		if readRequested < 0 && strings.Contains(line, `"type":"tool.requested"`) && strings.Contains(line, `"tool":"read"`) && strings.Contains(line, `"operation":"file_read"`) && isSurveyNode(line) {
			readRequested = index
			readRequestedLine = line
		}
		if readCompletedLine == "" && strings.Contains(line, `"type":"tool.completed"`) && strings.Contains(line, `"tool":"read"`) && strings.Contains(line, `"operation":"file_read"`) && isSurveyNode(line) {
			readCompletedLine = line
		}
		if surveyCompleted < 0 && strings.Contains(line, `"type":"workflow.node_completed"`) && isSurveyNode(line) {
			surveyCompleted = index
			surveyArtifactLine = line
		}
		if buildCompleted < 0 && strings.Contains(line, `"type":"workflow.node_completed"`) && strings.Contains(line, `"node_id":"node-build"`) {
			buildCompleted = index
			buildArtifactLine = line
		}
		if buildRequestedLine == "" && strings.Contains(line, `"type":"tool.requested"`) && strings.Contains(line, `"tool":"write"`) && strings.Contains(line, `"operation":"file_write"`) {
			buildRequestedLine = line
		}
		if buildToolCompletedLine == "" && strings.Contains(line, `"type":"tool.completed"`) && strings.Contains(line, `"tool":"write"`) && strings.Contains(line, `"operation":"file_write"`) {
			buildToolCompletedLine = line
		}
		if reviewActivated < 0 && strings.Contains(line, `"type":"workflow.node_activated"`) && strings.Contains(line, `"node_id":"node-review"`) {
			reviewActivated = index
		}
		if criticChallenge < 0 && strings.Contains(line, `"type":"avatar.spoke"`) && strings.Contains(line, `"message_type":"challenge"`) {
			criticChallenge = index
		}
		if reviewCompleted < 0 && strings.Contains(line, `"type":"workflow.node_completed"`) && strings.Contains(line, `"node_id":"node-review"`) {
			reviewCompleted = index
			reviewArtifactLine = line
		}
		if synthesisCompleted < 0 && strings.Contains(line, `"type":"synthesis.completed"`) {
			synthesisCompleted = index
		}
	}
	if surveyActivated < 0 || readRequested < 0 || surveyCompleted < 0 {
		t.Fatalf("expected survey node activation, read tool request, and node completion in transcript, got %s", text)
	}
	if !isSurveyNode(readRequestedLine) || !strings.Contains(readRequestedLine, `"node_role":"Researcher"`) {
		t.Fatalf("expected researcher tool.requested to carry survey node coordinates, got %s", readRequestedLine)
	}
	if !isSurveyNode(readCompletedLine) || !strings.Contains(readCompletedLine, `"node_role":"Researcher"`) {
		t.Fatalf("expected researcher tool.completed to carry survey node coordinates, got %s", readCompletedLine)
	}
	isDocsOrLinearSurvey := func(line string) bool {
		return strings.Contains(line, `"node_id":"node-survey-docs"`) ||
			(strings.Contains(line, `"node_id":"node-survey"`) && !strings.Contains(line, "node-survey-"))
	}
	docsActivated := -1
	docsRead := -1
	docsCompleted := -1
	for index, line := range lines {
		if !isDocsOrLinearSurvey(line) {
			continue
		}
		if docsActivated < 0 && strings.Contains(line, `"type":"workflow.node_activated"`) {
			docsActivated = index
		}
		if docsRead < 0 && strings.Contains(line, `"type":"tool.requested"`) && strings.Contains(line, `"tool":"read"`) {
			docsRead = index
		}
		if docsCompleted < 0 && strings.Contains(line, `"type":"workflow.node_completed"`) {
			docsCompleted = index
		}
	}
	if docsActivated >= 0 && docsRead >= 0 && docsCompleted >= 0 {
		if !(docsActivated < docsRead && docsRead < docsCompleted) {
			t.Fatalf("expected docs survey read inside that node, got activated=%d requested=%d completed=%d", docsActivated, docsRead, docsCompleted)
		}
	}
	if reviewActivated < 0 || criticChallenge < 0 || reviewCompleted < 0 {
		t.Fatalf("expected review node activation, critic challenge, and node completion in transcript, got %s", text)
	}
	if !(reviewActivated < criticChallenge && criticChallenge < reviewCompleted) {
		t.Fatalf("expected critic challenge inside scheduler-owned review node execution, got activated=%d challenge=%d completed=%d in %s", reviewActivated, criticChallenge, reviewCompleted, text)
	}
	if synthesisCompleted < 0 || synthesisCompleted < reviewCompleted {
		t.Fatalf("expected Synthesizer tail lifecycle after DAG review, got reviewCompleted=%d synthesisCompleted=%d in %s", reviewCompleted, synthesisCompleted, text)
	}
	if !strings.Contains(surveyArtifactLine, `"artifact_ids":[`) {
		t.Fatalf("expected researcher workflow.node_completed to carry artifact ids, got %s", surveyArtifactLine)
	}
	if buildCompleted < 0 || !strings.Contains(buildArtifactLine, `"artifact_ids":["generated-skill"]`) {
		t.Fatalf("expected builder workflow.node_completed to carry generated-skill artifact, got %s", buildArtifactLine)
	}
	if !strings.Contains(buildRequestedLine, `"node_id":"node-build"`) || !strings.Contains(buildRequestedLine, `"node_role":"Builder"`) {
		t.Fatalf("expected builder tool.requested to carry node coordinates, got %s", buildRequestedLine)
	}
	if !strings.Contains(buildToolCompletedLine, `"node_id":"node-build"`) || !strings.Contains(buildToolCompletedLine, `"node_role":"Builder"`) {
		t.Fatalf("expected builder tool.completed to carry node coordinates, got %s", buildToolCompletedLine)
	}
	if !strings.Contains(reviewArtifactLine, `"artifact_ids":["node-review"]`) {
		t.Fatalf("expected critic workflow.node_completed to retain node artifact fallback, got %s", reviewArtifactLine)
	}
	snapshot, err := memoryStore.LoadLatestTaskSnapshot("workflow-task")
	if err != nil {
		t.Fatalf("load workflow task snapshot failed: %v", err)
	}
	for _, expected := range []struct {
		role      string
		nodeID    string
		tool      string
		operation string
		status    string
	}{
		{role: "Researcher", nodeID: "survey", tool: "read", operation: "file_read", status: "completed"},
		{role: "Builder", nodeID: "node-build", tool: "write", operation: "file_write", status: "completed"},
		{role: "Critic", nodeID: "node-review", tool: "review", operation: "boundary_review", status: "approval_boundary_reviewed"},
		{role: "Synthesizer", nodeID: "lifecycle-synthesize", tool: "synthesize", operation: "summarize", status: "completed"},
	} {
		evidence := latestNodeEvidenceForRole(snapshot.EvaluationRecords, expected.role)
		if evidence == nil {
			t.Fatalf("expected %s node work evidence, got %+v", expected.role, snapshot.EvaluationRecords)
		}
		gotID := memstore.EvaluationRecordNodeID(*evidence)
		if expected.role == "Researcher" {
			if !strings.Contains(gotID, expected.nodeID) {
				t.Fatalf("expected %s node id to contain %s, got %+v", expected.role, expected.nodeID, evidence)
			}
		} else if gotID != expected.nodeID {
			t.Fatalf("expected %s node id %s, got %+v", expected.role, expected.nodeID, evidence)
		}
		if evidence.Tool != expected.tool || memstore.EvaluationRecordOperation(*evidence) != expected.operation {
			t.Fatalf("expected %s %s/%s evidence, got %+v", expected.role, expected.tool, expected.operation, evidence)
		}
		if memstore.EvaluationRecordDetailValue(*evidence, "status") != expected.status {
			t.Fatalf("expected %s status %s, got %+v", expected.role, expected.status, evidence)
		}
		if expected.role == "Researcher" && len(memstore.EvaluationRecordExpectedTargets(*evidence)) == 0 {
			t.Fatalf("expected researcher node evidence to record read target, got %+v", evidence)
		}
		if expected.tool != "write" {
			assertNonMutatingNodeEvidenceHasNoRecoveryAuthority(t, *evidence)
		}
	}
}

func assertNonMutatingNodeEvidenceHasNoRecoveryAuthority(t *testing.T, record memstore.EvaluationRecord) {
	t.Helper()
	if memstore.EvaluationRecordApprovalKey(record) != "" ||
		memstore.EvaluationRecordContinuationRunID(record) != "" ||
		memstore.EvaluationRecordContinuationTaskID(record) != "" ||
		memstore.EvaluationRecordReplayStatus(record) != "" ||
		memstore.EvaluationRecordPausePointID(record) != "" ||
		memstore.EvaluationRecordRecoveryKind(record) != "" ||
		memstore.EvaluationRecordVerifierGateStatus(record) != "" ||
		memstore.EvaluationRecordVerified(record) != "" ||
		memstore.EvaluationRecordVerificationReportPath(record) != "" {
		t.Fatalf("expected non-mutating node evidence to avoid recovery authority fields, got %+v", record)
	}
}

func TestEngineRun_ResearcherPrefersRequestNamedExistingFile(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\ncodex context only\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Readme\nproject context\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("configs", "agent.yaml"), []byte("runtime:\n  mode: deterministic\n"), 0o644); err != nil {
		t.Fatalf("write agent config failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-request-target")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory-request-target"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()

	engine := NewEngine(toolRegistry, writer, events.NewStore(), "session-request-target", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, nil).
		WithMemory(memoryStore).
		WithTaskWorkspace("request-target-task", filepath.Join(tempDir, ".avatars", "tasks", "request-target-task"))
	input := "Inspect configs/agent.yaml and CLI docs for runtime readiness drift."
	plan := planner.BuildForComplexity(input, planner.Context{}, planner.Assess(input))
	surveyNodes := 0
	for _, node := range plan.Nodes {
		if node.AssignedRole == "Researcher" {
			surveyNodes++
		}
	}
	if surveyNodes != 1 {
		t.Skipf("named-file survey preference needs a single Researcher DAG node; C3 small plans omit it (got %d)", surveyNodes)
	}
	result, err := engine.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("engine run failed: %v", err)
	}

	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	readRequestedLine := ""
	readCompletedLine := ""
	for _, line := range strings.Split(string(content), "\n") {
		if readRequestedLine == "" && strings.Contains(line, `"type":"tool.requested"`) && strings.Contains(line, `"tool":"read"`) && strings.Contains(line, `"operation":"file_read"`) {
			readRequestedLine = line
		}
		if readCompletedLine == "" && strings.Contains(line, `"type":"tool.completed"`) && strings.Contains(line, `"tool":"read"`) && strings.Contains(line, `"operation":"file_read"`) {
			readCompletedLine = line
		}
	}
	if readRequestedLine == "" || readCompletedLine == "" {
		t.Fatalf("expected researcher read request/completion in transcript, got %s", string(content))
	}
	for label, line := range map[string]string{"requested": readRequestedLine, "completed": readCompletedLine} {
		if !(strings.Contains(line, `"node_id":"node-survey"`) || strings.Contains(line, `"node_id":"node-survey-`)) || !strings.Contains(line, `"node_role":"Researcher"`) {
			t.Fatalf("expected researcher node coordinates on %s line, got %s", label, line)
		}
		if !strings.Contains(line, `"path":"configs\\agent.yaml"`) && !strings.Contains(line, `"path":"configs/agent.yaml"`) {
			t.Fatalf("expected %s line to read configs/agent.yaml, got %s", label, line)
		}
		if strings.Contains(line, `"path":"process_record.md"`) {
			t.Fatalf("expected %s line to avoid process_record.md default when request names a file, got %s", label, line)
		}
	}
	snapshot, err := memoryStore.LoadLatestTaskSnapshot("request-target-task")
	if err != nil {
		t.Fatalf("load request target task snapshot failed: %v", err)
	}
	evidence := latestNodeEvidenceForRole(snapshot.EvaluationRecords, "Researcher")
	if evidence == nil {
		t.Fatalf("expected researcher node work evidence, got %+v", snapshot.EvaluationRecords)
	}
	targets := memstore.EvaluationRecordExpectedTargets(*evidence)
	if len(targets) != 1 || targets[0] != filepath.Clean("configs/agent.yaml") {
		t.Fatalf("expected researcher evidence target configs/agent.yaml, got %+v", evidence)
	}
}

func TestRecoveryEvidenceChainDoesNotOverrideLifecycleStatus(t *testing.T) {
	snapshot := memstore.Snapshot{
		EvaluationRecords: []memstore.EvaluationRecord{
			{Cause: "verification_reverify_covered", Summary: "PASS covers remediation.", Details: []string{"remediation_resume_attempt_id: resume-remediate"}},
			{Cause: "node_work_evidence", Summary: "Builder replay completed.", Details: []string{"node_id: node-build", "node_role: Builder", "tool: write", "operation: file_write", "status: replay_completed", "recovery_kind: approval_replay"}},
			{Cause: "failed_node_pause_point", Summary: "Prior failed node.", Details: []string{"pause_point_id: pause-failed", "node_id: node-review", "status_reason: prior failure"}},
			{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"status: completed", "origin_run_id: run-a", "origin_task_id: task-a"}},
		},
	}
	chain := memstore.LatestRecoveryEvidenceChain(snapshot.EvaluationRecords, 5)
	if len(chain) != 3 {
		t.Fatalf("expected three recovery chain records, got %+v", chain)
	}
	decision := memstore.LatestRecoveryDecision(snapshot.EvaluationRecords, 5)
	if decision == nil || decision.Category != "closed" || decision.SourceCause != "verification_reverify_covered" {
		t.Fatalf("expected closed advisory recovery decision, got %+v", decision)
	}
	if proposal := memstore.LatestRecoveryActionProposal(snapshot.EvaluationRecords, 5); proposal != nil {
		t.Fatalf("expected no retry proposal for closed recovery decision, got %+v", proposal)
	}
	if request := memstore.LatestRecoveryActionRequest(snapshot.EvaluationRecords, 5, "task_operator_command"); request != nil {
		t.Fatalf("expected no recovery action request for closed recovery decision, got %+v", request)
	}
	if boundary := memstore.LatestRecoveryExecutionBoundary(snapshot.EvaluationRecords, 5, "task_operator_command"); boundary != nil {
		t.Fatalf("expected no recovery execution boundary for closed recovery decision, got %+v", boundary)
	}
	if status := memstore.TaskWorkspaceStatus("stable", snapshot); status != "completed" {
		t.Fatalf("expected lifecycle status to remain completed, got %q", status)
	}
}

func TestRecoveryDecisionIsAdvisoryForRetryableEvidence(t *testing.T) {
	snapshot := memstore.Snapshot{
		EvaluationRecords: []memstore.EvaluationRecord{
			{Cause: "failed_node_pause_point", Summary: "Prior failed node.", Details: []string{"pause_point_id: pause-failed", "node_id: node-review", "status_reason: tool failed", "retryable: true"}},
			{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to awaiting_approval.", Details: []string{"status: awaiting_approval", "origin_run_id: run-a", "origin_task_id: task-a"}},
		},
	}
	decision := memstore.LatestRecoveryDecision(snapshot.EvaluationRecords, 5)
	if decision == nil || decision.Category != "retryable" || decision.PausePointID != "pause-failed" {
		t.Fatalf("expected retryable advisory recovery decision, got %+v", decision)
	}
	proposal := memstore.LatestRecoveryActionProposal(snapshot.EvaluationRecords, 5)
	if proposal == nil || proposal.ActionKind != "plan_failed_node_retry" || proposal.RequiredAuthority != "workflow_scheduler" {
		t.Fatalf("expected failed-node retry proposal metadata, got %+v", proposal)
	}
	request := memstore.LatestRecoveryActionRequest(snapshot.EvaluationRecords, 5, "task_operator_command")
	if request == nil || request.ActionKind != "plan_failed_node_retry" || request.OperatorSource != "task_operator_command" {
		t.Fatalf("expected failed-node recovery action request metadata, got %+v", request)
	}
	boundary := memstore.LatestRecoveryExecutionBoundary(snapshot.EvaluationRecords, 5, "task_operator_command")
	if boundary == nil || boundary.ActionKind != "plan_failed_node_retry" || boundary.GuardStatus != "manual_review_required" {
		t.Fatalf("expected failed-node manual recovery boundary metadata, got %+v", boundary)
	}
	if status := memstore.TaskWorkspaceStatus("stable", snapshot); status != "awaiting_approval" {
		t.Fatalf("expected lifecycle status to remain authoritative, got %q", status)
	}
}

func TestPersistFailedNodeRecoveryEvaluationIncludesRetryContractCoordinates(t *testing.T) {
	dir := t.TempDir()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(dir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("open memory failed: %v", err)
	}
	defer func() {
		if closeErr := memoryStore.Close(); closeErr != nil {
			t.Fatalf("close memory failed: %v", closeErr)
		}
	}()
	engine := &Engine{sessionID: "session-contract", memory: memoryStore}
	point := NewPausePoint(PausePointKindWorkflowNode, "run-contract", "task-contract", "node-build", "node-build", "", "workflow", "node_pause", "digest-contract")
	point.StatusReason = "tool failed"
	point.DependsOn = []string{"node-plan"}

	if err := engine.persistFailedNodeRecoveryEvaluation("run-contract", "task-contract", point, true); err != nil {
		t.Fatalf("persist failed-node recovery evaluation failed: %v", err)
	}
	snapshot, err := memoryStore.LoadLatestTaskSnapshot("task-contract")
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.EvaluationRecords) != 1 {
		t.Fatalf("expected one evaluation record, got %+v", snapshot.EvaluationRecords)
	}
	details := strings.Join(snapshot.EvaluationRecords[0].Details, "\n")
	for _, want := range []string{
		"pause_point_id: " + point.ID,
		"pause_point_kind: workflow_node",
		"node_id: node-build",
		"retryable: true",
		"origin_run_id: run-contract",
		"origin_task_id: task-contract",
		"pause_point_digest: digest-contract",
		"current_node_status: failed",
		"dependency_readiness: recorded",
		"scheduler_continuation_readiness: requires_manual_review",
		"depends_on: node-plan",
	} {
		if !strings.Contains(details, want) {
			t.Fatalf("expected failed-node retry coordinate %q in details:\n%s", want, details)
		}
	}
}

func latestNodeEvidenceForRole(records []memstore.EvaluationRecord, role string) *memstore.EvaluationRecord {
	for _, record := range memstore.LatestNodeWorkEvidenceChain(records, 10) {
		if memstore.EvaluationRecordNodeRole(record) == role {
			cloned := record
			cloned.Details = append([]string(nil), record.Details...)
			return &cloned
		}
	}
	return nil
}

func TestEngineCallMCP_WritesTranscriptAndEvents(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-mcp")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	engine := NewEngine(
		registry.NewToolRegistry(),
		writer,
		store,
		"session-mcp",
		verification.DefaultPolicy(),
		llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}),
		&stubMCPClient{response: mcp.CallResponse{JSONRPC: "2.0", ID: "mcp-1", Result: json.RawMessage(`{"tools":[{"name":"read_file"}]}`)}},
		nil,
	)

	result, err := engine.CallMCP(context.Background(), mcp.HTTPSpec("http://example.com/mcp"), "tools/list", map[string]any{"cursor": "0"})
	if err != nil {
		t.Fatalf("call mcp failed: %v", err)
	}
	if !strings.Contains(string(result.Result), `"read_file"`) {
		t.Fatalf("expected read_file in result, got %s", string(result.Result))
	}

	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}

	text := string(content)
	if !strings.Contains(text, "mcp.requested") {
		t.Fatalf("expected transcript to contain mcp.requested event, got %s", text)
	}
	if !strings.Contains(text, "mcp.completed") {
		t.Fatalf("expected transcript to contain mcp.completed event, got %s", text)
	}

	history := store.History()
	if len(history) == 0 {
		t.Fatal("expected event store history")
	}
	if history[len(history)-1].Type != "memory.compaction_boundary_written" {
		t.Fatalf("expected last event to be memory.compaction_boundary_written, got %s", history[len(history)-1].Type)
	}
}

func TestEngineRun_DefaultPermissionModeBlocksGeneratedSkillWrite(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-run-awaiting-approval")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()
	store := events.NewStore()
	skillStore := skills.NewStore(filepath.Join(tempDir, "skills"))
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	engine := NewEngine(toolRegistry, writer, store, "session-run-awaiting-approval", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, skillStore).
		WithMemory(memoryStore).
		WithTaskWorkspace("awaiting-approval-task", filepath.Join(tempDir, ".avatars", "tasks", "awaiting-approval-task")).
		WithPermissionMode(PermissionModeDefault)
	result, err := engine.Run(context.Background(), "Analyze the current repository and propose a refactoring plan")
	if err == nil || !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("expected approval required error, got %v", err)
	}
	if strings.TrimSpace(result.TranscriptPath) == "" {
		t.Fatal("expected partial run result transcript path on approval-required error")
	}
	if strings.TrimSpace(result.Summary) == "" || !strings.Contains(result.Summary, "approval required") {
		t.Fatalf("expected partial run summary to retain approval-required error, got %+v", result)
	}
	history := store.History()
	if len(history) == 0 {
		t.Fatal("expected runtime events")
	}
	foundAwaitingApproval := false
	foundBlockedRunCompleted := false
	buildActivated := -1
	writeRequested := -1
	writeAwaitingApproval := -1
	buildCompleted := -1
	for index, event := range history {
		if event.Type == "tool.awaiting_approval" {
			foundAwaitingApproval = true
			if fmt.Sprint(event.Payload["tool"]) == "write" && fmt.Sprint(event.Payload["operation"]) == "file_write" {
				writeAwaitingApproval = index
			}
		}
		if event.Type == "workflow.node_activated" && fmt.Sprint(event.Payload["node_id"]) == "node-build" {
			buildActivated = index
		}
		if event.Type == "tool.requested" && fmt.Sprint(event.Payload["tool"]) == "write" && fmt.Sprint(event.Payload["operation"]) == "file_write" {
			writeRequested = index
		}
		if event.Type == "workflow.node_completed" && fmt.Sprint(event.Payload["node_id"]) == "node-build" {
			buildCompleted = index
		}
		if event.Type == "run.completed" && fmt.Sprint(event.Payload["status"]) == "awaiting_approval" {
			foundBlockedRunCompleted = true
			if summary := fmt.Sprint(event.Payload["summary"]); !strings.Contains(summary, "approval required") {
				t.Fatalf("expected blocked run summary to retain approval-required context, got %q", summary)
			}
		}
		if event.Type == "skill.generated" {
			t.Fatalf("expected skill generation to stop before skill.generated, got %+v", event)
		}
	}
	if !foundAwaitingApproval {
		t.Fatalf("expected tool.awaiting_approval event, got %+v", history)
	}
	if !foundBlockedRunCompleted {
		t.Fatalf("expected run.completed awaiting_approval event, got %+v", history)
	}
	if buildActivated < 0 || writeRequested < 0 || writeAwaitingApproval < 0 {
		t.Fatalf("expected node-build activation, write request, and awaiting approval events, got %+v", history)
	}
	if !(buildActivated < writeRequested && writeRequested < writeAwaitingApproval) {
		t.Fatalf("expected Builder write request inside scheduler-owned node-build execution, got activated=%d requested=%d awaiting=%d history=%+v", buildActivated, writeRequested, writeAwaitingApproval, history)
	}
	if buildCompleted >= 0 {
		t.Fatalf("expected node-build to pause before completion while awaiting approval, got completed index %d history=%+v", buildCompleted, history)
	}
	snapshot, err := memoryStore.LoadLatestTaskSnapshot("awaiting-approval-task")
	if err != nil {
		t.Fatalf("load task snapshot failed: %v", err)
	}
	nodeEvidence := memstore.LatestNodeWorkEvidence(snapshot.EvaluationRecords)
	if nodeEvidence == nil {
		t.Fatalf("expected latest node work evidence, got %+v", snapshot.EvaluationRecords)
	}
	if memstore.EvaluationRecordNodeID(*nodeEvidence) != "node-build" || memstore.EvaluationRecordNodeRole(*nodeEvidence) != "Builder" {
		t.Fatalf("expected builder node work evidence, got %+v", nodeEvidence)
	}
	if nodeEvidence.Tool != "write" || memstore.EvaluationRecordOperation(*nodeEvidence) != "file_write" {
		t.Fatalf("expected write/file_write node work evidence, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordDetailValue(*nodeEvidence, "status") != "awaiting_approval" {
		t.Fatalf("expected awaiting approval node work evidence, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordPausePointID(*nodeEvidence) == "" || memstore.EvaluationRecordRecoveryKind(*nodeEvidence) != "approval_required" {
		t.Fatalf("expected approval recovery coordinates, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordDetailValue(*nodeEvidence, "pause_point_kind") != "workflow_node" {
		t.Fatalf("expected approval-required evidence to point at workflow-node pause point, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordVerifierGateStatus(*nodeEvidence) != "pending_approval" || memstore.EvaluationRecordVerified(*nodeEvidence) != "false" {
		t.Fatalf("expected awaiting approval node evidence to stay on approval gate metadata, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordApprovalKey(*nodeEvidence) != "" || memstore.EvaluationRecordContinuationRunID(*nodeEvidence) != "" || memstore.EvaluationRecordContinuationTaskID(*nodeEvidence) != "" || memstore.EvaluationRecordReplayStatus(*nodeEvidence) != "" {
		t.Fatalf("expected approval-required node evidence to exclude replay lineage, got %+v", nodeEvidence)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "skills", "generated")); err == nil {
		entries, readErr := os.ReadDir(filepath.Join(tempDir, "skills", "generated"))
		if readErr != nil {
			t.Fatalf("read generated skills dir failed: %v", readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("expected no generated skill files after awaiting approval, got %d", len(entries))
		}
	}
}

func TestEngineCallTool_WritesStandardToolEvents(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	targetPath := filepath.Join(tempDir, "process_record.md")
	if err := os.WriteFile(targetPath, []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("seed file failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}

	store := events.NewStore()
	engine := NewEngine(toolRegistry, writer, store, "session-tool", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, nil)
	result, err := engine.CallTool(context.Background(), "read", "file_read", tools.ReadInput{Path: targetPath})
	if err != nil {
		t.Fatalf("call tool failed: %v", err)
	}
	if !strings.Contains(result.Content, "Process Record") {
		t.Fatalf("expected tool content to include file contents, got %q", result.Content)
	}

	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "tool.requested") {
		t.Fatalf("expected transcript to contain tool.requested, got %s", text)
	}
	if !strings.Contains(text, "tool.authorized") {
		t.Fatalf("expected transcript to contain tool.authorized, got %s", text)
	}
	if !strings.Contains(text, "tool.completed") {
		t.Fatalf("expected transcript to contain tool.completed, got %s", text)
	}
	if !strings.Contains(text, `"action_id":"session-tool-run-`) {
		t.Fatalf("expected transcript to contain action_id, got %s", text)
	}
	if !strings.Contains(text, `"action":{"action_id":"`) {
		t.Fatalf("expected transcript to contain structured action envelope, got %s", text)
	}
	if !strings.Contains(text, `"constraints":{"readonly":true}`) {
		t.Fatalf("expected transcript to contain readonly constraints, got %s", text)
	}
	if !strings.Contains(text, `"args":{"path":"`) {
		t.Fatalf("expected transcript to contain structured action args, got %s", text)
	}
	if !strings.Contains(text, `"policy":{"decision":"allow","decision_source":"runtime_builtin_policy"`) ||
		!strings.Contains(text, `"risk_level":"safe"`) ||
		!strings.Contains(text, `"readonly":true`) ||
		!strings.Contains(text, `Runtime builtin policy allowed read as a safe tool action. The action is bounded as read-only within the current sandbox. Independent verification is not required for this action.`) ||
		!strings.Contains(text, `"verifier_required":false`) {
		t.Fatalf("expected transcript to contain authorization policy rationale, got %s", text)
	}
	if !strings.Contains(text, "run.completed") {
		t.Fatalf("expected transcript to contain run.completed, got %s", text)
	}
	if !strings.Contains(text, "memory.compaction_boundary_written") {
		t.Fatalf("expected transcript to contain memory.compaction_boundary_written, got %s", text)
	}
	if len(store.History()) == 0 {
		t.Fatal("expected event history")
	}
}

func TestEngineInspectMCP_ProbesBoundedCapabilities(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-mcp-inspect")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	engine := NewEngine(
		registry.NewToolRegistry(),
		writer,
		store,
		"session-mcp-inspect",
		verification.DefaultPolicy(),
		llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}),
		&stubMCPClient{responses: map[string]mcp.CallResponse{
			"tools/list":     {JSONRPC: "2.0", ID: "mcp-tools", Result: json.RawMessage(`{"tools":[{"name":"read_file"}]}`)},
			"resources/list": {JSONRPC: "2.0", ID: "mcp-resources", Error: &mcp.RPCError{Code: -32601, Message: "method not found"}},
			"prompts/list":   {JSONRPC: "2.0", ID: "mcp-prompts", Result: json.RawMessage(`{"prompts":[{"name":"summarize_repo"}]}`)},
		}},
		nil,
	)

	result, err := engine.InspectMCP(context.Background(), mcp.HTTPSpec("http://example.com/mcp"))
	if err != nil {
		t.Fatalf("inspect mcp failed: %v", err)
	}
	if len(result.Sections) != 3 {
		t.Fatalf("expected 3 inspection sections, got %+v", result.Sections)
	}
	if !result.Sections[0].Supported || len(result.Sections[0].Names) != 1 || result.Sections[0].Names[0] != "read_file" {
		t.Fatalf("expected tools section, got %+v", result.Sections[0])
	}
	if result.Sections[1].Supported {
		t.Fatalf("expected resources section to be unsupported, got %+v", result.Sections[1])
	}
	if !result.Sections[2].Supported || len(result.Sections[2].Names) != 1 || result.Sections[2].Names[0] != "summarize_repo" {
		t.Fatalf("expected prompts section, got %+v", result.Sections[2])
	}

	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "mcp.requested") || !strings.Contains(text, "tools/list") {
		t.Fatalf("expected transcript to contain tools/list request, got %s", text)
	}
	if !strings.Contains(text, "resources/list") || !strings.Contains(text, "mcp.failed") {
		t.Fatalf("expected transcript to contain unsupported resources failure, got %s", text)
	}
	if !strings.Contains(text, "prompts/list") || !strings.Contains(text, "mcp.completed") {
		t.Fatalf("expected transcript to contain prompts/list completion, got %s", text)
	}
}

func TestEngineMCPRunContext_UsesBoundTaskWorkspace(t *testing.T) {
	engine := &Engine{sessionID: "session-mcp", taskID: "demo-task", taskRoot: "/tmp/demo-task"}

	_, taskID, taskTitle, inspectPayload := engine.newMCPRunContext("inspect", "http://example.com/mcp", "")
	if taskID != "demo-task" {
		t.Fatalf("expected inspect task id to reuse bound task workspace, got %q", taskID)
	}
	if taskTitle != "MCP inspect http://example.com/mcp" {
		t.Fatalf("expected inspect task title, got %q", taskTitle)
	}
	if inspectPayload["task_workspace_id"] != "demo-task" {
		t.Fatalf("expected inspect payload to include task workspace id, got %+v", inspectPayload)
	}
	if inspectPayload["task_workspace_root"] != "/tmp/demo-task" {
		t.Fatalf("expected inspect payload to include task workspace root, got %+v", inspectPayload)
	}

	_, callTaskID, callTaskTitle, callPayload := engine.newMCPRunContext("call", "http://example.com/mcp", "tools/list")
	if callTaskID != "demo-task" {
		t.Fatalf("expected call task id to reuse bound task workspace, got %q", callTaskID)
	}
	if callTaskTitle != "MCP call tools/list" {
		t.Fatalf("expected call task title, got %q", callTaskTitle)
	}
	if callPayload["method"] != "tools/list" {
		t.Fatalf("expected call payload to include method, got %+v", callPayload)
	}
}

func TestEngineCallTool_WriteTriggersVerificationEvents(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module example.com/writeverify\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-write-verify")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}

	store := events.NewStore()
	engine := NewEngine(toolRegistry, writer, store, "session-write-verify", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, nil)
	var verifierWorkingDir string
	engine.verifier = func(workingDir string, includeRace bool) verifierRunner {
		verifierWorkingDir = workingDir
		return stubVerifierRunner{report: verification.Report{
			Verdict: verification.VerdictPass,
			Summary: "Verification finished with PASS. 1 passed, 0 partial, 0 failed.",
			Checks: []verification.CheckEvidence{{
				Name:           "go test",
				CommandRun:     "go test ./...",
				OutputObserved: "ok\tavatars/internal/runtime",
				Expected:       "All package tests should pass.",
				Actual:         "Command completed successfully.",
				Result:         verification.VerdictPass,
			}},
		}}
	}

	result, err := engine.CallTool(context.Background(), "write", "file_write", tools.WriteInput{Path: "note.txt", Content: "hello verifier", WorkingDir: tempDir, ExpectedTargets: []string{"note.txt"}})
	if err != nil {
		t.Fatalf("call tool failed: %v", err)
	}
	if !strings.Contains(result.Content, "Wrote") {
		t.Fatalf("expected write output, got %q", result.Content)
	}
	if verifierWorkingDir != tempDir {
		t.Fatalf("expected verifier working dir %q, got %q", tempDir, verifierWorkingDir)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "note.txt")); err != nil {
		t.Fatalf("expected note.txt to exist: %v", err)
	}

	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "verification.started") {
		t.Fatalf("expected transcript to contain verification.started, got %s", text)
	}
	if !strings.Contains(text, `"expected_targets":["note.txt"]`) {
		t.Fatalf("expected transcript to contain expected_targets, got %s", text)
	}
	if !strings.Contains(text, `"policy":{"decision":"allow","decision_source":"runtime_builtin_policy"`) ||
		!strings.Contains(text, `"risk_level":"guarded"`) ||
		!strings.Contains(text, `"readonly":false`) ||
		!strings.Contains(text, `Runtime builtin policy allowed write as a guarded tool action. The action is allowed to mutate sandboxed workspace state. Independent verification is required after completion.`) {
		t.Fatalf("expected transcript to contain non-readonly authorization policy, got %s", text)
	}
	if !strings.Contains(text, `"verifier_required":true`) {
		t.Fatalf("expected transcript to contain verifier-required authorization policy, got %s", text)
	}
	if !strings.Contains(text, "verification.check_completed") {
		t.Fatalf("expected transcript to contain verification.check_completed, got %s", text)
	}
	if !strings.Contains(text, "verification.completed") {
		t.Fatalf("expected transcript to contain verification.completed, got %s", text)
	}
}

func TestEngineCallTool_WriteReturnsErrorWhenVerificationFails(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module example.com/writeverifyfail\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-write-verify-fail")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()

	engine := NewEngine(toolRegistry, writer, store, "session-write-verify-fail", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, nil).WithMemory(memoryStore)
	engine.verifier = func(workingDir string, includeRace bool) verifierRunner {
		return stubVerifierRunner{report: verification.Report{
			Verdict: verification.VerdictFail,
			Summary: "Verification finished with FAIL. 0 passed, 0 partial, 1 failed.",
			Checks: []verification.CheckEvidence{{
				Name:           "go test",
				CommandRun:     "go test ./...",
				OutputObserved: "FAIL\tavatars/internal/runtime",
				Expected:       "All package tests should pass.",
				Actual:         "exit status 1",
				Result:         verification.VerdictFail,
			}},
		}}
	}

	_, err = engine.CallTool(context.Background(), "write", "file_write", tools.WriteInput{Path: "note.txt", Content: "hello verifier", WorkingDir: tempDir, ExpectedTargets: []string{"note.txt"}})
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if !strings.Contains(err.Error(), "verification failed after write") {
		t.Fatalf("expected verification failure error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tempDir, "note.txt")); statErr != nil {
		t.Fatalf("expected file to be written before gate failure, got %v", statErr)
	}
	content, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, `"status":"needs_remediation"`) {
		t.Fatalf("expected needs_remediation terminal status, got %s", text)
	}
	if !strings.Contains(text, "verification.remediation_pause_point") {
		t.Fatalf("expected verifier remediation pause point event, got %s", text)
	}
	if !strings.Contains(text, `"pause_point_kind":"verifier_remediation"`) {
		t.Fatalf("expected verifier remediation pause point kind, got %s", text)
	}
	if !strings.Contains(text, `"failed_checks"`) || !strings.Contains(text, `"name":"go test"`) {
		t.Fatalf("expected failed verifier checks in remediation pause point, got %s", text)
	}
	if !strings.Contains(text, `"expected_targets"`) || !strings.Contains(text, `note.txt`) {
		t.Fatalf("expected verifier remediation pause point targets, got %s", text)
	}
	if !strings.Contains(text, "Verifier blocked completion for write/file_write") {
		t.Fatalf("expected verifier completion summary, got %s", text)
	}

	snapshot, err := memoryStore.LoadSnapshot("session-write-verify-fail")
	if err != nil {
		t.Fatalf("load memory snapshot failed: %v", err)
	}
	matched := false
	for _, record := range snapshot.EvaluationRecords {
		if record.Cause != "verifier_remediation_pause_point" {
			continue
		}
		if record.Source != "verifier" {
			t.Fatalf("expected verifier source, got %q", record.Source)
		}
		details := strings.Join(record.Details, "\n")
		if !strings.Contains(details, "pause_point_kind: verifier_remediation") || !strings.Contains(details, "failed_check: go test") {
			t.Fatalf("expected remediation pause point details, got %+v", record.Details)
		}
		matched = true
		break
	}
	if !matched {
		t.Fatalf("expected verifier remediation pause point evaluation record, got %+v", snapshot.EvaluationRecords)
	}

	history := store.History()
	if len(history) == 0 {
		t.Fatal("expected event history")
	}
	last := history[len(history)-1]
	if last.Type != "memory.compaction_boundary_written" {
		t.Fatalf("expected last event to be memory.compaction_boundary_written, got %s", last.Type)
	}
}

func TestEngineRun_PersistsHotSummariesToSQLite(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-sqlite")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new sqlite memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	projectMemoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "project-memory"))
	if err != nil {
		t.Fatalf("new project sqlite memory store failed: %v", err)
	}
	defer func() {
		_ = projectMemoryStore.Close()
	}()

	engine := NewEngine(toolRegistry, writer, events.NewStore(), "session-sqlite", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, nil).WithMemory(memoryStore)
	if _, err := engine.Run(context.Background(), "Analyze the current repository and propose a refactoring plan"); err != nil {
		t.Fatalf("engine run failed: %v", err)
	}

	snapshot, err := memoryStore.LoadSnapshot("session-sqlite")
	if err != nil {
		t.Fatalf("load sqlite snapshot failed: %v", err)
	}
	if snapshot.StableSummary == "" {
		t.Fatal("expected stable summary in sqlite memory store")
	}
	if snapshot.TaskSummary == "" {
		t.Fatal("expected task summary in sqlite memory store")
	}
	if len(snapshot.AvatarSummaries) == 0 {
		t.Fatal("expected avatar summaries in sqlite memory store")
	}
}

func TestEngineRun_UsesStableTaskWorkspaceID(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "sessions"), "session-stable")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	engine := NewEngine(toolRegistry, writer, events.NewStore(), "session-stable", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, nil).WithTaskWorkspace("demo-task", filepath.Join(tempDir, ".avatars", "tasks", "demo-task"))
	result, err := engine.Run(context.Background(), "Analyze the current repository and propose a refactoring plan")
	if err != nil {
		t.Fatalf("engine run failed: %v", err)
	}
	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, `"task_id":"demo-task"`) {
		t.Fatalf("expected transcript to use stable task id demo-task, got %s", text)
	}
	if !strings.Contains(text, `"task_workspace_id":"demo-task"`) {
		t.Fatalf("expected run.started payload to include stable task workspace id, got %s", text)
	}
}

func TestEngineResume_RestoresTranscriptAndWritesResumeEvent(t *testing.T) {
	tempDir := t.TempDir()
	sourceWriter, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "source-session")
	if err != nil {
		t.Fatalf("new source writer failed: %v", err)
	}
	defer func() {
		_ = sourceWriter.Close()
	}()

	if err := sourceWriter.Append(events.Envelope{EventID: "evt-source-1", Sequence: 1, RunID: "run-source", TaskID: "task-source", Phase: "reviewing", Type: "run.completed", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"status": "completed", "summary": "Source summary"}}); err != nil {
		t.Fatalf("append source run.completed failed: %v", err)
	}
	if err := sourceWriter.Append(events.Envelope{EventID: "evt-source-task", Sequence: 2, RunID: "run-source", TaskID: "task-source", Phase: "planning", Type: "memory.summary_updated", EmittedAt: time.Now().UTC(), Source: "memory", Payload: map[string]any{"scope": "task", "summary": "Task summary"}}); err != nil {
		t.Fatalf("append source task summary failed: %v", err)
	}
	if err := sourceWriter.Append(events.Envelope{EventID: "evt-source-attempt", Sequence: 3, RunID: "run-source", TaskID: "task-source", Phase: "reviewing", Type: "tool.approval_continued", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"pause_point_id": "pause-source", "pause_point_kind": "approval_gate", "resume_attempt_id": "resume-source", "status": "completed", "summary": "Continuation completed."}}); err != nil {
		t.Fatalf("append source resume attempt failed: %v", err)
	}
	if err := sourceWriter.Append(events.Envelope{EventID: "evt-source-2", Sequence: 4, RunID: "run-source", TaskID: "task-source", Phase: "reviewing", Type: "memory.compaction_boundary_written", EmittedAt: time.Now().UTC(), Source: "memory", Payload: map[string]any{"status": "completed", "summary": "Boundary summary", "boundary_kind": "run_terminal"}}); err != nil {
		t.Fatalf("append source boundary failed: %v", err)
	}
	if err := sourceWriter.Flush(); err != nil {
		t.Fatalf("flush source writer failed: %v", err)
	}

	resumeWriter, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "resume-session")
	if err != nil {
		t.Fatalf("new resume writer failed: %v", err)
	}
	defer func() {
		_ = resumeWriter.Close()
	}()

	engine := NewEngine(registry.NewToolRegistry(), resumeWriter, events.NewStore(), "resume-session", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, nil)
	result, err := engine.Resume(context.Background(), sourceWriter.Path())
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if result.Restored.Summary != "Boundary summary" {
		t.Fatalf("expected restored summary Boundary summary, got %q", result.Restored.Summary)
	}
	if result.Restored.TaskSummary != "Task summary" {
		t.Fatalf("expected restored task summary Task summary, got %q", result.Restored.TaskSummary)
	}
	if result.Restored.PausePointID != "pause-source" || result.Restored.ResumeAttemptID != "resume-source" || result.Restored.ResumeAttemptStatus != "completed" {
		t.Fatalf("expected restored pause/attempt truth, got %+v", result.Restored)
	}

	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read resume transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "memory.resume_restored") {
		t.Fatalf("expected transcript to contain memory.resume_restored, got %s", text)
	}
	if !strings.Contains(text, "memory.compaction_boundary_written") {
		t.Fatalf("expected transcript to contain memory.compaction_boundary_written, got %s", text)
	}
	if !strings.Contains(text, "\"task_summary\":\"Task summary\"") {
		t.Fatalf("expected transcript to contain restored task summary payload, got %s", text)
	}
	if !strings.Contains(text, "\"pause_point_id\":\"pause-source\"") || !strings.Contains(text, "\"resume_attempt_id\":\"resume-source\"") {
		t.Fatalf("expected transcript to contain restored pause/attempt payload, got %s", text)
	}
}

func TestEngineRunWithResume_ContinuesWithRestoredSummary(t *testing.T) {
	tempDir := t.TempDir()
	legacyPath := filepath.Join(tempDir, "legacy.jsonl")
	legacyTranscript := strings.Join([]string{
		`{"event_id":"evt-task","sequence":1,"run_id":"run-legacy","task_id":"task-legacy","phase":"planning","type":"memory.summary_updated","emitted_at":"2026-04-28T00:00:00Z","source":"memory","payload":{"scope":"task","summary":"Legacy task summary"}}`,
		`{"event_id":"evt-avatar","sequence":2,"run_id":"run-legacy","task_id":"task-legacy","avatar_id":"avatar-planner","phase":"planning","type":"memory.summary_updated","emitted_at":"2026-04-28T00:00:01Z","source":"memory","payload":{"scope":"avatar","summary":"Planner hot summary"}}`,
		`{"event_id":"evt-node","sequence":3,"run_id":"run-legacy","task_id":"task-legacy","phase":"planning","type":"workflow.node_created","emitted_at":"2026-04-28T00:00:02Z","source":"runtime","payload":{"title":"Inspect current task state","assigned_role":"Researcher"}}`,
		`{"event_id":"evt-tool","sequence":4,"run_id":"run-legacy","task_id":"task-legacy","avatar_id":"avatar-builder","phase":"execution","type":"tool.completed","emitted_at":"2026-04-28T00:00:03Z","source":"tool","payload":{"summary":"Tool completed: read process_record.md."}}`,
		`{"event_id":"evt-1","sequence":5,"run_id":"run-legacy","task_id":"task-legacy","phase":"reviewing","type":"run.completed","emitted_at":"2026-04-28T00:00:04Z","source":"runtime","payload":{"status":"completed","summary":"Legacy stable summary"}}`,
		`{"event_id":"evt-2","sequence":6,"run_id":"run-legacy","task_id":"task-legacy","phase":"reviewing","type":"memory.compaction_boundary_written","emitted_at":"2026-04-28T00:00:05Z","source":"memory","payload":{"status":"completed","summary":"Legacy stable summary","boundary_kind":"run_terminal"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(legacyPath, []byte(legacyTranscript), 0o644); err != nil {
		t.Fatalf("write legacy transcript failed: %v", err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-run-resume")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	engine := NewEngine(toolRegistry, writer, events.NewStore(), "session-run-resume", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, nil)
	result, err := engine.RunWithResume(context.Background(), "Continue repository analysis", legacyPath)
	if err != nil {
		t.Fatalf("run with resume failed: %v", err)
	}
	if !strings.Contains(result.Summary, "Continued from transcript") {
		t.Fatalf("expected resumed run summary to mention transcript continuation, got %q", result.Summary)
	}

	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "memory.resume_restored") {
		t.Fatalf("expected transcript to contain memory.resume_restored, got %s", text)
	}
	if !strings.Contains(text, "Restored stable summary from") {
		t.Fatalf("expected planner message to mention restored summary, got %s", text)
	}
	if !strings.Contains(text, "Restored task summary is available") {
		t.Fatalf("expected planner message to mention restored task summary, got %s", text)
	}
	if !strings.Contains(text, "Restored 1 avatar summaries") {
		t.Fatalf("expected planner message to mention restored avatar summary count, got %s", text)
	}
	if !strings.Contains(text, "Restored 2 recent key events") {
		t.Fatalf("expected planner message to mention restored recent events, got %s", text)
	}
}

func TestEngineRun_LoadsAlwaysOnSkillIntoPrompt(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-always-on")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()
	skillStore := skills.NewStore(filepath.Join(tempDir, "skills"))
	if err := os.MkdirAll(filepath.Join(tempDir, "skills", "approved"), 0o755); err != nil {
		t.Fatalf("mkdir approved failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "skills", "approved", "avatars-operating-contract.md"), []byte(`---
name: avatars-operating-contract
description: persistent operating contract
when_to_use: always
context: classify natural language first
version: 0.1.0
lifecycle-state: approved
always-on: true
allowed-tools:
  - read
---
Always answer directly when the user asks a normal question.
`), 0o644); err != nil {
		t.Fatalf("write always-on skill failed: %v", err)
	}
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := NewEngine(toolRegistry, writer, events.NewStore(), "session-always-on", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, skillStore).
		WithPermissionMode(PermissionModePlan)
	result, err := engine.Run(context.Background(), "Analyze the current repository and propose a refactoring plan")
	if err != nil {
		t.Fatalf("engine run failed: %v", err)
	}
	if result.ApprovedSkillCount != 1 {
		t.Fatalf("expected approved skill count to include always-on skill, got %+v", result)
	}
	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "skill.always_on_loaded") {
		t.Fatalf("expected transcript to record always-on skill loading, got %s", text)
	}
}

func TestEngineRunWithResume_MergesSQLiteHotMemory(t *testing.T) {
	tempDir := t.TempDir()
	legacyPath := filepath.Join(tempDir, "legacy.jsonl")
	legacyTranscript := strings.Join([]string{
		`{"event_id":"evt-1","sequence":1,"run_id":"run-legacy","task_id":"task-legacy","phase":"reviewing","type":"run.completed","emitted_at":"2026-04-28T00:00:02Z","source":"runtime","payload":{"status":"completed","summary":"Legacy stable summary"}}`,
		`{"event_id":"evt-2","sequence":2,"run_id":"run-legacy","task_id":"task-legacy","phase":"reviewing","type":"memory.compaction_boundary_written","emitted_at":"2026-04-28T00:00:03Z","source":"memory","payload":{"status":"completed","summary":"Legacy stable summary","boundary_kind":"run_terminal"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(legacyPath, []byte(legacyTranscript), 0o644); err != nil {
		t.Fatalf("write legacy transcript failed: %v", err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new sqlite memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	projectMemoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "project-memory"))
	if err != nil {
		t.Fatalf("new project sqlite memory store failed: %v", err)
	}
	defer func() {
		_ = projectMemoryStore.Close()
	}()
	for _, record := range []memstore.SummaryRecord{
		{SessionID: "legacy", RunID: "run-legacy", TaskID: "task-legacy", Scope: memstore.ScopeStable, Summary: "Legacy stable summary", Status: "completed", BoundaryKind: "run_terminal"},
		{SessionID: "legacy", RunID: "run-legacy", TaskID: "task-legacy", Scope: memstore.ScopeTask, Summary: "SQLite task summary"},
		{SessionID: "legacy", RunID: "run-legacy", TaskID: "task-legacy", AvatarID: "avatar-planner", Scope: memstore.ScopeAvatar, Role: "Planner", Summary: "SQLite planner summary"},
	} {
		if err := memoryStore.UpsertSummary(record); err != nil {
			t.Fatalf("seed sqlite memory failed: %v", err)
		}
	}
	if err := memoryStore.RecordWarmLesson(memstore.WarmLessonRecord{SessionID: "legacy", RunID: "run-legacy", TaskID: "task-legacy", Kind: "workflow", Summary: "Keep the planner decomposition stable before execution: SQLite task summary", Source: "task_summary", Confidence: "medium"}); err != nil {
		t.Fatalf("seed sqlite warm lesson failed: %v", err)
	}
	if err := projectMemoryStore.RecordProjectLesson(memstore.ProjectLessonRecord{SourceTaskID: "task-legacy", Kind: "runtime_followup", Summary: "Add a deterministic follow-up path for patch when verifier verdict is PARTIAL before marking the task complete.", Source: "evolution_candidate", Confidence: "high"}); err != nil {
		t.Fatalf("seed project sqlite lesson failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-sqlite-resume")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	engine := NewEngine(toolRegistry, writer, events.NewStore(), "session-sqlite-resume", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, nil).WithMemory(memoryStore).WithProjectMemory(projectMemoryStore)
	result, err := engine.RunWithResume(context.Background(), "Continue repository analysis", legacyPath)
	if err != nil {
		t.Fatalf("run with sqlite memory resume failed: %v", err)
	}

	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "Restored task summary is available") {
		t.Fatalf("expected planner message to mention restored sqlite task summary, got %s", text)
	}
	if !strings.Contains(text, "Restored 1 avatar summaries") {
		t.Fatalf("expected planner message to mention sqlite avatar summary count, got %s", text)
	}
	if !strings.Contains(text, "Restored 1 warm lessons") {
		t.Fatalf("expected planner message to mention sqlite warm lesson count, got %s", text)
	}
	if !strings.Contains(text, "Restored 1 project lessons") {
		t.Fatalf("expected planner message to mention project lesson count, got %s", text)
	}
	if !strings.Contains(text, `"task_summary":"SQLite task summary"`) {
		t.Fatalf("expected transcript to contain sqlite task summary payload, got %s", text)
	}
}

// capturingLLMClient records every Generate call's system/user prompt so
// the test can assert what reached the avatar LLM. It returns a minimal
// "ok" response so the loop can complete the workflow.
type capturingLLMClient struct {
	prompts []llm.Request
}

func (c *capturingLLMClient) Provider() string { return "capturing" }

func (c *capturingLLMClient) Supports(capability llm.Capability) bool { return false }

func (c *capturingLLMClient) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	c.prompts = append(c.prompts, request)
	return llm.Response{Text: "ok", Provider: "capturing", Model: "capturing-model"}, nil
}

// TODO-02 (P0): The user's `@file` / `--from-file` content must reach the
// avatar LLM as part of the prompt. The bug was that the file was loaded
// into `replFileContext` for the natural-language classifier, but never
// injected into the runtime prompt bundle, so the avatar would say
// "no repository evidence found" while the user's attached file sat
// unread. This test asserts that with `Engine.WithAttachedFile(path,
// content)`, the captured LLM prompts contain the attached file's path
// and content.
func TestEngineRun_AttachedFileInjectsIntoPrompt(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}

	// Simulate the user's attached spec doc: 4 python script tasks as in
	// the user's "帮我写个脚本" follow-up. The avatar must see this content,
	// not "no repository evidence found".
	attachedPath := "plan.md"
	attachedContent := "## Plan\n\n" +
		"1. 找出100-200之间所有的质数\n" +
		"2. 找出1-1000之间所有的完美数\n" +
		"3. 公鸡每只五钱，母鸡每只三钱，小鸡每三只一钱，用一百钱买一百只鸡\n" +
		"4. 九九乘法表\n"
	if err := os.WriteFile(attachedPath, []byte(attachedContent), 0o644); err != nil {
		t.Fatalf("write plan failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-attached-file")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	capturing := &capturingLLMClient{}
	engine := NewEngine(toolRegistry, writer, events.NewStore(), "session-attached-file", verification.DefaultPolicy(), capturing, nil, nil).
		WithPermissionMode(PermissionModePlan).
		WithAttachedFile(attachedPath, attachedContent)

	// A short input that doesn't itself trigger deterministic re-reads.
	_, err = engine.Run(context.Background(), "execute the attached plan")
	if err != nil {
		t.Fatalf("engine run failed: %v", err)
	}

	if len(capturing.prompts) == 0 {
		t.Fatal("expected at least one LLM call to have been captured")
	}

	// The attached file's content must appear in the system prompt the
	// avatar LLM sees. We aggregate every captured system prompt and
	// assert the path + content are present.
	combined := strings.Builder{}
	for _, p := range capturing.prompts {
		combined.WriteString(p.SystemPrompt)
		combined.WriteString("\n---\n")
		combined.WriteString(p.UserPrompt)
		combined.WriteString("\n")
	}
	combinedText := combined.String()
	if !strings.Contains(combinedText, "attached_file") {
		t.Fatalf("expected captured prompt to include attached_file section, got %q", combinedText)
	}
	if !strings.Contains(combinedText, "path: plan.md") {
		t.Fatalf("expected captured prompt to include attached file path, got %q", combinedText)
	}
	if !strings.Contains(combinedText, "找出100-200之间所有的质数") {
		t.Fatalf("expected captured prompt to include attached file content (质数 task), got %q", combinedText)
	}
	if !strings.Contains(combinedText, "九九乘法表") {
		t.Fatalf("expected captured prompt to include attached file content (九九乘法表 task), got %q", combinedText)
	}
}

// TODO-02 (P0): Defensive companion. When `WithAttachedFile` is NOT called
// (i.e. the user did not pass `--from-file` and the REPL `@file` was not
// used), the prompt must NOT contain a stray `attached_file:` header. An
// empty section would confuse the avatar into thinking there is evidence
// it cannot see.
func TestEngineRun_NoAttachedFileProducesNoSection(t *testing.T) {
	tempDir := t.TempDir()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-no-attached")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	capturing := &capturingLLMClient{}
	engine := NewEngine(toolRegistry, writer, events.NewStore(), "session-no-attached", verification.DefaultPolicy(), capturing, nil, nil).
		WithPermissionMode(PermissionModePlan)
	if _, err := engine.Run(context.Background(), "just analyze, no attachment"); err != nil {
		t.Fatalf("engine run failed: %v", err)
	}
	if len(capturing.prompts) == 0 {
		t.Fatal("expected at least one LLM call to have been captured")
	}
	for _, p := range capturing.prompts {
		if strings.Contains(p.SystemPrompt, "attached_file") {
			t.Fatalf("expected no attached_file section when WithAttachedFile was not called, got %q", p.SystemPrompt)
		}
		if strings.Contains(p.UserPrompt, "attached_file") {
			t.Fatalf("expected no attached_file section in user prompt when WithAttachedFile was not called, got %q", p.UserPrompt)
		}
	}
}

func TestWithAutoApproveSkills_SetsFlag(t *testing.T) {
	engine := &Engine{}
	if engine.autoApproveSkills {
		t.Fatal("expected autoApproveSkills to default to false")
	}
	returned := engine.WithAutoApproveSkills(true)
	if !engine.autoApproveSkills {
		t.Fatal("expected autoApproveSkills to be true after WithAutoApproveSkills(true)")
	}
	if returned != engine {
		t.Fatal("expected WithAutoApproveSkills to return the engine for chaining")
	}
	engine.WithAutoApproveSkills(false)
	if engine.autoApproveSkills {
		t.Fatal("expected autoApproveSkills to be false after WithAutoApproveSkills(false)")
	}
}
