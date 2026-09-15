package main

import "strings"

// looksLikeInProcessLibraryCreate is true when the user wants an importable
// in-process library/package/crate/module, not a CLI/HTTP scaffold.
// Cross-language: Go/Python/JS/TS/Rust/Java/C#.
func looksLikeInProcessLibraryCreate(lowered string) bool {
	lowered = strings.ToLower(strings.TrimSpace(lowered))
	if lowered == "" {
		return false
	}
	lib := containsAnyIntentToken(lowered,
		"开源库", "进程内", "可 import", "可以 import",
		"library", "crate", "in-process", "in process", "importable",
		"import 的",
	)
	if strings.Contains(lowered, "库") {
		lib = true
	}
	create := containsAnyIntentToken(lowered,
		"做一个", "写一个", "新建", "从零", "空目录", "创建一个",
		"create a", "build a", "from scratch", "current directory",
	)
	if !lib || !create {
		return false
	}
	if looksLikeExplicitAppScaffold(lowered) && !looksLikeRejectedCLIOrHTTP(lowered) {
		return false
	}
	return true
}

func looksLikeExplicitAppScaffold(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"命令行工具", "cli tool", "cli 工具",
		"http 服务", "http server", "web app", "rest api", "管理后台",
		"空项目", "empty project", "搭一个项目", "搭建项目",
	)
}

func looksLikeRejectedCLIOrHTTP(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"不要 cli", "不要命令行", "不要 http", "也不要 cli", "也不要 http",
		"不要http", "也不要http",
		"no cli", "no http", "don't add a cli", "do not add a cli",
		"do not add an http", "don't add an http", "without a cli", "without http",
	)
}

func libraryCreateRunCommand(input string) []string {
	return []string{"run", "--new-task", "--permission-mode", "acceptEdits", strings.TrimSpace(input)}
}

// rewriteBootstrapAwayFromLibrary maps deprecated bootstrap/go-cli scaffolds
// onto acceptEdits when the NL asked for an in-process library. Does not touch
// LLM system prefixes (intent cache).
func rewriteBootstrapAwayFromLibrary(input string, cmd []string) []string {
	if len(cmd) == 0 || cmd[0] != "bootstrap" {
		return cmd
	}
	if !looksLikeInProcessLibraryCreate(strings.ToLower(input)) {
		return cmd
	}
	return libraryCreateRunCommand(input)
}
