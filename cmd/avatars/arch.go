package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"avatars/internal/arch"
)

// runArch handles the "avatars arch" command.
// Sub-modes:
//
//	avatars arch --init [description]     New project: design architecture from intent
//	avatars arch --analyze                Existing project: reverse-engineer architecture
//	avatars arch --resume                 Unfinished project: compare target vs current
//	avatars arch --mark-stale             Manually mark architecture.md as stale
//	avatars arch --show                   Display current architecture.md
//	avatars arch --check                  Check if architecture.md is stale
func runArch(args []string) error {
	var (
		mode     string // init, analyze, resume
		markStale bool
		showDoc   bool
		checkOnly bool
		focusTopic string // --focus: guide the LLM toward specific registration patterns
		userDesc  string
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--init":
			mode = "init"
		case "--analyze":
			mode = "analyze"
		case "--resume":
			mode = "resume"
		case "--focus":
			if i+1 >= len(args) {
				return errors.New("arch --focus requires a topic, e.g. --focus \"LLM provider registration\"")
			}
			i++
			focusTopic = strings.TrimSpace(args[i])
			if focusTopic == "" {
				return errors.New("arch --focus requires a non-empty topic")
			}
		case "--mark-stale":
			markStale = true
		case "--show":
			showDoc = true
		case "--check":
			checkOnly = true
		case "--help", "-h":
			fmt.Fprint(os.Stderr, archUsage)
			return nil
		default:
			if !strings.HasPrefix(args[i], "-") {
				userDesc = strings.TrimSpace(args[i])
			}
		}
	}

	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("arch: getwd: %w", err)
	}

	// --show: display current architecture.md.
	if showDoc {
		return showArchDoc(root)
	}

	// --check: check staleness.
	if checkOnly {
		return checkArchDoc(root)
	}

	// --mark-stale: manually mark as stale.
	if markStale {
		return markArchStale(root)
	}

	// Default to --analyze if no mode specified.
	if mode == "" {
		mode = "analyze"
	}

	// --init requires a description.
	if mode == "init" && userDesc == "" {
		fmt.Fprint(os.Stderr, "Enter a description of the project you want to build:\n> ")
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			userDesc = strings.TrimSpace(scanner.Text())
		}
		if userDesc == "" {
			return errors.New("arch --init requires a project description")
		}
	}

	return runArchMode(root, mode, userDesc, focusTopic)
}

// runArchMode executes the architecture analysis pipeline for the given mode.
func runArchMode(root, mode, userDesc, focusTopic string) error {
	// Phase 1: Scan or prepare input.
	fmt.Fprintf(os.Stderr, "\U0001F4E1 Scanning project: %s\n", root)
	scan, err := arch.ScanProject(root)
	if err != nil {
		return fmt.Errorf("arch scan: %w", err)
	}

	if mode == "init" && scan.FileStats.TotalFiles == 0 {
		scan = arch.NewScanForInit(root)
	}

	fmt.Fprintf(os.Stderr, "  Found %d files, %d entry candidates, %d dependency manifests\n",
		scan.FileStats.TotalFiles, len(scan.EntryCandidates), len(scan.DepManifests))

	// Phase 2: Load existing doc for --resume.
	var existingDoc *arch.ArchDoc
	if mode == "resume" {
		existingDoc, _ = arch.ReadArchDoc(root)
		if existingDoc == nil {
			fmt.Fprintf(os.Stderr, "  No existing architecture.md found — falling back to --analyze mode\n")
			mode = "analyze"
		} else {
			fmt.Fprintf(os.Stderr, "  Loaded existing architecture.md (status: %s, generated: %s)\n",
				existingDoc.Meta.Status, existingDoc.Meta.GeneratedAt)
		}
	}

	// Phase 3: Invoke LLM to generate/update architecture.
	fmt.Fprintf(os.Stderr, "⌛ Analyzing architecture with LLM...\n")

	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil {
		// LLM unavailable — fall back to what we have.
		if existingDoc != nil {
			fmt.Fprintf(os.Stderr, "  LLM unavailable, displaying existing architecture document.\n")
			fmt.Println(arch.FormatArchDoc(existingDoc))
			return nil
		}
		return fmt.Errorf("arch: no LLM client available and no existing architecture.md: %w", err)
	}
	defer cleanup()

	ctx := context.Background()

	if focusTopic != "" {
		fmt.Fprintf(os.Stderr, "  Focus: %s\n", focusTopic)
	}
	doc, err := arch.GenerateArchDoc(ctx, client, scan, mode, userDesc, existingDoc, focusTopic)
	if err != nil {
		return fmt.Errorf("arch generate: %w", err)
	}
	// Always enrich with automated DataFlow analysis (import graph, key types).
	doc.DataFlow = arch.AnalyzeDataFlow(root)

	// Phase 4: For --resume, merge with existing.
	if mode == "resume" && existingDoc != nil {
		doc = arch.MergeArchDoc(existingDoc, doc)
	}

	// Phase 5: Display draft and prompt for confirmation.
	fmt.Fprintf(os.Stderr, "\n--- Draft architecture.md ---\n")
	fmt.Println(arch.FormatArchDoc(doc))
	fmt.Fprintf(os.Stderr, "--- End of draft ---\n\n")

	fmt.Fprintf(os.Stderr, "Review the architecture above.\n")
	fmt.Fprintf(os.Stderr, "[c]onfirm / [e]dit manually / [r]eject: ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return errors.New("arch: no input")
	}
	choice := strings.ToLower(strings.TrimSpace(scanner.Text()))

	switch {
	case choice == "c" || choice == "confirm":
		if err := arch.ConfirmArchDoc(root, doc); err != nil {
			return fmt.Errorf("arch confirm: %w", err)
		}
		fmt.Fprintf(os.Stderr, "✅ architecture.md confirmed and saved.\n")
		fmt.Fprintf(os.Stderr, "   Run `avatars arch --show` to view, or just continue working — avatars will use it automatically.\n")

	case choice == "e" || choice == "edit":
		// Write as draft, tell user to edit.
		if err := arch.WriteArchDoc(root, doc); err != nil {
			return fmt.Errorf("arch write draft: %w", err)
		}
		fmt.Fprintf(os.Stderr, "📝 architecture.md written as draft. Edit it, then run `avatars arch --analyze` to refresh.\n")

	default:
		fmt.Fprintf(os.Stderr, "❌ Architecture document rejected. No file written.\n")
		fmt.Fprintf(os.Stderr, "   Run `avatars arch --analyze` to try again with different parameters.\n")
	}

	return nil
}

// showArchDoc reads and displays the current architecture.md.
func showArchDoc(root string) error {
	doc, err := arch.ReadArchDoc(root)
	if err != nil {
		return fmt.Errorf("arch show: %w", err)
	}
	if doc == nil {
		fmt.Println("No architecture.md found in this project.")
		fmt.Println(arch.SuggestArchCommand())
		return nil
	}

	// Check staleness and show status.
	check := arch.CheckStale(root, doc)
	fmt.Fprintf(os.Stderr, "Status: %s", doc.Meta.Status)
	if check.IsStale {
		fmt.Fprintf(os.Stderr, " ⚠️ STALE — %s", check.Details)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr)
	fmt.Println(arch.FormatArchDoc(doc))
	return nil
}

// checkArchDoc checks if architecture.md exists and is stale.
func checkArchDoc(root string) error {
	doc, err := arch.ReadArchDoc(root)
	if err != nil {
		return fmt.Errorf("arch check: %w", err)
	}
	if doc == nil {
		fmt.Println("status: missing")
		fmt.Println(arch.SuggestArchCommand())
		return nil
	}

	check := arch.CheckStale(root, doc)
	fmt.Printf("status: %s\n", doc.Meta.Status)
	fmt.Printf("generated: %s\n", doc.Meta.GeneratedAt)
	if check.IsStale {
		fmt.Printf("stale: true — %s\n", check.Details)
	} else {
		fmt.Println("stale: false")
	}
	return nil
}

// markArchStale manually marks architecture.md as stale.
func markArchStale(root string) error {
	doc, err := arch.ReadArchDoc(root)
	if err != nil {
		return fmt.Errorf("arch mark-stale: %w", err)
	}
	if doc == nil {
		return errors.New("no architecture.md found to mark stale")
	}
	if err := arch.MarkStale(root, doc); err != nil {
		return fmt.Errorf("arch mark-stale: %w", err)
	}
	fmt.Println("✅ architecture.md marked as stale.")
	return nil
}

const archUsage = `Usage: avatars arch [--init | --analyze | --resume] [description]

Generate or update an architecture.md document that helps avatars understand
your project structure. This enables better multi-file changes and project-level
reasoning.

Modes:
  --init [description]   Design architecture for a new project from your intent.
  --analyze              Reverse-engineer architecture from existing code. (default)
  --resume               Compare target architecture with current state, identify gaps.
  --focus <topic>        Guide LLM to look for specific registration patterns.
                         Example: --focus "LLM provider registration"

Management:
  --show                 Display the current architecture.md.
  --check                Check if architecture.md is stale.
  --mark-stale           Manually mark the document as stale.

Examples:
  avatars arch --analyze
      Analyze the current project and generate architecture.md.

  avatars arch --analyze --focus "LLM provider registration"
      Analyze, paying special attention to how new LLM providers are registered.

  avatars arch --init "A Go CLI tool for task management"
      Design architecture for a new CLI project.

  avatars arch --resume
      Compare existing architecture.md with current project state.

  avatars arch --show
      Display current architecture document.

The architecture.md is a persistent project asset. It helps avatars:
  - Understand your project structure without re-scanning every time.
  - Know which files to modify when adding a new component (Registration Points).
  - Follow project conventions and dependency rules.
`
