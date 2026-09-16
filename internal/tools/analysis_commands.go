package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"avatars/internal/platform"
)

// AnalysisCommand maps keyword triggers to shell commands. This is the
// single source of truth for analysis command patterns — used by both
// the catch-all handler (cmd/avatars) and the pipeline Builder fallback
// (internal/runtime). Fixes P6-1 deduplication.
type AnalysisCommand struct {
	Keywords []string // any matches trigger this command
	Command  string   // shell command (bash -c)
}

// AnalysisCommands is the canonical mapping of user-intent keywords to
// read-only shell analysis commands. Order matters: more specific
// patterns first.
var AnalysisCommands = []AnalysisCommand{
	{Keywords: []string{"todo", "fixme", "hack", "xxx"},
		Command: `grep -rn "TODO\|FIXME\|HACK\|XXX" --include="*.go" --include="*.md" --include="*.py" --include="*.js" --include="*.ts" . 2>nul | head -40`},
	{Keywords: []string{"函数", "func", "导出", "export", "def"},
		Command: `grep -rn "^func [A-Z]\|^def \|^class " --include="*.go" --include="*.py" --include="*.js" . 2>nul | head -50`},
	{Keywords: []string{"类型", "type", "接口", "interface", "struct", "class", "类"},
		Command: `grep -rn "^type [A-Z]\|^class [A-Z]" --include="*.go" --include="*.py" --include="*.js" . 2>nul | head -30`},
	{Keywords: []string{"import", "导入", "依赖"},
		Command: `grep -rh "^import" --include="*.go" . 2>nul | sort | uniq -c | sort -rn | head -15`},
	{Keywords: []string{"行", "line", "loc"},
		Command: `find . -name "*.go" -not -path "*/.git/*" -not -path "*/.avatars/*" -not -path "*/vendor/*" -not -path "*/node_modules/*" -exec wc -l {} + 2>nul | tail -1`},
	{Keywords: []string{"树", "tree", "结构", "目录"},
		Command: `find . -maxdepth 3 -type d -not -path "*/.git/*" -not -path "*/.avatars/*" -not -path "*/node_modules/*" -not -path "*/vendor/*" | sort | head -30`},
	{Keywords: []string{"python", "py", ".py", "python文件"},
		Command: `find . -name "*.py" -not -path "*/.git/*" -not -path "*/.avatars/*" -not -path "*/__pycache__/*" | wc -l`},
	{Keywords: []string{"javascript", "js", ".js", "js文件"},
		Command: `find . -name "*.js" -not -path "*/.git/*" -not -path "*/.avatars/*" -not -path "*/node_modules/*" | wc -l`},
	{Keywords: []string{"go", "文件", "file", "统计", "count", "多少", "how many"},
		Command: `find . -name "*.go" -not -path "*/.git/*" -not -path "*/.avatars/*" -not -path "*/vendor/*" -not -path "*/node_modules/*" | wc -l`},
	{Keywords: []string{"分析", "analyze", "了解", "understand"},
		Command: `cat README.md 2>nul | head -80`},
	{Keywords: []string{"mod", "依赖", "dependency"},
		Command: `cat go.mod 2>nul | head -30`},
}

// BuildAnalysisCommands returns shell commands matching the given input
// keywords. At most maxCommands are returned. Used by both the catch-all
// handler and the pipeline Builder fallback.
func BuildAnalysisCommands(input string, maxCommands int) []string {
	if maxCommands <= 0 {
		maxCommands = 5
	}
	lowered := strings.ToLower(input)
	seen := map[string]bool{}
	var commands []string

	for _, spec := range AnalysisCommands {
		if len(commands) >= maxCommands {
			break
		}
		for _, kw := range spec.Keywords {
			if strings.Contains(lowered, kw) {
				if !seen[spec.Command] {
					seen[spec.Command] = true
					commands = append(commands, localizeAnalysisCommand(spec.Command))
				}
				break
			}
		}
	}

	if len(commands) == 0 {
		commands = defaultAnalysisCommands()
	}
	return commands
}

func defaultAnalysisCommands() []string {
	if platform.IsWindows() {
		return []string{
			`powershell -NoProfile -Command "(Get-ChildItem -Recurse -Filter *.go -ErrorAction SilentlyContinue | Where-Object { $_.FullName -notmatch '\\.git|\\.avatars|\\vendor|\\node_modules' }).Count"`,
			`powershell -NoProfile -Command "Get-ChildItem -Directory -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Name"`,
		}
	}
	return []string{
		`find . -name "*.go" -not -path "*/.git/*" -not -path "*/.avatars/*" -not -path "*/vendor/*" -not -path "*/node_modules/*" | wc -l`,
		`find . -maxdepth 2 -type d -not -path "*/.git/*" -not -path "*/.avatars/*" | sort | head -20`,
	}
}

func localizeAnalysisCommand(command string) string {
	if !platform.IsWindows() {
		return command
	}
	psCount := func(filter string) string {
		return `powershell -NoProfile -Command "(Get-ChildItem -Recurse -Filter ` + filter + ` -ErrorAction SilentlyContinue | Where-Object { $_.FullName -notmatch '\\.git|\\.avatars|\\vendor|\\node_modules|__pycache__' }).Count"`
	}
	switch {
	case strings.Contains(command, `find . -name "*.go"`) && strings.Contains(command, `wc -l`):
		return psCount("*.go")
	case strings.Contains(command, `find . -maxdepth`) && strings.Contains(command, `-type d`):
		return `powershell -NoProfile -Command "Get-ChildItem -Directory -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Name"`
	case strings.Contains(command, `find . -name "*.py"`) && strings.Contains(command, `wc -l`):
		return psCount("*.py")
	case strings.Contains(command, `find . -name "*.js"`) && strings.Contains(command, `wc -l`):
		return psCount("*.js")
	case strings.HasPrefix(command, "cat README"):
		return `powershell -NoProfile -Command "if (Test-Path README.md) { Get-Content README.md -TotalCount 80 }"`
	case strings.HasPrefix(command, "cat go.mod"):
		return `powershell -NoProfile -Command "if (Test-Path go.mod) { Get-Content go.mod -TotalCount 30 }"`
	case strings.HasPrefix(command, "dir /s /b") && strings.Contains(command, "find /c"):
		// Legacy broken cmd pipeline — rewrite to PowerShell.
		if strings.Contains(command, "*.py") {
			return psCount("*.py")
		}
		if strings.Contains(command, "*.js") {
			return psCount("*.js")
		}
		return psCount("*.go")
	default:
		return command
	}
}

// ExecuteReadOnlyShell runs a shell command and returns stdout. Only
// read-only commands are allowed (find, grep, wc, cat, head, sort, uniq,
// ls, dir). Uses os/exec with a timeout.
func ExecuteReadOnlyShell(command string, timeout time.Duration) (string, error) {
	lowered := strings.ToLower(strings.TrimSpace(command))
	allowedPrefixes := []string{"find ", "grep ", "wc ", "cat ", "head ", "sort ", "uniq ", "ls ", "dir "}
	if platform.IsWindows() {
		allowedPrefixes = append(allowedPrefixes, "powershell ", "where ")
	}
	allowed := false
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(lowered, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("command not allowed in plan mode: %s", command)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	shellArgs := platform.ShellCommand("-c", command)
	cmd := exec.CommandContext(ctx, shellArgs[0], shellArgs[1:]...)
	cmd.Dir = "."
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// ExecuteAnalysisCommands runs the given shell commands and returns
// formatted output. Truncates long output to maxOutputLen.
func ExecuteAnalysisCommands(commands []string, maxOutputLen int, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var sb strings.Builder
	sb.WriteString("Analysis results:\n\n")
	for _, cmd := range commands {
		sb.WriteString(fmt.Sprintf("$ %s\n", cmd))
		shellArgs := platform.ShellCommand("-c", cmd)
		execCmd := exec.CommandContext(ctx, shellArgs[0], shellArgs[1:]...)
		execCmd.Dir = "."
		output, err := execCmd.Output()
		if err != nil {
			sb.WriteString(fmt.Sprintf("  (error: %v)\n", err))
		} else {
			out := strings.TrimSpace(string(output))
			if out == "" {
				sb.WriteString("  (no output)\n")
			} else {
				if len(out) > maxOutputLen {
					out = out[:maxOutputLen] + "\n... (truncated)"
				}
				sb.WriteString(out)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}
