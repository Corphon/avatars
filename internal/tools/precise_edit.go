package tools

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PreciseEditInput describes a single deterministic insertion into an existing file.
// Unlike PatchTool (which replaces Old→New), PreciseEditTool inserts Content at an
// anchor point without removing any existing text. This is the tool for integration
// wiring tasks: adding imports, list entries, switch cases, or dropdown options.
type PreciseEditInput struct {
	FilePath   string `json:"file_path"`   // relative path (resolved against WorkingDir)
	Anchor     string `json:"anchor"`      // text that marks the insertion point
	Content    string `json:"content"`     // text to insert
	Position   string `json:"position"`    // "before" or "after" the anchor line
	IfMissing  bool   `json:"if_missing"`  // skip when Content is already present (idempotent)
	WorkingDir string `json:"working_dir"` // base directory for relative paths
}

type PreciseEditTool struct{}

func (PreciseEditTool) Name() string {
	return "precise_edit"
}

func (PreciseEditTool) IsConcurrencySafe(input any) bool {
	return false
}

func (PreciseEditTool) Call(ctx context.Context, input any) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	in, err := normalizePreciseEditInput(input)
	if err != nil {
		return Result{}, err
	}

	workingDir, err := filepath.Abs(filepath.Clean(in.WorkingDir))
	if err != nil {
		return Result{}, err
	}
	info, err := os.Stat(workingDir)
	if err != nil {
		return Result{}, err
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("precise_edit tool requires a directory working dir, got file: %s", workingDir)
	}

	targetPath, err := resolveWriteTarget(workingDir, in.FilePath)
	if err != nil {
		return Result{}, err
	}

	original, err := os.ReadFile(targetPath)
	if err != nil {
		return Result{}, fmt.Errorf("precise_edit cannot read %s: %w", in.FilePath, err)
	}

	// PE-3: Snapshot original state for safety verification and potential rollback.
	originalSize := len(original)

	// Idempotency: skip if content already present.
	if in.IfMissing && strings.Contains(string(original), strings.TrimSpace(in.Content)) {
		return Result{Content: fmt.Sprintf("precise_edit: content already present in %s (skipped) | size: %d bytes", in.FilePath, originalSize)}, nil
	}

	lines := strings.Split(string(original), "\n")
	trimmedAnchor := strings.TrimSpace(in.Anchor)

	// Find anchor — first try exact line match, then substring match.
	anchorIdx := -1
	matchType := ""
	for i, line := range lines {
		if strings.TrimSpace(line) == trimmedAnchor {
			if anchorIdx >= 0 {
				return Result{}, fmt.Errorf(
					"precise_edit: anchor found multiple times in %s (lines %d and %d); use a more specific anchor",
					in.FilePath, anchorIdx+1, i+1)
			}
			anchorIdx = i
			matchType = "exact"
		}
	}

	// Fallback: substring match (only if exact match found zero or one result).
	if anchorIdx < 0 {
		for i, line := range lines {
			if strings.Contains(line, trimmedAnchor) {
				if anchorIdx >= 0 {
					return Result{}, fmt.Errorf(
						"precise_edit: anchor substring found multiple times in %s (lines %d and %d); use a more specific anchor",
						in.FilePath, anchorIdx+1, i+1)
				}
				anchorIdx = i
				matchType = "substring"
			}
		}
	}

	if anchorIdx < 0 {
		return Result{}, fmt.Errorf(
			"precise_edit: anchor not found in %s: %q", in.FilePath, truncateForError(trimmedAnchor, 80))
	}

	// Determine insertion point.
	var insertAt int
	if in.Position == "before" {
		insertAt = anchorIdx
	} else {
		insertAt = anchorIdx + 1
	}

	// Build new content.
	contentLines := strings.Split(strings.TrimRight(in.Content, "\n"), "\n")
	newLines := make([]string, 0, len(lines)+len(contentLines))
	newLines = append(newLines, lines[:insertAt]...)
	newLines = append(newLines, contentLines...)
	newLines = append(newLines, lines[insertAt:]...)

	updated := strings.Join(newLines, "\n")
	// SEC-2: Atomic write via temp file.
	tmp, err := os.CreateTemp(filepath.Dir(targetPath), filepath.Base(targetPath)+".avatars-tmp-*")
	if err != nil {
		return Result{}, fmt.Errorf("precise_edit temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write([]byte(updated)); err != nil {
		tmp.Close()
		return Result{}, fmt.Errorf("precise_edit write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Result{}, err
	}
	if err := tmp.Close(); err != nil {
		return Result{}, err
	}

	// PE-3: Verify on temp before touching real file.
	written, err := os.ReadFile(tmpPath)
	if err != nil {
		return Result{}, fmt.Errorf("precise_edit re-read temp: %w", err)
	}
	if len(written) < originalSize {
		return Result{}, fmt.Errorf("precise_edit SAFETY BLOCK: size %d < %d in %s", len(written), originalSize, in.FilePath)
	}

	// VG-2: Compile/lint check on temp.
	if errMsg := validateFileAfterEdit(tmpPath, in.FilePath); errMsg != "" {
		return Result{}, fmt.Errorf("precise_edit COMPILE BLOCK: %s", errMsg)
	}

	// Atomic rename.
	_ = os.Remove(targetPath)
	if err := os.Rename(tmpPath, targetPath); err != nil {
		tmpData, _ := os.ReadFile(tmpPath)
		os.WriteFile(targetPath, tmpData, 0o644)
	}

	addedBytes := len(written) - originalSize
	return Result{Content: fmt.Sprintf(
		"precise_edit: inserted %d line(s) %s line %d (%s match) in %s | size: %d->%d (+%d bytes)",
		len(contentLines), in.Position, anchorIdx+1, matchType, in.FilePath,
		originalSize, len(written), addedBytes)}, nil
	}

func normalizePreciseEditInput(input any) (PreciseEditInput, error) {
	switch value := input.(type) {
	case PreciseEditInput:
		return finalizePreciseEditInput(value)
	case *PreciseEditInput:
		if value == nil {
			return PreciseEditInput{}, errors.New("precise_edit tool input cannot be nil")
		}
		return finalizePreciseEditInput(*value)
	default:
		return PreciseEditInput{}, errors.New("precise_edit tool expects tools.PreciseEditInput")
	}
}

func finalizePreciseEditInput(input PreciseEditInput) (PreciseEditInput, error) {
	if strings.TrimSpace(input.FilePath) == "" {
		return PreciseEditInput{}, errors.New("precise_edit tool file_path cannot be empty")
	}
	if strings.TrimSpace(input.Anchor) == "" {
		return PreciseEditInput{}, errors.New("precise_edit tool anchor cannot be empty")
	}
	if strings.TrimSpace(input.Content) == "" {
		return PreciseEditInput{}, errors.New("precise_edit tool content cannot be empty")
	}
	if input.Position != "before" && input.Position != "after" {
		return PreciseEditInput{}, fmt.Errorf("precise_edit tool position must be 'before' or 'after', got %q", input.Position)
	}
	if strings.TrimSpace(input.WorkingDir) == "" {
		return PreciseEditInput{}, errors.New("precise_edit tool working_dir cannot be empty")
	}
	return input, nil
}

// DiffAndApplyEdits compares original and proposed file content, extracts
// only the NEW lines (not present in original), finds their anchor context
// from surrounding original lines, and applies each insertion via PreciseEditTool.
//
// Prefer SurgicalApplyFromDump for full-file dumps — it also handles replace hunks.
// This helper remains the insert-only engine used by SurgicalApplyFromDump.
//
// Returns the number of insertions applied.
func DiffAndApplyEdits(originalContent, proposedContent, filePath, workingDir string) (int, error) {
	originalLines := strings.Split(originalContent, "\n")
	proposedLines := strings.Split(proposedContent, "\n")

	// Build lookup set of original lines (trimmed).
	originalSet := make(map[string]bool, len(originalLines))
	for _, line := range originalLines {
		originalSet[strings.TrimSpace(line)] = true
	}

	type insertion struct {
		anchor   string
		content  string
		position string
	}

	var insertions []insertion
	var pending []string
	var anchor string

	for _, line := range proposedLines {
		trimmed := strings.TrimSpace(line)
		if originalSet[trimmed] {
			// Existing line — flush pending block if any.
			if len(pending) > 0 && anchor != "" {
				insertions = append(insertions, insertion{
					anchor:   anchor,
					content:  strings.Join(pending, "\n"),
					position: "after",
				})
				pending = nil
			}
			anchor = line
		} else if trimmed != "" {
			// New line — collect into pending block.
			if anchor != "" {
				pending = append(pending, line)
			}
		}
	}
	// Flush trailing block.
	if len(pending) > 0 && anchor != "" {
		insertions = append(insertions, insertion{
			anchor:   anchor,
			content:  strings.Join(pending, "\n"),
			position: "after",
		})
	}

	applied := 0
	tool := PreciseEditTool{}
	for _, ins := range insertions {
		if strings.TrimSpace(ins.content) == "" {
			continue
		}
		_, err := tool.Call(context.Background(), PreciseEditInput{
			FilePath:   filePath,
			Anchor:     ins.anchor,
			Content:    ins.content,
			Position:   ins.position,
			IfMissing:  true,
			WorkingDir: workingDir,
		})
		if err != nil {
			return applied, fmt.Errorf("diff-apply %s: %w", filePath, err)
		}
		applied++
	}
	return applied, nil
}

func truncateForError(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// validateFileAfterEdit runs a language-appropriate syntax/compile check on a file
// after it has been edited. Returns empty string if the file is valid, or an error
// message if the file fails validation (syntax error, compilation error, etc.).
func validateFileAfterEdit(filePath string, displayPath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	pkgDir := filepath.Dir(filePath)

	switch ext {
	case ".go":
		// Tier 1: Always run go/parser on the specific file content.
		// This catches truncated/broken files regardless of extension
		// (critical for atomic write temp files which lack .go suffix).
		content, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return fmt.Sprintf("cannot read file for check: %v", readErr)
		}
		fset := token.NewFileSet()
		if _, parseErr := parser.ParseFile(fset, filePath, content, parser.ParseComments); parseErr != nil {
			return fmt.Sprintf("go syntax error: %s", parseErr.Error())
		}
		// Tier 2: If go.mod exists and file is real .go (not temp),
		// also run package-level compilation check.
		if _, modErr := os.Stat(filepath.Join(pkgDir, "go.mod")); modErr == nil {
			if strings.HasSuffix(filePath, ".go") {
				cmd := exec.Command("go", "build", ".")
				cmd.Dir = pkgDir
				if out, err := cmd.CombinedOutput(); err != nil {
					return fmt.Sprintf("go build error: %s", strings.TrimSpace(string(out)))
				}
				testCmd := exec.Command("go", "test", "-run", "^$", ".")
				testCmd.Dir = pkgDir
				if out, err := testCmd.CombinedOutput(); err != nil {
					return fmt.Sprintf("go test compile error: %s", strings.TrimSpace(string(out)))
				}
			}
		}
		case ".rs":
		cmd := exec.Command("cargo", "check")
		cmd.Dir = pkgDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Sprintf("cargo check error: %s", strings.TrimSpace(string(out)))
		}
	case ".py":
		cmd := exec.Command("python", "-m", "py_compile", filePath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Sprintf("python syntax error: %s", strings.TrimSpace(string(out)))
		}
	case ".js", ".mjs":
		cmd := exec.Command("node", "--check", filePath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Sprintf("javascript syntax error: %s", strings.TrimSpace(string(out)))
		}
	case ".ts", ".tsx":
		cmd := exec.Command("npx", "tsc", "--noEmit", filePath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Sprintf("typescript error: %s", strings.TrimSpace(string(out)))
		}
	case ".c", ".h":
		cmd := exec.Command("gcc", "-fsyntax-only", filePath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Sprintf("C syntax error: %s", strings.TrimSpace(string(out)))
		}
	case ".cpp", ".cc", ".cxx", ".hpp":
		cmd := exec.Command("g++", "-fsyntax-only", filePath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Sprintf("C++ syntax error: %s", strings.TrimSpace(string(out)))
		}
	case ".sh":
		cmd := exec.Command("bash", "-n", filePath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Sprintf("shell syntax error: %s", strings.TrimSpace(string(out)))
		}
	}
	// For unknown extensions, skip validation (no tool available).
	return ""
}
