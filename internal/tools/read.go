package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type ReadTool struct{}

func (ReadTool) Name() string {
	return "read"
}

func (ReadTool) IsConcurrencySafe(input any) bool {
	return true
}

func (ReadTool) Call(ctx context.Context, input any) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	readInput, err := normalizeReadInput(input)
	if err != nil {
		return Result{}, err
	}

	cleanPath := filepath.Clean(readInput.Path)
	info, err := os.Stat(cleanPath)
	if err != nil {
		return Result{}, err
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("read tool requires a file path, got directory: %s", cleanPath)
	}

	content, err := os.ReadFile(cleanPath)
	if err != nil {
		return Result{}, err
	}

	return Result{Content: string(content)}, nil
}

func normalizeReadInput(input any) (ReadInput, error) {
	switch value := input.(type) {
	case ReadInput:
		if value.Path == "" {
			return ReadInput{}, errors.New("read tool path cannot be empty")
		}
		return value, nil
	case *ReadInput:
		if value == nil || value.Path == "" {
			return ReadInput{}, errors.New("read tool path cannot be empty")
		}
		return *value, nil
	default:
		return ReadInput{}, errors.New("read tool expects tools.ReadInput")
	}
}
