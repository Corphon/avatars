package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type PatchTool struct{}

func (PatchTool) Name() string {
	return "patch"
}

func (PatchTool) IsConcurrencySafe(input any) bool {
	return false
}

func (PatchTool) Call(ctx context.Context, input any) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	patchInput, err := normalizePatchInput(input)
	if err != nil {
		return Result{}, err
	}

	workingDir, err := filepath.Abs(filepath.Clean(patchInput.WorkingDir))
	if err != nil {
		return Result{}, err
	}
	workingDirInfo, err := os.Stat(workingDir)
	if err != nil {
		return Result{}, err
	}
	if !workingDirInfo.IsDir() {
		return Result{}, fmt.Errorf("patch tool requires a directory working dir, got file: %s", workingDir)
	}

	targetPath, err := resolveWriteTarget(workingDir, patchInput.Path)
	if err != nil {
		return Result{}, err
	}
	content, err := os.ReadFile(targetPath)
	if err != nil {
		return Result{}, err
	}

	text := string(content)
	matchCount := strings.Count(text, patchInput.Old)
	if matchCount == 0 {
		return Result{}, fmt.Errorf("patch tool could not find target text in %s", targetPath)
	}

	replacements := 1
	if patchInput.ReplaceAll {
		replacements = -1
	} else if matchCount != 1 {
		return Result{}, fmt.Errorf("patch tool requires exactly one match when replace-all is disabled; found %d matches in %s", matchCount, targetPath)
	}

	updated := strings.Replace(text, patchInput.Old, patchInput.New, replacements)
	// SEC-2: Atomic write via temp file (match write.go's pattern).
	if err := writeFileAtomicTool(targetPath, []byte(updated), 0o644); err != nil {
		return Result{}, err
	}

	appliedCount := matchCount
	if !patchInput.ReplaceAll {
		appliedCount = 1
	}
	return Result{Content: fmt.Sprintf("Patched %d occurrence(s) in %s", appliedCount, targetPath)}, nil
}

func normalizePatchInput(input any) (PatchInput, error) {
	switch value := input.(type) {
	case PatchInput:
		return finalizePatchInput(value)
	case *PatchInput:
		if value == nil {
			return PatchInput{}, errors.New("patch tool input cannot be nil")
		}
		return finalizePatchInput(*value)
	default:
		return PatchInput{}, errors.New("patch tool expects tools.PatchInput")
	}
}

func finalizePatchInput(input PatchInput) (PatchInput, error) {
	if strings.TrimSpace(input.Path) == "" {
		return PatchInput{}, errors.New("patch tool path cannot be empty")
	}
	if strings.TrimSpace(input.WorkingDir) == "" {
		return PatchInput{}, errors.New("patch tool working dir cannot be empty")
	}
	if input.Old == "" {
		return PatchInput{}, errors.New("patch tool old text cannot be empty")
	}
	return input, nil
}
