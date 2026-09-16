package runtime

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"avatars/internal/planner"
)

var (
	rePhaseNComplete = regexp.MustCompile(`(?i)\bphase\s+\d+\s*[—–-]?\s*(is\s+)?(fully\s+|verified\s+)?complete\b`)
	reWorkflowDocRef = regexp.MustCompile(`(?i)(?:docs[/\\]workflow[/\\])?[a-z0-9_.-]*phase\d+\.md|docs[/\\]workflow[/\\][a-z0-9_.-]+\.md|avatars_(?:plan|todo)\.md|user_requirement\.md|process_record\.md`)
	writeVerbOnly    = regexp.MustCompile(`(?i)^(write|create|add|draft|author|update|edit|编写|创建|添加|写|撰写)s?\s*[.。,，:：]*$`)
)

// builderProseSaysComplete reports LLM "already done / verification summary"
// replies that produce no new files. Language-agnostic: any phase number,
// any common toolchain — not Go-only and not Phase-1-only.
func builderProseSaysComplete(text string) bool {
	lower := strings.ToLower(text)
	if rePhaseNComplete.MatchString(lower) {
		return true
	}
	for _, needle := range []string{
		"no further code changes",
		"no code changes were needed",
		"no code changes needed",
		"no modifications were needed",
		"no modifications needed",
		"no file changes were needed",
		"already on disk",
		"already complete",
		"already existed",
		"implementation was already",
		"files were already",
		"all verification passed",
		"verification passed",
		"all checks pass",
		"build is passing",
		"build status: passing",
		"tests pass",
		"tests passed",
		"go build ./... — exits 0",
		"verification report",
		"phase 1 summary",
		"all phase 1 deliverables",
		"both `go build",
	} {
		if hasPositiveNeedle(lower, needle) {
			return true
		}
	}
	if toolchainPassPhrase(lower) {
		return true
	}
	return false
}

func hasPositiveNeedle(lower, needle string) bool {
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return false
	}
	start := idx - 10
	if start < 0 {
		start = 0
	}
	prefix := lower[start:idx]
	if strings.Contains(prefix, "not ") || strings.Contains(prefix, "n't ") ||
		strings.Contains(prefix, "not-") {
		return false
	}
	return true
}

func toolchainPassPhrase(lower string) bool {
	runners := []string{"pytest", "npm test", "cargo test", "go test", "mvn test", "dotnet test"}
	for _, r := range runners {
		if !strings.Contains(lower, r) {
			continue
		}
		if strings.Contains(lower, "pass") || strings.Contains(lower, "ok ") ||
			strings.Contains(lower, "exits 0") || strings.Contains(lower, "exit 0") {
			return true
		}
	}
	return false
}

func builderCodeFilePaths(files []builderCodeFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func builderFilesHaveImplSources(files []builderCodeFile) bool {
	return len(implementationSourcesFrom(builderCodeFilePaths(files))) > 0
}

func builderFilesAreNonImplementation(files []builderCodeFile) bool {
	if len(files) == 0 {
		return false
	}
	return !builderFilesHaveImplSources(files)
}

func mergeBuilderCodeFiles(base, extra []builderCodeFile) []builderCodeFile {
	if len(extra) == 0 {
		return base
	}
	if len(base) == 0 {
		return extra
	}
	idx := make(map[string]int, len(base)+len(extra))
	out := append([]builderCodeFile{}, base...)
	for i, f := range out {
		idx[filepath.ToSlash(f.Path)] = i
	}
	for _, f := range extra {
		key := filepath.ToSlash(f.Path)
		if i, ok := idx[key]; ok {
			out[i] = f
			continue
		}
		idx[key] = len(out)
		out = append(out, f)
	}
	return out
}

func stripWorkflowFileMentions(input string) string {
	s := reWorkflowDocRef.ReplaceAllString(input, " ")
	s = strings.ReplaceAll(s, "`", " ")
	return strings.TrimSpace(s)
}

// asksForAppSources reports that the user asked for library/package/API work,
// not only a workflow markdown file. Cross-language: any source tree.
func asksForAppSources(input string) bool {
	rest := stripWorkflowFileMentions(input)
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return false
	}
	collapsed := strings.Join(strings.Fields(rest), " ")
	if writeVerbOnly.MatchString(collapsed) {
		return false
	}
	return needsCodeImplementation(rest)
}

// retryHollowBuilderDelivery gives one extra codegen pass when a code task
// produced no implementation sources. Reuses the existing user-prompt repair
// suffix so the cached Builder system prefix is unchanged.
func (e *Engine) retryHollowBuilderDelivery(ctx context.Context, work workflowNodeWorkContext, userFacingInput string, codeFiles []builderCodeFile, codeErr error) ([]builderCodeFile, error) {
	if e == nil || e.llm == nil {
		return codeFiles, codeErr
	}
	if planner.LooksLikeProgressReconcileTask(userFacingInput) {
		return codeFiles, codeErr
	}
	wd, wdErr := os.Getwd()
	if wdErr != nil {
		return codeFiles, codeErr
	}
	state := e.ensureBuilderRetryState(work.nodeID)
	empty := deliveryLooksEmpty(wd)
	hasImpl := builderFilesHaveImplSources(codeFiles)
	needsCode := needsCodeImplementation(userFacingInput)

	if empty && needsCode && !hasImpl && !state.EmptyDeliveryRetried {
		state.EmptyDeliveryRetried = true
		state.ProseNoFilesRetried = true
		retryWork := work
		retryWork.input = work.input + builderRepairWriteSuffix
		_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.empty_delivery_retry", "runtime", map[string]any{
			"node_id": work.nodeID,
			"reason":  "code task produced no implementation sources; retry write",
		}, nil)
		retryFiles, retryErr := e.generateBuilderCode(ctx, retryWork)
		if retryErr == nil && len(retryFiles) > 0 {
			return mergeBuilderCodeFiles(codeFiles, retryFiles), nil
		}
		if retryErr != nil {
			return codeFiles, retryErr
		}
		return codeFiles, codeErr
	}

	if !empty && asksForAppSources(userFacingInput) && builderFilesAreNonImplementation(codeFiles) && !state.DocsOnlyRetried {
		state.DocsOnlyRetried = true
		retryWork := work
		retryWork.input = work.input + builderRepairWriteSuffix
		_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.docs_only_retry", "runtime", map[string]any{
			"node_id": work.nodeID,
			"reason":  "workflow/docs writes are not implementation delivery; retry write",
		}, nil)
		retryFiles, retryErr := e.generateBuilderCode(ctx, retryWork)
		if retryErr == nil {
			return mergeBuilderCodeFiles(codeFiles, retryFiles), nil
		}
		if builderProseSaysComplete(retryErr.Error()) && crossLangHealthCheck(wd) == "" {
			return codeFiles, nil
		}
		return mergeBuilderCodeFiles(codeFiles, retryFiles), retryErr
	}

	return codeFiles, codeErr
}
