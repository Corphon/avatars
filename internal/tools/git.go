package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultGitTimeoutMillis = 30000

type gitExecutor interface {
	Run(ctx context.Context, workingDir string, command []string) (string, error)
}

type execGitExecutor struct{}

func (execGitExecutor) Run(ctx context.Context, workingDir string, command []string) (string, error) {
	if len(command) == 0 {
		return "", errors.New("git command cannot be empty")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = workingDir
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

type GitTool struct {
	executor gitExecutor
}

func (GitTool) Name() string {
	return "git"
}

func (GitTool) IsConcurrencySafe(input any) bool {
	return true
}

func (t GitTool) Call(ctx context.Context, input any) (Result, error) {
	gitInput, err := normalizeGitInput(input)
	if err != nil {
		return Result{}, err
	}

	workingDir := filepath.Clean(gitInput.WorkingDir)
	info, err := os.Stat(workingDir)
	if err != nil {
		return Result{}, err
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("git tool requires a directory working dir, got file: %s", workingDir)
	}

	timeout := time.Duration(gitInput.TimeoutMillis) * time.Millisecond
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	executor := t.executor
	if executor == nil {
		executor = execGitExecutor{}
	}

	command := append([]string{"git"}, gitInput.Args...)
	output, err := executor.Run(callCtx, workingDir, command)
	trimmed := strings.TrimSpace(output)
	if err != nil {
		if trimmed == "" {
			trimmed = err.Error()
		} else {
			trimmed = trimmed + "\n" + err.Error()
		}
		return Result{Content: trimmed}, fmt.Errorf("git tool command failed: %w", err)
	}

	return Result{Content: trimmed}, nil
}

func normalizeGitInput(input any) (GitInput, error) {
	switch value := input.(type) {
	case GitInput:
		return finalizeGitInput(value)
	case *GitInput:
		if value == nil {
			return GitInput{}, errors.New("git tool input cannot be nil")
		}
		return finalizeGitInput(*value)
	default:
		return GitInput{}, errors.New("git tool expects tools.GitInput")
	}
}

func finalizeGitInput(input GitInput) (GitInput, error) {
	if len(input.Args) == 0 {
		return GitInput{}, errors.New("git tool args cannot be empty")
	}
	if strings.TrimSpace(input.WorkingDir) == "" {
		return GitInput{}, errors.New("git tool working dir cannot be empty")
	}
	if !isAllowedGitSubcommand(input.Args[0]) {
		return GitInput{}, fmt.Errorf("git tool only allows read-only subcommands: status, diff, log")
	}
	if input.TimeoutMillis <= 0 {
		input.TimeoutMillis = defaultGitTimeoutMillis
	}
	return input, nil
}

func isAllowedGitSubcommand(subcommand string) bool {
	switch strings.ToLower(strings.TrimSpace(subcommand)) {
	case "status", "diff", "log":
		return true
	default:
		return false
	}
}
