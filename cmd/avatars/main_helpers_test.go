package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"avatars/internal/llm"
)

// Shared CLI test helpers and stubs. Theme files live beside this one.

type stubNaturalLanguageLLM struct {
	response string
	fallback bool
}

func (s stubNaturalLanguageLLM) Provider() string { return "stub" }

func (s stubNaturalLanguageLLM) Supports(capability llm.Capability) bool { return false }

func (s stubNaturalLanguageLLM) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	return llm.Response{Text: s.response, Fallback: s.fallback, Provider: s.Provider(), Model: "stub"}, nil
}

func writeBuiltinHealedMarker(t *testing.T) {
	t.Helper()
	skillsDir := "skills"
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("create skills dir for marker: %v", err)
	}
	markerPath := filepath.Join(skillsDir, ".builtin-healed")
	if err := os.WriteFile(markerPath, []byte("test marker\n"), 0o644); err != nil {
		t.Fatalf("write builtin-healed marker: %v", err)
	}
}

func captureRunOutput(t *testing.T, args []string) string {
	t.Helper()
	content, runErr := captureRunOutputAllowError(t, args)
	if runErr != nil {
		t.Fatalf("run failed: %v\n%s", runErr, content)
	}
	return content
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func captureRunOutputAllowError(t *testing.T, args []string) (string, error) {
	t.Helper()
	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe failed: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = originalStdout
	}()

	var (
		content []byte
		readErr error
		wg      sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		content, readErr = io.ReadAll(reader)
	}()

	runErr := run(args)
	_ = writer.Close()
	wg.Wait()
	_ = reader.Close()
	if readErr != nil {
		t.Fatalf("read captured stdout failed: %v", readErr)
	}
	return string(content), runErr
}

func readSkillMarkdownEntries(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	filtered := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered, nil
}

func removeFrontmatterScalar(content string, key string) string {
	needle := key + ": "
	lines := strings.Split(content, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, needle) {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}

func skillHistorySidecarPath(skillPath string) string {
	return strings.TrimSuffix(skillPath, filepath.Ext(skillPath)) + ".history.json"
}
