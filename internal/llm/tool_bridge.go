package llm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	genkitai "github.com/firebase/genkit/go/ai"
)

// --- Tool input types (used by genkit to infer JSON schemas) ---

// ReadFileInput is the input schema for read_file tool.
type ReadFileInput struct {
	Path string `json:"path"`
}

// WriteFileInput is the input schema for write_file tool.
type WriteFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// EditFileInput is the input schema for edit_file tool.
// Uses the Anthropic-style precise edit pattern: replace
// old_string with new_string exactly once in the file.
type EditFileInput struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// ShellDoneInput is the input schema for shell_done tool.
type ShellDoneInput struct {
	Command    string `json:"command"`
	WorkingDir string `json:"working_dir,omitempty"`
}

// PreciseEditInput is the input schema for precise_edit tool.
// G1: Field names now match the ToolDefinition in StandardCodeTools:
// path, anchor, old_block, new_block, position.
type PreciseEditInput struct {
	Path     string `json:"path"`
	Anchor   string `json:"anchor"`
	OldBlock string `json:"old_block"`
	NewBlock string `json:"new_block"`
	Position string `json:"position,omitempty"`
}

// GitDiffInput is the input schema for git_diff tool. S5.5.
type GitDiffInput struct {
	Path string `json:"path,omitempty"`
}

// --- Tool bridge: genkit ToolDef → avatars internal/tools ---

// buildGenkitToolRefs converts avatars ToolDefinitions into genkit ToolRefs
// that can be passed to genkitai.WithTools. Each tool bridges to the
// avatars internal/tools package for execution.
func buildGenkitToolRefs(toolDefs []ToolDefinition) []genkitai.ToolRef {
	refs := make([]genkitai.ToolRef, 0, len(toolDefs))
	for _, td := range toolDefs {
		switch td.Name {
		case "read_file":
			refs = append(refs, buildReadFileTool(td))
		case "write_file":
			refs = append(refs, buildWriteFileTool(td))
		case "edit_file":
			refs = append(refs, buildEditFileTool(td))
		case "shell_done":
			refs = append(refs, buildShellDoneTool(td))
		case "precise_edit":
			refs = append(refs, buildPreciseEditTool(td))
		case "git_diff":
			refs = append(refs, buildGitDiffTool(td))
		default:
			if IsMCPToolName(td.Name) {
				refs = append(refs, buildMCPBridgeTool(td))
			}
		}
	}
	return refs
}

func buildReadFileTool(td ToolDefinition) genkitai.ToolRef {
	return genkitai.NewTool(
		td.Name,
		td.Description,
		func(ctx *genkitai.ToolContext, input ReadFileInput) (string, error) {
			cleanPath := filepath.Clean(input.Path)
			info, err := os.Stat(cleanPath)
			if err != nil {
				return "", fmt.Errorf("read_file: %w", err)
			}
			if info.IsDir() {
				return "", fmt.Errorf("read_file: path is a directory, not a file: %s", cleanPath)
			}
			content, err := os.ReadFile(cleanPath)
			if err != nil {
				return "", fmt.Errorf("read_file: %w", err)
			}
			return string(content), nil
		},
	)
}

func buildWriteFileTool(td ToolDefinition) genkitai.ToolRef {
	return genkitai.NewTool(
		td.Name,
		td.Description,
		func(ctx *genkitai.ToolContext, input WriteFileInput) (string, error) {
			cleanPath, err := ConfineToolPathWithContent(input.Path, input.Content)
			if err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			content, err := prepareWriteContent(cleanPath, input.Content)
			if err != nil {
				return "", err
			}
			dir := filepath.Dir(cleanPath)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return "", fmt.Errorf("write_file: create directory: %w", err)
			}
			// Atomic write: temp file + rename (matches TODO-1.6a pattern).
			tmpPath := cleanPath + ".tmp"
			if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
				return "", fmt.Errorf("write_file: write temp: %w", err)
			}
			_ = os.Remove(cleanPath) // Windows: rename fails if dest exists
			if err := os.Rename(tmpPath, cleanPath); err != nil {
				os.Remove(tmpPath)
				return "", fmt.Errorf("write_file: rename: %w", err)
			}
			NotifyFileMutation(cleanPath)
			return FormatWriteResult(input.Path, cleanPath, len(content)), nil
		},
	)
}

func buildEditFileTool(td ToolDefinition) genkitai.ToolRef {
	return genkitai.NewTool(
		td.Name,
		td.Description,
		func(ctx *genkitai.ToolContext, input EditFileInput) (string, error) {
			cleanPath, err := ConfineToolPath(input.Path)
			if err != nil {
				return "", fmt.Errorf("edit_file: %w", err)
			}
			content, err := os.ReadFile(cleanPath)
			if err != nil {
				return "", fmt.Errorf("edit_file: read: %w", err)
			}
			text := string(content)
			oldStr := input.OldString
			newStr := input.NewString

			count := strings.Count(text, oldStr)
			if count == 0 {
				return "", fmt.Errorf(
					"edit_file: old_string not found in %s. The file may have changed since last read — re-read the file and retry.",
					cleanPath,
				)
			}
			if count > 1 {
				return "", fmt.Errorf(
					"edit_file: old_string appears %d times in %s — provide more surrounding context to make the match unique.",
					count, cleanPath,
				)
			}

			replaced := strings.Replace(text, oldStr, newStr, 1)
			replaced, err = prepareWriteContent(cleanPath, replaced)
			if err != nil {
				return "", err
			}
			// Atomic write: temp file + rename.
			tmpPath := cleanPath + ".tmp"
			if err := os.WriteFile(tmpPath, []byte(replaced), 0644); err != nil {
				return "", fmt.Errorf("edit_file: write temp: %w", err)
			}
			_ = os.Remove(cleanPath)
			if err := os.Rename(tmpPath, cleanPath); err != nil {
				os.Remove(tmpPath)
				return "", fmt.Errorf("edit_file: rename: %w", err)
			}
			NotifyFileMutation(cleanPath)
			return fmt.Sprintf("Successfully edited %s: replaced old_string with new_string (1 occurrence).", cleanPath), nil
		},
	)
}

func buildPreciseEditTool(td ToolDefinition) genkitai.ToolRef {
	return genkitai.NewTool(
		td.Name,
		td.Description,
		func(ctx *genkitai.ToolContext, input PreciseEditInput) (string, error) {
			cleanPath, err := ConfineToolPath(input.Path)
			if err != nil {
				return "", fmt.Errorf("precise_edit: %w", err)
			}
			return runPreciseEdit(cleanPath, input.Anchor, input.OldBlock, input.NewBlock, input.Position)
		},
	)
}

func buildShellDoneTool(td ToolDefinition) genkitai.ToolRef {
	return genkitai.NewTool(
		td.Name,
		td.Description,
		func(ctx *genkitai.ToolContext, input ShellDoneInput) (string, error) {
			return runShellDone(ctx, input.Command, input.WorkingDir)
		},
	)
}

func buildGitDiffTool(td ToolDefinition) genkitai.ToolRef {
	return genkitai.NewTool(
		td.Name,
		td.Description,
		func(ctx *genkitai.ToolContext, input GitDiffInput) (string, error) {
			return runGitDiff(ctx, input.Path)
		},
	)
}

type mcpGenericInput map[string]any

func buildMCPBridgeTool(td ToolDefinition) genkitai.ToolRef {
	return genkitai.NewTool(
		td.Name,
		td.Description,
		func(ctx *genkitai.ToolContext, input mcpGenericInput) (string, error) {
			args := map[string]any(input)
			if args == nil {
				args = map[string]any{}
			}
			return executeToolByName(context.Background(), td.Name, args)
		},
	)
}
