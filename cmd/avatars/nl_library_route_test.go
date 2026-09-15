package main

import (
	"strings"
	"testing"
)

func TestRewriteBootstrapAwayFromLibrary(t *testing.T) {
	lib := "请在当前空目录做一个可以 import 的 Go 进程内优先队列库，名字叫 priorityqx。不要 HTTP 服务，也不要 CLI。Push 带优先级，Pop 取出最高优先（数字越小越优先）。"
	got := rewriteBootstrapAwayFromLibrary(lib, []string{"bootstrap", "--apply", "--name", "priorityqx", "--stack", "go-cli"})
	joined := strings.Join(got, " ")
	if got[0] != "run" || !strings.Contains(joined, "--new-task") || !strings.Contains(joined, "acceptEdits") {
		t.Fatalf("library NL must not stay bootstrap, got %q", joined)
	}
	if strings.Contains(joined, "go-cli") {
		t.Fatalf("must not keep go-cli scaffold: %q", joined)
	}

	scaffold := "帮我搭一个空项目并生成代码"
	keep := rewriteBootstrapAwayFromLibrary(scaffold, []string{"bootstrap", "--apply", "--name", "app", "--stack", "go-cli"})
	if keep[0] != "bootstrap" {
		t.Fatalf("empty-project scaffold should stay bootstrap, got %q", strings.Join(keep, " "))
	}

	cli := "帮我搭一个叫 ledger-cli 的空项目"
	keepCLI := rewriteBootstrapAwayFromLibrary(cli, []string{"bootstrap", "--apply", "--name", "ledger-cli"})
	if keepCLI[0] != "bootstrap" {
		t.Fatalf("named empty CLI project should stay bootstrap, got %q", strings.Join(keepCLI, " "))
	}
}

func TestLooksLikeInProcessLibraryCreate(t *testing.T) {
	if !looksLikeInProcessLibraryCreate(strings.ToLower("创建一个纯 Go 开源库 retrybudget")) {
		t.Fatal("open-source lib should match")
	}
	if looksLikeInProcessLibraryCreate(strings.ToLower("帮我搭一个空项目并生成代码")) {
		t.Fatal("empty project scaffold must not match")
	}
}
