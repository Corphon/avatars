package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// numberedOrBulletList matches markdown/plain numbered or bullet lines.
var numberedOrBulletList = regexp.MustCompile(`(?m)^\s*(?:\d+[\.、)]|[-*])\s+`)

// looksLikeVisualSketchSpec is true when the user wants a Stage HTML sketch
// (slides, PPT-like deck, illustration, clickable webpage), not a coded
// service or in-process library. Cross-language NL: the same ask in English
// or Chinese, and the same HTML artifact whether the rest of the repo is
// Go, Python, JS/TS, Rust, Java, or C#.
func looksLikeVisualSketchSpec(text string) bool {
	lowered := strings.ToLower(strings.TrimSpace(text))
	if lowered == "" {
		return false
	}
	if looksLikeInProcessLibraryCreate(lowered) {
		return false
	}
	if looksLikeCodedWebApp(lowered) && !looksLikeExplicitStageCommand(lowered) {
		return false
	}
	if looksLikeExplicitStageCommand(lowered) {
		return true
	}
	visual := containsAnyIntentToken(lowered,
		"webpage", "web page", "html page", "html file", "landing page",
		"slides", "slideshow", "powerpoint", "ppt-like", "ppt like", "ppt",
		"interactive page", "presentation page", "visual sketch", "gallery card",
		"网页", "幻灯片", "幻灯", "演示文稿",
	)
	if !visual {
		return false
	}
	imperative := containsAnyIntentToken(lowered,
		"write a", "write an", "create a", "build a", "make a", "generate a",
		"写一个", "写一份", "做一个", "生成一个", "来写",
		"use avatars stage", "avatars stage",
	)
	list := looksLikeNumberedOrBulletList(text)
	return imperative || list
}

func looksLikeExplicitStageCommand(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"avatars stage",
		"use avatars stage",
		"用 avatars stage",
		"stage --from-file",
	)
}

// looksLikeCodedWebApp is a software product (HTTP API, SPA app, backend),
// not a single HTML sketch card.
func looksLikeCodedWebApp(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"rest api", "http server", "http 服务", "web app", "webapp",
		"react app", "vue app", "next.js", "nextjs", "express",
		"fastapi", "django", "flask", "spring boot",
		"管理后台", "后端服务",
	)
}

func looksLikeNumberedOrBulletList(text string) bool {
	return numberedOrBulletList.FindString(strings.TrimSpace(text)) != ""
}

func stageSketchCommand(input string) []string {
	path := ""
	if ctx := getReplFileContext(); ctx != nil && strings.TrimSpace(ctx.Path) != "" {
		path = strings.TrimSpace(ctx.Path)
	}
	if path != "" {
		return []string{"stage", "--from-file", path}
	}
	return []string{"stage", strings.TrimSpace(input)}
}

// stageSketchSafeRunDecision intercepts visual-sketch NL before the intent
// LLM so the stable router prefix is not spent on an already-known action.
func stageSketchSafeRunDecision(input string) (naturalLanguageDecision, bool) {
	body := strings.TrimSpace(input)
	if ctx := getReplFileContext(); ctx != nil && looksLikeVisualSketchSpec(ctx.Content) {
		body = ctx.Content
	}
	if !looksLikeVisualSketchSpec(body) && !looksLikeVisualSketchSpec(input) {
		return naturalLanguageDecision{}, false
	}
	return naturalLanguageDecision{
		Kind:       naturalLanguageDecisionSafeRun,
		Reason:     "visual sketch / stage HTML card, not a coded app or library",
		Command:    stageSketchCommand(input),
		Confidence: 94,
	}, true
}

// rewriteCommandTowardStageSketch maps run/bootstrap/summarize misroutes onto
// `stage --from-file` when the NL (or attached file) is a visual sketch.
// Does not touch LLM system prefixes (intent cache).
func rewriteCommandTowardStageSketch(input string, cmd []string) []string {
	body := strings.TrimSpace(input)
	if ctx := getReplFileContext(); ctx != nil && looksLikeVisualSketchSpec(ctx.Content) {
		body = ctx.Content
	}
	if !looksLikeVisualSketchSpec(body) && !looksLikeVisualSketchSpec(input) {
		return cmd
	}
	if len(cmd) > 0 && cmd[0] == "stage" {
		return ensureStageFromFile(cmd)
	}
	return stageSketchCommand(input)
}

func ensureStageFromFile(cmd []string) []string {
	if len(cmd) == 0 || cmd[0] != "stage" {
		return cmd
	}
	for i, a := range cmd {
		if a == "--from-file" && i+1 < len(cmd) && strings.TrimSpace(cmd[i+1]) != "" {
			return cmd
		}
	}
	ctx := getReplFileContext()
	if ctx == nil || strings.TrimSpace(ctx.Path) == "" {
		return cmd
	}
	out := []string{"stage", "--from-file", strings.TrimSpace(ctx.Path)}
	for i := 1; i < len(cmd); i++ {
		if cmd[i] == "--from-file" {
			i++
			continue
		}
		out = append(out, cmd[i])
	}
	return out
}

// stageGalleryStatusDecision answers "is that stage page still there"
// locally. It does not start a sketch and does not call the intent LLM.
func stageGalleryStatusDecision(input string) (naturalLanguageDecision, bool) {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if !looksLikeStageGalleryQuestion(lowered) {
		return naturalLanguageDecision{}, false
	}
	if looksLikeVisualSketchSpec(input) {
		return naturalLanguageDecision{}, false
	}
	return naturalLanguageDecision{
		Kind:       naturalLanguageDecisionAnswer,
		Reason:     "stage gallery answered locally",
		Answer:     stageGalleryStatusAnswer(),
		Confidence: 90,
	}, true
}

func looksLikeStageGalleryQuestion(lowered string) bool {
	if !containsAnyIntentToken(lowered, "舞台页", "stage page", "stage gallery", "stage/index") {
		return false
	}
	return containsAnyIntentToken(lowered,
		"还在", "在不", "在吗", "有没有", "有关系", "跟这次", "跟这次有",
		"still there", "still exist", "related", "where is", "is it",
	)
}

func stageGalleryStatusAnswer() string {
	var files []string
	entries, err := os.ReadDir("stage")
	if err == nil {
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			name := ent.Name()
			lower := strings.ToLower(name)
			if strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm") {
				files = append(files, filepath.ToSlash(filepath.Join("stage", name)))
			}
		}
	}
	if len(files) == 0 {
		return "No stage gallery files under stage/. A stage page is a visual sketch, separate from the current software task."
	}
	return "Stage sketches on disk: " + strings.Join(files, ", ") + ". They are visual sketches, not the current software task."
}

func isAutoSafeStageCommand(command []string) bool {
	if len(command) == 0 || command[0] != "stage" {
		return false
	}
	for _, a := range command {
		if a == "--delete" || a == "--serve" {
			return false
		}
	}
	return true
}
