package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/app"
	"avatars/internal/planner"
	"avatars/internal/skillbuilder"
)

// runSkillNew creates a new skill proposal from a topic description and
// writes it to skills/generated/ for review. K2: CLI entry point for
// "avatars skills new <topic>".
//
// Usage:
//
//	avatars skills new "Go concurrency patterns for Builder"
//	avatars skills new --role builder "Go concurrency patterns"
func runSkillNew(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars skills new [--role <role>] \"<topic description>\"")
	}

	role := ""
	topic := ""

	// Parse optional --role flag.
	for i := 0; i < len(args); i++ {
		if args[i] == "--role" && i+1 < len(args) {
			role = strings.ToLower(strings.TrimSpace(args[i+1]))
			i++ // skip next arg
			continue
		}
		if !strings.HasPrefix(args[i], "--") {
			topic = args[i]
		}
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return errors.New("topic description is required")
	}

	// Bootstrap application for skill store access.
	application, err := app.Bootstrap()
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	defer func() { _ = application.Close() }()

	if role == "" {
		role = "builder" // default role
	}

	// Generate the proposal.
	proposal := skillbuilder.BuildForRole(role, topic, planner.Plan{}, "")
	markdown := proposal.Markdown()

	// Write to generated/ directory.
	generatedDir := filepath.Join(app.ResolveRuntimeHome(), "skills", "generated")
	if err := os.MkdirAll(generatedDir, 0755); err != nil {
		return fmt.Errorf("create generated dir: %w", err)
	}

	filename := proposal.Slug + ".md"
	destPath := filepath.Join(generatedDir, filename)
	if err := os.WriteFile(destPath, []byte(markdown), 0644); err != nil {
		return fmt.Errorf("write skill proposal: %w", err)
	}

	fmt.Printf("Skill proposal generated: %s\n", destPath)
	fmt.Printf("Role: %s | Slug: %s\n", role, proposal.Slug)
	fmt.Println("Review with: avatars skills review", filename)
	fmt.Println("Approve with: avatars skills approve", filename)
	return nil
}
