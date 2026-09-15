package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/app"

	"gopkg.in/yaml.v3"
)

const defaultCLIActionsPath = "configs/cli_actions.yaml"

type cliActionsFile struct {
	Version      int              `yaml:"version"`
	Purpose      string           `yaml:"purpose"`
	RoutingRules []string         `yaml:"routing_rules"`
	Actions      []cliActionEntry `yaml:"actions"`
}

type cliActionEntry struct {
	ID                   string   `yaml:"id"`
	Command              string   `yaml:"command"`
	Summary              string   `yaml:"summary"`
	Safety               string   `yaml:"safety"`
	RequiresConfirmation bool     `yaml:"requires_confirmation"`
	UseWhen              []string `yaml:"use_when"`
}

func intentCLIActionPromptSection() string {
	actions, err := loadCLIActions(defaultCLIActionsPath)
	if err != nil || len(actions.Actions) == 0 {
		return ""
	}
	return formatCLIActionPromptSection(actions)
}

func loadCLIActions(path string) (cliActionsFile, error) {
	resolvedPath := strings.TrimSpace(path)
	if resolvedPath == "" {
		resolvedPath = defaultCLIActionsPath
	}
	if !filepath.IsAbs(resolvedPath) {
		resolvedPath = app.ResolveRuntimePath(resolvedPath)
	}
	content, err := os.ReadFile(filepath.Clean(resolvedPath))
	if err != nil {
		return cliActionsFile{}, err
	}
	var parsed cliActionsFile
	if err := yaml.Unmarshal(content, &parsed); err != nil {
		return cliActionsFile{}, err
	}
	return parsed, nil
}

func formatCLIActionPromptSection(actions cliActionsFile) string {
	var builder strings.Builder
	builder.WriteString("Structured CLI action map:\n")
	if purpose := strings.TrimSpace(actions.Purpose); purpose != "" {
		builder.WriteString("purpose: ")
		builder.WriteString(purpose)
		builder.WriteByte('\n')
	}
	if len(actions.RoutingRules) > 0 {
		builder.WriteString("routing_rules:\n")
		for _, rule := range actions.RoutingRules {
			if trimmed := strings.TrimSpace(rule); trimmed != "" {
				builder.WriteString("- ")
				builder.WriteString(trimmed)
				builder.WriteByte('\n')
			}
		}
	}
	builder.WriteString("actions:\n")
	for _, action := range actions.Actions {
		if strings.TrimSpace(action.ID) == "" || strings.TrimSpace(action.Command) == "" {
			continue
		}
		builder.WriteString(fmt.Sprintf("- id=%s | command=%s | safety=%s | confirmation=%t", strings.TrimSpace(action.ID), strings.TrimSpace(action.Command), strings.TrimSpace(action.Safety), action.RequiresConfirmation))
		if summary := strings.TrimSpace(action.Summary); summary != "" {
			builder.WriteString(" | summary=")
			builder.WriteString(summary)
		}
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}

func trimNonEmpty(values []string) []string {
	trimmedValues := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			trimmedValues = append(trimmedValues, trimmed)
		}
	}
	return trimmedValues
}
