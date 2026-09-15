package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"avatars/internal/platform"
)

// replSession and its accessors moved to repl_session.go (M1 refactoring).
// Backward-compatible wrappers delegate to a package-level defaultSession.

const usageText = "usage: avatars [<intent>] | avatars intent [--choose <n>] [--confirm] [--from-file <path>] \"<intent>\" | avatars run [--task <task-id>] [--new-task] [--resume <transcript-path>] [--permission-mode <mode>] [--from-file <path>] \"<task>\" | avatars repl [--task <task-id>] [--new-task] | avatars bootstrap [--apply] [--name <name>] [--module <module>] [--stack go-cli|python-cli|node-cli|rust-cli] | avatars script [--apply] <path> [description] | avatars edit [--apply] <path> <instruction> | avatars edit-many [--apply] <instruction> | avatars resume <transcript-path> | avatars tasks list | avatars tasks show <task-id> [--view planner|synthesizer|verifier|governance|avatar:<avatar-id>] | avatars tasks retry-node <task-id> --origin-run <run-id> --pause-point <pause-point-id> --node <node-id> --pause-digest <digest> --dry-run | avatars tasks delete <task-id> | avatars memory status [--task <task-id>] | avatars memory maintain --dry-run [--task <task-id>] | avatars memory maintain --apply --confirm-archive [--task <task-id>] | avatars memory archive-status [--task <task-id>] | avatars memory archive-list [--task <task-id>] | avatars memory archive-restore --dry-run --tombstone <tombstone-id> [--task <task-id>] | avatars rollback inspect --artifact <path> | avatars rollback apply --artifact <path> --confirm | avatars governance status [--task <task-id>] | avatars feedback import-diagnostics <task-id> <json-file> | avatars serve [--task <task-id>] [--resume <transcript-path>] [--permission-mode <mode>] [task] | avatars verify [--race] [--task <task-id>] | avatars llm providers | avatars llm show <provider> | avatars smoke repl-routing [--task <task-id>] [--new-task] | avatars smoke coding-gate | avatars actions list [--path <cli-actions.yaml>] | avatars actions show <action-id> [--path <cli-actions.yaml>] | avatars route [--task <task-id>] [--new-task] \"<intent>\" | avatars shell [--permission-mode <mode>] <cmd> [args...] | avatars write [--overwrite] [--permission-mode <mode>] <path> <content> | avatars patch [--replace-all] [--permission-mode <mode>] <path> <old> <new> | avatars git <subcommand> [args...] | avatars mcp list | avatars mcp show <server-name> | avatars mcp inspect [--task <task-id>] <server-name-or-url> | avatars mcp call [--task <task-id>] <server-name-or-url> <method> [params-json] | avatars plugins list | avatars skills generate [--task <task-id>] [--from-file <path>] \"<task>\" | avatars skills approve <generated-file> | avatars skills archive <approved-file> | avatars skills disable <approved-file> | avatars skills restore <archived-or-disabled-file> | avatars skills restore-missing <recorded-file-or-name> <source-markdown> | avatars skills repair-metadata <current-file> | avatars skills repair-history <current-file> | avatars skills repair-invariants <current-file> | avatars skills list | avatars skills pending | avatars skills review <generated-file> | avatars skills archived | avatars skills disabled | avatars skills timeline | avatars skills ledger | avatars skills reconcile | avatars skills restore-guide <recorded-file-or-name> | avatars skills resolve-missing <recorded-file-or-name> | avatars skills sync | avatars skills show <current-file> | avatars skills status | avatars skills promotion [--task <task-id>]"

func main() {
	platform.EnableUTF8Console()
	// F54: redirected Windows stdout (operator logs) gets a UTF-8 BOM so
	// Notepad/PowerShell sniff UTF-8 when the first bytes are CJK. Never BOM
	// stderr — AVATARS_PROGRESS=json must stay a raw JSON stream (F72).
	platform.WriteUTF8BOMIfRedirected(os.Stdout)
	seedRuntimeHome()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func seedRuntimeHome() {
	home := strings.TrimSpace(os.Getenv("AVATARS_HOME"))
	if home == "" {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		bundleHome := filepath.Dir(filepath.Dir(exe))
		if bundleHome == "" || bundleHome == "." {
			return
		}
		_ = os.Setenv("AVATARS_HOME", bundleHome)
		home = bundleHome
	}
	// Batch5/Q: warn when home agent.yaml lags repo configs (e.g. builder turns).
	warnStaleAvatarsHomeConfig(home)
}

func warnStaleAvatarsHomeConfig(home string) {
	homeCfg := filepath.Join(home, "configs", "agent.yaml")
	homeRaw, err := os.ReadFile(homeCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "avatars: warning: AVATARS_HOME configs missing (%s); copy configs/agent.yaml into AVATARS_HOME/configs before retests\n", homeCfg)
		return
	}
	// Prefer sibling repo configs when running from a checkout with both trees.
	candidates := []string{
		filepath.Join("configs", "agent.yaml"),
		filepath.Join("..", "configs", "agent.yaml"),
	}
	var repoRaw []byte
	for _, c := range candidates {
		if b, e := os.ReadFile(c); e == nil {
			repoRaw = b
			break
		}
	}
	if len(repoRaw) == 0 {
		return
	}
	homeTurns := extractYAMLBuilderMaxTurns(string(homeRaw))
	repoTurns := extractYAMLBuilderMaxTurns(string(repoRaw))
	if homeTurns > 0 && repoTurns > 0 && homeTurns != repoTurns {
		fmt.Fprintf(os.Stderr, "avatars: warning: AVATARS_HOME builder max_tool_turns=%d but repo configs has %d — copy configs/agent.yaml to %s\n",
			homeTurns, repoTurns, filepath.Join(home, "configs"))
	}
}

func extractYAMLBuilderMaxTurns(yamlText string) int {
	// Minimal scan: under max_tool_turns_by_role, builder: N
	inBlock := false
	for _, line := range strings.Split(yamlText, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "max_tool_turns_by_role:") {
			inBlock = true
			continue
		}
		if inBlock {
			if strings.HasPrefix(trimmed, "builder:") {
				fields := strings.Fields(trimmed)
				if len(fields) >= 2 {
					n, _ := strconv.Atoi(fields[1])
					return n
				}
			}
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.Contains(line, "  ") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") {
				inBlock = false
			}
		}
	}
	return 0
}
