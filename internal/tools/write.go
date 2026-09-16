package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/writejail"
)

type WriteTool struct{}

func (WriteTool) Name() string {
	return "write"
}

func (WriteTool) IsConcurrencySafe(input any) bool {
	return false
}

func (WriteTool) Call(ctx context.Context, input any) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	writeInput, err := normalizeWriteInput(input)
	if err != nil {
		return Result{}, err
	}

	workingDir, err := filepath.Abs(filepath.Clean(writeInput.WorkingDir))
	if err != nil {
		return Result{}, err
	}
	workingDirInfo, err := os.Stat(workingDir)
	if err != nil {
		return Result{}, err
	}
	if !workingDirInfo.IsDir() {
		return Result{}, fmt.Errorf("write tool requires a directory working dir, got file: %s", workingDir)
	}

	targetPath, err := resolveWriteTarget(workingDir, writeInput.Path)
	if err != nil {
		return Result{}, err
	}

	allowOverwrite := writeInput.Overwrite || shouldAutoOverwriteWrite(writeInput, targetPath)
	if !allowOverwrite {
		if _, err := os.Stat(targetPath); err == nil {
			return Result{}, fmt.Errorf("write tool refuses to overwrite existing file: %s (pass Overwrite=true, or set Intent/ExpectedTargets for an explicit edit)", targetPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return Result{}, err
		}
	} else if !writeInput.Overwrite {
		// Soft auto-overwrite: keep auditability without failing the user.
		writeInput.Overwrite = true
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return Result{}, err
	}
	// P1-fix: Atomic write via temp file (prevents truncation on interrupted
	// writes). Write to temp, validate on temp, then atomic rename.
	tmp, err := os.CreateTemp(filepath.Dir(targetPath), filepath.Base(targetPath)+".avatars-tmp-*")
	if err != nil {
		return Result{}, fmt.Errorf("write temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write([]byte(writeInput.Content)); err != nil {
		tmp.Close()
		return Result{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Result{}, err
	}
	if err := tmp.Close(); err != nil {
		return Result{}, err
	}

	// Validate on temp file first (before touching target).
	if errMsg := validateFileAfterEdit(tmpPath, writeInput.Path); errMsg != "" {
		return Result{}, fmt.Errorf(
			"write COMPILE BLOCK: file has syntax errors — rolled back\n%s: %s",
			writeInput.Path, errMsg)
	}

	// Atomic replace: remove existing target (Windows requires this), then rename.
	_ = os.Remove(targetPath)
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return Result{}, fmt.Errorf("write atomic rename: %w", err)
	}

	return Result{Content: fmt.Sprintf("Wrote %d bytes to %s", len(writeInput.Content), targetPath)}, nil
}

func normalizeWriteInput(input any) (WriteInput, error) {
	switch value := input.(type) {
	case WriteInput:
		return finalizeWriteInput(value)
	case *WriteInput:
		if value == nil {
			return WriteInput{}, errors.New("write tool input cannot be nil")
		}
		return finalizeWriteInput(*value)
	default:
		return WriteInput{}, errors.New("write tool expects tools.WriteInput")
	}
}

func finalizeWriteInput(input WriteInput) (WriteInput, error) {
	if strings.TrimSpace(input.Path) == "" {
		return WriteInput{}, errors.New("write tool path cannot be empty")
	}
	if strings.TrimSpace(input.WorkingDir) == "" {
		return WriteInput{}, errors.New("write tool working dir cannot be empty")
	}
	return input, nil
}

// shouldAutoOverwriteWrite allows replacing an existing file when the caller
// clearly meant an edit (Builder Intent / ExpectedTargets) or when refining
// stage gallery assets. Bare writes without intent still refuse overwrite.
func shouldAutoOverwriteWrite(input WriteInput, targetPath string) bool {
	slash := filepath.ToSlash(targetPath)
	base := filepath.Base(targetPath)
	for _, t := range input.ExpectedTargets {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		ts := filepath.ToSlash(t)
		if ts == slash || filepath.Base(ts) == base || strings.HasSuffix(slash, "/"+ts) || strings.HasSuffix(slash, ts) {
			return true
		}
	}
	intent := strings.ToLower(strings.TrimSpace(input.Intent))
	if intent != "" {
		for _, tok := range []string{"edit", "modify", "update", "builder", "fix", "refine", "修改", "更新", "编辑", "改"} {
			if strings.Contains(intent, tok) {
				return true
			}
		}
	}
	// Stage creative HTML/CSS/JSON: iterative refine is the normal workflow.
	if strings.Contains(slash, "/stage/") || strings.HasPrefix(slash, "stage/") {
		switch strings.ToLower(filepath.Ext(targetPath)) {
		case ".html", ".css", ".json":
			return true
		}
	}
	return false
}

func resolveWriteTarget(workingDir string, path string) (string, error) {
	workingDir = filepath.Clean(workingDir)
	candidate := strings.TrimSpace(path)
	if candidate == "" {
		return "", fmt.Errorf("write tool target escapes sandbox: empty path")
	}
	abs, err := writejail.Confine(workingDir, candidate)
	if err != nil {
		return "", fmt.Errorf("write tool target escapes sandbox: %s", path)
	}
	return abs, nil
}
