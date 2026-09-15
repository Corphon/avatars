package main

import (
	"sort"
	"strings"
	"sync"
)

// CommandInfo describes a registered avatars CLI command. L3: Used by
// nl_llm_router.go to get the command list instead of maintaining a
// separate hardcoded list that drifts from intent.go.
type CommandInfo struct {
	Name        string   // primary command name (e.g. "run", "skills")
	Subcommands []string // subcommand names (e.g. ["new", "generate", "approve"])
	Description string   // one-line description for LLM routing
	Category    string   // "task", "skills", "arch", "verify", "info"
}

// CommandRegistry holds all registered avatars commands. L3: Commands
// register themselves via Register(), and the NL router reads from
// KnownCommands() instead of a hardcoded list.
type CommandRegistry struct {
	mu       sync.RWMutex
	commands []CommandInfo
}

var globalRegistry = &CommandRegistry{}

// Register adds a command to the global registry. Call during init() or
// at command registration time. L3.
func RegisterCommand(info CommandInfo) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.commands = append(globalRegistry.commands, info)
}

// KnownCommands returns a sorted copy of all registered commands. L3.
func KnownCommands() []CommandInfo {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()
	result := make([]CommandInfo, len(globalRegistry.commands))
	copy(result, globalRegistry.commands)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// KnownCommandNames returns a flat list of command names for LLM routing. L3.
func KnownCommandNames() []string {
	cmds := KnownCommands()
	names := make([]string, 0, len(cmds)*2)
	for _, cmd := range cmds {
		names = append(names, cmd.Name)
		for _, sub := range cmd.Subcommands {
			names = append(names, cmd.Name+" "+sub)
		}
	}
	return names
}

// FormatCommandList returns a human-readable command list for the NL
// classifier prompt. L3: Replaces the hardcoded knownAvatarsCommands.
func FormatCommandList() string {
	cmds := KnownCommands()
	var sb strings.Builder
	sb.WriteString("Available avatars commands:\n")
	for _, cmd := range cmds {
		sb.WriteString("- ")
		sb.WriteString(cmd.Name)
		if len(cmd.Subcommands) > 0 {
			sb.WriteString(" (" + strings.Join(cmd.Subcommands, ", ") + ")")
		}
		if cmd.Description != "" {
			sb.WriteString(": " + cmd.Description)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// init registers the core avatars commands. L3: This is the single source
// of truth for the command list — nl_llm_router.go reads from here instead
// of maintaining a parallel hardcoded list that drifts from intent.go.
func init() {
	RegisterCommand(CommandInfo{
		Name: "run", Description: "Execute a task using the full avatar pipeline",
		Subcommands: []string{}, Category: "task",
	})
	RegisterCommand(CommandInfo{
		Name: "skills", Description: "Manage avatar skills (generate, approve, review, list)",
		Subcommands: []string{"new", "generate", "approve", "import", "archive", "disable",
			"restore", "list", "pending", "review", "archived", "disabled",
			"timeline", "ledger", "reconcile", "sync", "show", "status", "promotion"},
		Category: "skills",
	})
	RegisterCommand(CommandInfo{
		Name: "arch", Description: "Analyze or initialize project architecture",
		Subcommands: []string{"analyze", "init", "check"}, Category: "arch",
	})
	RegisterCommand(CommandInfo{
		Name: "verify", Description: "Run verification on generated code",
		Subcommands: []string{}, Category: "verify",
	})
	RegisterCommand(CommandInfo{
		Name: "serve", Description: "Start the operator/event dashboard (not the creative gallery)",
		Subcommands: []string{}, Category: "info",
	})
	RegisterCommand(CommandInfo{
		Name: "stage", Description: "Creative HTML gallery: visualize an idea or project (not code delivery; not avatars serve)",
		Subcommands: []string{}, Category: "info",
	})
}
