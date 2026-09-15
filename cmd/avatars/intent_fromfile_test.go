package main

import (
	"strings"
	"testing"
)

func TestF74_TaskSpecDocument_CrossLanguageNLLibrary(t *testing.T) {
	// F74: compact --from-file with empty extra input must treat a NL task
	// spec as the intent (not "summarize this attached file"), for Go and
	// other common languages — same CLI path, not a Go-only heuristic.
	cases := []struct {
		name    string
		content string
	}{
		{
			name: "go-zh-slugifyx-r25",
			content: `我想在这个空目录里从零做一个 Go 小开源库，名字叫 slugifyx：把标题变成 URL slug。

需求白话说：
- 纯库优先，模块路径 example.com/slugifyx
- 带像样单元测试，go test 要过
- 不要一上来铺 CLI

请直接动手做。`,
		},
		{
			name: "python-en-library",
			content: `Create a small Python library called slugifyx from scratch.

Requirements:
- library-first, public API in slugifyx/
- unit tests with pytest
- do not add a CLI yet

Please implement it.`,
		},
		{
			name: "rust-zh-crate",
			content: `从零做一个 Rust crate，名字叫 slugifyx。

需求：
- 纯库，不要 bin
- cargo test 要过
- 说明写在源码头注释

请直接动手做。`,
		},
		{
			name: "java-en-library",
			content: `Create a small Java library called slugifyx from scratch.

Requirements:
- library-first, no main() CLI
- JUnit tests
- javadoc on the public API

Please implement it.`,
		},
		{
			name: "javascript-en-package",
			content: `Build a tiny JavaScript library that slugifies titles.

Requirements:
- no CLI
- jest unit tests
- ESM package

Please write the code.`,
		},
		{
			name: "stage-zh-webpage",
			content: `用 avatars stage 来写一个网页：
1. 推荐一本好书。
2. 举例说明为什么推荐。
3. 美化布局交给你，风格适合学生。`,
		},
		{
			name: "stage-en-html-deck",
			content: `Use avatars stage to write a webpage:
1. Recommend a book for a 7th-grade talk.
2. Clickable slides, more lively than PowerPoint.
3. Layout is up to you.`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !looksLikeTaskSpecDocument(tc.content) {
				t.Fatalf("expected task spec, looksLikeTaskSpecDocument=false")
			}
			got := attachedFileDefaultInput(tc.content)
			if got != strings.TrimSpace(tc.content) {
				t.Fatalf("expected file body as intent, got %q", got)
			}
			if strings.Contains(got, "summarize this attached file") || strings.Contains(got, "看看这个文件") {
				t.Fatalf("must not rewrite task spec to summarize-attachment: %q", got)
			}
		})
	}
}

func TestAttachedFileContinuationUsesBodyNotSummarize(t *testing.T) {
	cases := []string{
		"Last round hung: go test blocked on Wait using wall-clock sleep. Please continue and make the tests pass. Do not resume a random number.",
		"pytest still hangs. Fix the failing tests so the suite is green.",
		"cargo test failed. Continue fixing the crate until tests pass.",
		"刚才那轮没过门：go test 会卡住。Wait 用了墙上时钟 sleep。请接着把测试修绿。不要 resume 一个莫名其妙的数字。",
	}
	for _, content := range cases {
		got := attachedFileDefaultInput(content)
		if got != strings.TrimSpace(content) {
			t.Fatalf("expected file body as intent, got %q", got)
		}
		if strings.Contains(got, "summarize this attached file") {
			t.Fatalf("must not rewrite continuation to summarize: %q", got)
		}
	}

	reconcile := []string{
		"代码已经写出来、go test 也绿了，但清单看起来还全是空的。请把清单和真实进度对齐。用人话讲为什么这么划阶段、现在第几阶段。不要让我填 resume 数字。只动当前目录。",
		"The code is already on disk and tests are green, but the checklist is still empty. Align the checklist with real progress and say which phase we are in.",
	}
	for _, content := range reconcile {
		got := attachedFileDefaultInput(content)
		if got != strings.TrimSpace(content) {
			t.Fatalf("expected checklist-align body as intent, got %q", got)
		}
	}
}

func TestAttachedFileSkillDocStillSummarizes(t *testing.T) {
	skill := "---\nname: Test Skill\n---\n# Test Skill\nThis is a test skill document.\n"
	if looksLikeTaskSpecDocument(skill) {
		t.Fatal("skill document must not look like a task spec")
	}
	got := attachedFileDefaultInput(skill)
	if !strings.Contains(got, "summarize this attached file") {
		t.Fatalf("skill doc should keep summarize default, got %q", got)
	}

	src := "package slugifyx\n\nfunc Slugify(s string) string { return s }\n"
	if looksLikeTaskSpecDocument(src) {
		t.Fatal("Go source must not look like a task spec")
	}
	got = attachedFileDefaultInput(src)
	if !strings.Contains(got, "summarize this attached file") {
		t.Fatalf("source file should keep summarize default, got %q", got)
	}
}
