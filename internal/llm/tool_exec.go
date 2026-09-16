package llm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"avatars/internal/platform"
	"avatars/internal/tools"
)

// runPreciseEdit applies an anchored edit. Semantics match ToolDefinition
// (position: replace|before|after). Shared by OpenAI and Genkit tool paths. S5.5.
func runPreciseEdit(path, anchor, oldBlock, newBlock, position string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("precise_edit: path is required")
	}
	if anchor == "" {
		return "", fmt.Errorf("precise_edit: anchor is required")
	}
	if oldBlock == "" {
		oldBlock = anchor
	}
	cleanPath := filepath.Clean(path)
	fileContent, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", fmt.Errorf("precise_edit: read: %w", err)
	}
	text := string(fileContent)
	anchorIdx := strings.Index(text, anchor)
	if anchorIdx < 0 {
		return "", fmt.Errorf("precise_edit: anchor not found in %s", cleanPath)
	}

	switch strings.ToLower(strings.TrimSpace(position)) {
	case "before":
		replaced := text[:anchorIdx] + newBlock + text[anchorIdx:]
		if err := atomicWriteFile(cleanPath, []byte(replaced)); err != nil {
			return "", fmt.Errorf("precise_edit: %w", err)
		}
		return fmt.Sprintf("Successfully inserted before anchor in %s.", cleanPath), nil
	case "after":
		replaced := text[:anchorIdx+len(anchor)] + newBlock + text[anchorIdx+len(anchor):]
		if err := atomicWriteFile(cleanPath, []byte(replaced)); err != nil {
			return "", fmt.Errorf("precise_edit: %w", err)
		}
		return fmt.Sprintf("Successfully inserted after anchor in %s.", cleanPath), nil
	default:
		anchorCount := strings.Count(text, anchor)
		if anchorCount > 1 {
			return "", fmt.Errorf("precise_edit: anchor appears %d times in %s", anchorCount, cleanPath)
		}
		oldCount := strings.Count(text, oldBlock)
		if oldCount == 0 {
			return "", fmt.Errorf("precise_edit: old_block not found in %s", cleanPath)
		}
		if oldCount > 1 {
			return "", fmt.Errorf("precise_edit: old_block appears %d times in %s", oldCount, cleanPath)
		}
		replaced := strings.Replace(text, oldBlock, newBlock, 1)
		if err := atomicWriteFile(cleanPath, []byte(replaced)); err != nil {
			return "", fmt.Errorf("precise_edit: %w", err)
		}
		return fmt.Sprintf("Successfully applied precise_edit to %s.", cleanPath), nil
	}
}

// runGitDiff returns unified git diff for path (or whole tree when empty). S5.5.
func runGitDiff(ctx context.Context, path string) (string, error) {
	args := []string{"diff", "--"}
	if trimmed := strings.TrimSpace(path); trimmed != "" {
		args = append(args, filepath.Clean(trimmed))
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("git_diff error: %v\nOutput:\n%s", err, string(output)), nil
	}
	text := string(output)
	if strings.TrimSpace(text) == "" {
		return "(no changes)", nil
	}
	return text, nil
}

// runShellDone executes a shell command with configurable timeout (S5.7 / M-15).
func runShellDone(ctx context.Context, command, workingDir string) (string, error) {
	cmdStr := strings.TrimSpace(command)
	if cmdStr == "" {
		return "", fmt.Errorf("shell_done: command is required")
	}
	if err := tools.ValidateShellCommand([]string{cmdStr}); err != nil {
		return "", fmt.Errorf("shell_done: %w", err)
	}
	wd := strings.TrimSpace(workingDir)
	if wd == "" {
		wd = "."
	}
	timeout := TimeoutConfigOrDefault().ShellDone
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var c *exec.Cmd
	if filepath.Separator == '\\' {
		// Windows: wrap in `start /b` is unreliable for kill; use cmd /c and
		// rely on CommandContext. Also set CREATE_NEW_PROCESS_GROUP via
		// SysProcAttr is platform-specific — keep portable and document that
		// wall-clock may slightly exceed timeout when child ignores signals.
		c = exec.CommandContext(execCtx, "cmd", "/c", cmdStr)
	} else {
		c = exec.CommandContext(execCtx, "sh", "-c", cmdStr)
	}
	c.Dir = wd
	platform.HideConsoleWindow(c)
	output, err := c.CombinedOutput()
	if err != nil {
		if execCtx.Err() != nil {
			return fmt.Sprintf("Command exited with error: %v (shell_done timeout %s)\nOutput:\n%s", err, timeout, string(output)), nil
		}
		return fmt.Sprintf("Command exited with error: %v\nOutput:\n%s", err, string(output)), nil
	}
	return string(output), nil
}

func atomicWriteFile(path string, content []byte) error {
	prepared, err := prepareWriteContent(path, string(content))
	if err != nil {
		return err
	}
	content = []byte(prepared)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, content, 0644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	_ = os.Remove(path) // Windows: rename fails if dest exists
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	NotifyFileMutation(path)
	return nil
}

// applyToolsToOpenAIRequest attaches Tools/ToolChoice on every tool-loop turn.
// Z8/S5.2: previously only turn==0 got tools, so multi-step write_file/edit_file
// after the first round could not continue (model had no tools on turn 1+).
// DeepSeek's thinking+tools samples also pass tools on every sub-request.
func applyToolsToOpenAIRequest(reqPayload *openAIRequest, request Request, turn int) {
	if len(request.Tools) == 0 || reqPayload == nil {
		return
	}
	_ = turn // retained for call-site compatibility; tools apply every turn
	reqPayload.Tools = buildOpenAIToolDefs(request.Tools)
	if tc := strings.TrimSpace(request.ToolChoice); tc != "" {
		reqPayload.ToolChoice = tc
	} else {
		reqPayload.ToolChoice = "auto"
	}
}

// applyStructuredOutputToOpenAIRequest sets json_object / json_schema when
// the caller asked for structured output. Skip turn-0 when tools are present
// (models often refuse tools+json_schema together).
func applyStructuredOutputToOpenAIRequest(reqPayload *openAIRequest, request Request, turn int) {
	if reqPayload == nil || !request.StructuredOutput {
		return
	}
	if turn == 0 && len(request.Tools) > 0 {
		return
	}
	rf := buildResponseFormat(request.JSONSchema)
	reqPayload.ResponseFormat = &rf
}

// resolveMaxTokens picks request override, else category default from config. S5.6.
func resolveMaxTokens(request Request) int {
	if request.MaxTokens > 0 {
		return request.MaxTokens
	}
	if request.Category == CategoryCodeGeneration {
		return DefaultMaxTokensForCode()
	}
	return 0
}

// clampMaxTokensForProvider lowers a requested budget to the provider's
// documented output cap. Zero cap means the request value is sent as-is.
func clampMaxTokensForProvider(cap, tokens int) int {
	if tokens <= 0 {
		return tokens
	}
	if cap > 0 && tokens > cap {
		return cap
	}
	return tokens
}

func (c *OpenAICompatibleClient) cappedMaxTokens(request Request) int {
	n := resolveMaxTokens(request)
	n = clampMaxTokensForProvider(HardMaxOutputTokens, n)
	return clampMaxTokensForProvider(c.preset.MaxOutputTokens, n)
}
