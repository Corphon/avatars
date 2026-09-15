package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"avatars/internal/workflow"
)

// looksLikeForbiddenMutationAsk is true when the user forbids writes or a new
// task. Cross-language: same steal happens after a Python/JS/Rust/Java/C# run.
func looksLikeForbiddenMutationAsk(lowered string) bool {
	lowered = strings.ToLower(strings.TrimSpace(lowered))
	if lowered == "" {
		return false
	}
	if looksLikeNegatedReadOnlyOnly(lowered) || looksLikeTestRepairRequest(lowered) {
		return false
	}
	return containsAnyIntentToken(lowered,
		"别写代码", "先别写", "不要写代码", "先别改代码", "不要改代码", "不要改文件",
		"别动文件", "别改文件", "别改代码", "别开任务", "不要开任务", "不要跑任务",
		"别跑任务", "就解释", "直接答", "先别改",
		"don't write code", "do not write code", "dont write code",
		"don't edit", "do not edit", "don't change files", "do not change files",
		"don't start a task", "do not start a task", "don't open a task",
		"do not open a task", "don't run a task", "do not run a task",
		"just explain", "just answer", "answer only", "no new task",
	)
}

func looksLikeRetractedAside(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"开玩笑", "当我没说", "当我没问", "算了当我",
		"just kidding", "forget i said", "forget i asked", "never mind",
		"nevermind", "scratch that", "i take that back",
	)
}

func looksLikeDesignChoiceAsk(lowered string) bool {
	if containsAnyIntentToken(lowered,
		"覆盖还是", "还是阻塞", "该覆盖", "该阻塞",
		"拒绝还是", "还是排队", "还是拒绝", "直接拒绝",
		"要不要返回", "要不要带",
		"overwrite or", "or block", "or wait", "or queue",
		"reject or", "fail-fast or", "fail fast or",
		"should we overwrite", "should it block",
		"which semantic", "which overflow",
	) {
		return true
	}
	eitherOr := strings.Contains(lowered, "还是") || strings.Contains(lowered, " or ")
	forbid := looksLikeForbiddenMutationAsk(lowered)
	if eitherOr && forbid {
		return true
	}
	return (strings.Contains(lowered, "觉得") || strings.Contains(lowered, "what do you think") ||
		strings.Contains(lowered, "which should")) &&
		(strings.Contains(lowered, "覆盖") || strings.Contains(lowered, "阻塞") ||
			strings.Contains(lowered, "overwrite") || strings.Contains(lowered, "block") ||
			strings.Contains(lowered, "排队") || strings.Contains(lowered, "拒绝"))
}

func looksLikeConceptCompareAsk(lowered string) bool {
	if containsAnyIntentToken(lowered,
		"有啥区别", "有什么区别", "what's the difference", "what is the difference",
		"how is this different", "compared to",
	) {
		return true
	}
	return (strings.Contains(lowered, "区别") || strings.Contains(lowered, "difference")) &&
		(strings.Contains(lowered, " vs ") || strings.Contains(lowered, "versus") ||
			strings.Contains(lowered, "channel") || strings.Contains(lowered, "queue") ||
			strings.Contains(lowered, "hystrix") || strings.Contains(lowered, "gobreaker") ||
			strings.Contains(lowered, "errgroup") || strings.Contains(lowered, "waitgroup") ||
			strings.Contains(lowered, "wait group") || strings.Contains(lowered, "compared") ||
			strings.Contains(lowered, "afterfunc") || strings.Contains(lowered, "after func") ||
			strings.Contains(lowered, "lodash") || strings.Contains(lowered, "underscore") ||
			strings.Contains(lowered, "promise.all") || strings.Contains(lowered, "asyncio.gather") ||
			strings.Contains(lowered, "joinall") || strings.Contains(lowered, "countdownlatch"))
}

func looksLikeExportedAPIAsk(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"返回值", "签名", "满了会怎样", "满了怎样",
		"先定 api", "先定api", "先定一下", "先定一下 api",
		"要不要返回", "要不要带", "返回 error", "带 error",
		"return value", "returns what", "what does", "signature",
		"when full", "public api", "exported api",
		"settle api", "settle the api", "return error", "take an error",
		"should allow", "should record",
	)
}

func looksLikeQuestionOnlyAsk(lowered string) bool {
	return strings.Contains(lowered, "？") || strings.Contains(lowered, "?") ||
		strings.Contains(lowered, "要不要") || strings.Contains(lowered, "该不该") ||
		(strings.Contains(lowered, "怎么") && !looksLikeGreenfieldCreateRequest(lowered))
}

func looksLikeChecklistContradictionAsk(lowered string) bool {
	checklist := strings.Contains(lowered, "清单") || strings.Contains(lowered, "checklist") ||
		strings.Contains(lowered, "0/3") || strings.Contains(lowered, "todo") ||
		strings.Contains(lowered, "测试套件") || strings.Contains(lowered, "test suite")
	doneAsk := strings.Contains(lowered, "完成") || strings.Contains(lowered, "做完") ||
		strings.Contains(lowered, "矛盾") || strings.Contains(lowered, "到底") ||
		strings.Contains(lowered, "勾完") || strings.Contains(lowered, "勾了") ||
		strings.Contains(lowered, "finished") || strings.Contains(lowered, "done or not") ||
		strings.Contains(lowered, "contradict") || strings.Contains(lowered, "ticked")
	return checklist && doneAsk
}

func looksLikeDeliveryHonestyAsk(lowered string) bool {
	red := containsAnyIntentToken(lowered,
		"编不过", "编译失败", "编译不过", "还不绿", "还红", "通不过",
		"build failed", "compile fail", "compile error", "still red",
		"still failing", "does not compile", "doesn't compile", "dont compile",
		"卡住", "超时", "hung", "timed out", "deadlock",
	)
	if strings.Contains(lowered, "go test") && (strings.Contains(lowered, "不过") ||
		strings.Contains(lowered, "失败") || strings.Contains(lowered, "fail") ||
		strings.Contains(lowered, "红") || strings.Contains(lowered, "卡住") ||
		strings.Contains(lowered, "超时")) {
		red = true
	}
	ask := strings.Contains(lowered, "完成") || strings.Contains(lowered, "做完") ||
		strings.Contains(lowered, "到底") || strings.Contains(lowered, "勾完") ||
		strings.Contains(lowered, "done") || strings.Contains(lowered, "finished")
	return red && (ask || looksLikeChecklistContradictionAsk(lowered) || looksLikeForbiddenMutationAsk(lowered))
}

func looksLikeLastWriteVsQueueAsk(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"只留最后", "最后一次",
		"last write", "last-write", "last call wins", "last-call",
	)
}

func looksLikeShareOnceVsEachCallAsk(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"只执行一次", "共享结果", "大家共享", "同 key", "同一个 key",
		"share one", "same key", "coalesce", "singleflight",
		"每个都排队", "每趟都跑", "every call run", "run every call",
	)
}

// looksLikeNoMutateFollowUp is a status/design/API/compare/retract question
// that must not launch a coding DAG.
func looksLikeNoMutateFollowUp(lowered string) bool {
	lowered = strings.ToLower(strings.TrimSpace(lowered))
	if lowered == "" {
		return false
	}
	if mechanicalLocalAnswersYieldToWork(lowered) && !looksLikeForbiddenMutationAsk(lowered) {
		return false
	}
	if looksLikeResumeContinuationIntent(lowered) || looksLikeTestRepairRequest(lowered) ||
		looksLikeGreenfieldCreateRequest(lowered) {
		return false
	}
	if looksLikeChecklistProgressReconcile(lowered) && !looksLikeForbiddenMutationAsk(lowered) {
		return false
	}
	specific := looksLikeRetractedAside(lowered) ||
		looksLikeDesignChoiceAsk(lowered) ||
		looksLikeConceptCompareAsk(lowered) ||
		looksLikeExportedAPIAsk(lowered) ||
		looksLikeChecklistContradictionAsk(lowered) ||
		looksLikeDeliveryHonestyAsk(lowered) ||
		looksLikeWorkflowPhaseStatusAsk(lowered) ||
		looksLikeOrderSemanticsAsk(lowered)
	if looksLikeForbiddenMutationAsk(lowered) && (specific || looksLikeQuestionOnlyAsk(lowered)) {
		return true
	}
	if !specific {
		return false
	}
	return looksLikeRetractedAside(lowered)
}

func answerNoMutateFollowUp(input string) (string, bool) {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if !looksLikeNoMutateFollowUp(lowered) && !looksLikeExportedAPIAsk(lowered) &&
		!looksLikeDesignChoiceAsk(lowered) && !looksLikeConceptCompareAsk(lowered) &&
		!looksLikeChecklistContradictionAsk(lowered) && !looksLikeDeliveryHonestyAsk(lowered) &&
		!looksLikeRetractedAside(lowered) && !looksLikeOrderSemanticsAsk(lowered) {
		return "", false
	}
	if mechanicalLocalAnswersYieldToWork(lowered) && !looksLikeForbiddenMutationAsk(lowered) {
		return "", false
	}

	honestyAsk := looksLikeChecklistContradictionAsk(lowered) || looksLikeDeliveryHonestyAsk(lowered)
	var parts []string
	if looksLikeRetractedAside(lowered) {
		parts = append(parts, "OK — treating the aside as withdrawn. No files changed. The current delivery stays an in-process library/package/crate/module.")
	}
	if looksLikeWorkflowPhaseStatusAsk(lowered) || honestyAsk {
		if ans, ok := answerWorkflowPhaseStatusQuestion(input); ok {
			parts = append(parts, ans)
		} else {
			parts = append(parts, summarizeChecklistHonesty("."))
		}
	}
	dumpAPI := (looksLikeExportedAPIAsk(lowered) || looksLikeDesignChoiceAsk(lowered) ||
		(looksLikeForbiddenMutationAsk(lowered) && looksLikeQuestionOnlyAsk(lowered))) && !honestyAsk
	if dumpAPI {
		if api := summarizeWorkspacePublicAPI("."); api != "" {
			parts = append(parts, api)
		}
		if dec := summarizePlanKeyDecisions("."); dec != "" {
			parts = append(parts, dec)
		}
	}
	if looksLikeConceptCompareAsk(lowered) {
		parts = append(parts, summarizeConceptCompare(lowered))
	}
	if looksLikeDesignChoiceAsk(lowered) || looksLikeOrderSemanticsAsk(lowered) {
		if sem := summarizeOnDiskCallSemantics(".", lowered); sem != "" {
			parts = append(parts, sem)
		}
		if ord := summarizeOnDiskOrderSemantics(".", lowered); ord != "" {
			parts = append(parts, ord)
		} else if looksLikeOverflowChoiceAsk(lowered) && len(parts) > 0 {
			parts = append(parts, "Overflow choice is already on disk: prefer the documented Push/TryPush (or language equivalent) pair instead of changing architecture.")
		} else if looksLikeDesignChoiceAsk(lowered) && len(parts) > 0 {
			parts = append(parts, "Keep the on-disk exported API unless you explicitly ask to change it. No files changed.")
		}
	}
	if honesty := summarizeCompileTestHonesty("."); honesty != "" {
		joined := strings.Join(parts, "\n")
		if !strings.Contains(joined, honesty) {
			parts = append(parts, honesty)
		}
	}
	out := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if out == "" && looksLikeForbiddenMutationAsk(lowered) {
		return "No files changed. This is a question, not a new coding task.", true
	}
	if out == "" {
		return "", false
	}
	return out, true
}

func looksLikeOrderSemanticsAsk(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"插队", "进入顺序", "同优先级",
		"fifo", "insertion order", "jump the queue", "cut in line",
		"越小越优先", "越大越先", "数字越小", "数字越大",
		"min-heap", "max-heap", "min heap", "max heap",
		"smaller first", "larger first", "which way", "哪边",
	)
}

func looksLikeOverflowChoiceAsk(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"覆盖", "阻塞", "overwrite", "or block", "when full", "满了")
}

func summarizeChecklistHonesty(root string) string {
	planPath := filepath.Join(root, "docs", "workflow", "avatars_plan.md")
	todoPath := filepath.Join(root, "docs", "workflow", "avatars_todo.md")
	planBytes, planErr := os.ReadFile(planPath)
	todoBytes, todoErr := os.ReadFile(todoPath)
	if planErr != nil && todoErr != nil {
		return "No workflow plan/todo on disk yet. Treat the source tree (and language test runner) as the delivery, not a missing checklist count."
	}
	var b strings.Builder
	if planErr == nil {
		meta := workflow.ParsePlanMeta(string(planBytes))
		fmt.Fprintf(&b, "Plan status: %s. Phase count: %d. Active phase: %d.\n",
			strings.TrimSpace(meta.Status), meta.PhaseCount, meta.ActivePhase)
	}
	if todoErr == nil {
		done, total := workflow.CountTodoProgressFromContent(string(todoBytes))
		fmt.Fprintf(&b, "Todo checklist (active phase rows): %d/%d done.\n", done, total)
	}
	b.WriteString("If a delivery summary showed 0/N while todo is filled, trust the later todo/plan and the language test runner — the summary snapshot can lag marks.")
	if honesty := summarizeCompileTestHonesty(root); honesty != "" {
		b.WriteString("\n")
		b.WriteString(honesty)
	}
	return strings.TrimSpace(b.String())
}

func summarizeMissingTestsHonesty(root string) string {
	if workflow.ProjectHasTestFiles(root) {
		return ""
	}
	if !workflow.ProjectHasSubstantiveSources(root) {
		return ""
	}
	return "On disk: no test files. A ticked checklist or a language runner that exits 0 with no tests is not a green suite."
}

func summarizeCompileTestHonesty(root string) string {
	if workflow.ProjectHasTestFiles(root) && !workflow.ProjectTestsGreen(root) {
		return "On disk: test files exist but the language test runner is still red. Treat the delivery as incomplete."
	}
	if !workflow.ProjectHasSubstantiveSources(root) {
		return ""
	}
	return summarizeMissingTestsHonesty(root)
}

func summarizePlanKeyDecisions(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "docs", "workflow", "avatars_plan.md"))
	if err != nil {
		return ""
	}
	text := string(data)
	idx := strings.Index(text, "## Key Decisions")
	if idx < 0 {
		return ""
	}
	rest := text[idx:]
	if end := strings.Index(rest, "\n## "); end > 0 {
		rest = rest[:end]
	}
	var lines []string
	for _, line := range strings.Split(rest, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "|") && !strings.Contains(t, "---") && !strings.Contains(t, "Decision") {
			lines = append(lines, t)
		}
		if len(lines) >= 6 {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Plan decisions:\n" + strings.Join(lines, "\n")
}

func summarizeOnDiskCallSemantics(root, lowered string) string {
	shape := diskCallShape(root)
	switch shape {
	case "coalesce":
		return "Semantics on disk: concurrent callers of the same key share one in-flight execution and one result. They are not queued to run the function again unless Forget (or the language equivalent) drops that key. No files changed."
	case "debounce":
		return "Semantics on disk: later calls replace the pending invocation (last-write / debounce). Intermediate calls are not queued to run in order unless the exported API says so. No files changed."
	case "overflow":
		return "Overflow choice is already on disk: prefer the documented Push/TryPush (or language equivalent) pair instead of changing architecture."
	}
	if looksLikeShareOnceVsEachCallAsk(lowered) {
		return "Semantics on disk: follow the exported coalesce/share-once API (same key, one execution). Do not treat this as last-write/debounce unless those names are on disk. No files changed."
	}
	if looksLikeLastWriteVsQueueAsk(lowered) {
		return "Semantics on disk: follow the exported API. Last-write/debounce only applies when that pattern is what the sources already implement. No files changed."
	}
	return ""
}

func diskCallShape(root string) string {
	api := strings.ToLower(summarizeWorkspacePublicAPI(root))
	if api == "" {
		return ""
	}
	hasForget := strings.Contains(api, "forget")
	hasDo := strings.Contains(api, "do(") || strings.Contains(api, "dochan")
	if hasForget && hasDo {
		return "coalesce"
	}
	if strings.Contains(api, "debounce") || (strings.Contains(api, "cancel") && hasDo && !hasForget) {
		return "debounce"
	}
	if strings.Contains(api, "trypush") || strings.Contains(api, "try_push") ||
		(strings.Contains(api, "push(") && strings.Contains(api, "try")) {
		return "overflow"
	}
	return ""
}

func summarizeOnDiskOrderSemantics(root, lowered string) string {
	blob := strings.ToLower(workspaceSourceBlob(root))
	if blob == "" {
		return ""
	}
	var parts []string
	wantFIFO := containsAnyIntentToken(lowered, "插队", "fifo", "进入顺序", "后进", "insertion order", "jump")
	wantOrder := containsAnyIntentToken(lowered, "越小", "越大", "min-heap", "max-heap", "min heap", "max heap", "哪边", "smaller", "larger")
	if wantFIFO || wantOrder {
		if strings.Contains(blob, "fifo") || strings.Contains(blob, "insertion order") ||
			strings.Contains(blob, ".order") || strings.Contains(blob, "seq++") {
			parts = append(parts, "Equal-priority items follow insertion order (FIFO). A later equal-priority push does not jump ahead. No files changed.")
		}
		switch {
		case strings.Contains(blob, "min-heap") || strings.Contains(blob, "smaller") && strings.Contains(blob, "first") ||
			strings.Contains(blob, "prio <") || strings.Contains(blob, "priority <"):
			parts = append(parts, "On disk: smaller integer priority pops first (min-heap). No files changed.")
		case strings.Contains(blob, "max-heap") || strings.Contains(blob, "higher priorit") ||
			strings.Contains(blob, "prio >") || strings.Contains(blob, "priority >"):
			parts = append(parts, "On disk: larger integer priority pops first (max-heap). No files changed.")
		}
	}
	return strings.Join(parts, "\n")
}

func workspaceSourceBlob(root string) string {
	var b strings.Builder
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() {
				switch d.Name() {
				case ".git", ".avatars", "vendor", "node_modules", "docs", "testdata":
					if path != root {
						return filepath.SkipDir
					}
				}
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		switch ext {
		case ".go", ".py", ".js", ".ts", ".rs", ".java", ".cs":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if b.Len() > 12000 {
			return filepath.SkipAll
		}
		b.Write(data)
		b.WriteByte('\n')
		return nil
	})
	return b.String()
}

func summarizeConceptCompare(lowered string) string {
	low := strings.ToLower(lowered)
	if strings.Contains(low, "afterfunc") || strings.Contains(low, "after func") ||
		strings.Contains(low, "settimeout") || strings.Contains(low, "time.after") {
		return "A one-shot timer (time.AfterFunc, setTimeout, and similar) fires once. A debounce wrapper resets that timer on each call so only the last scheduled function runs after a quiet period. If sources have no HTTP/CLI surface, do not add one unless you ask for it."
	}
	if strings.Contains(low, "lodash") || strings.Contains(low, "underscore") {
		return "Same last-call-wins idea as lodash.debounce / underscore.debounce: later calls replace the pending one. This delivery is a small in-process library, not a port of that framework and not an HTTP service."
	}
	if strings.Contains(low, "errgroup") || strings.Contains(low, "waitgroup") ||
		strings.Contains(low, "wait group") || strings.Contains(low, "asyncio.gather") ||
		strings.Contains(low, "promise.all") || strings.Contains(low, "joinall") ||
		strings.Contains(low, "countdownlatch") {
		return "A wait-group / errgroup / gather / Promise.all waits for a set of independent tasks. An in-process coalesce (singleflight-style) merges concurrent callers of the same key into one execution. They solve different problems. If sources have no HTTP/CLI surface, do not add one unless you ask for it."
	}
	if strings.Contains(low, "container/heap") || strings.Contains(low, "heapq") ||
		strings.Contains(low, "priorityqueue") || strings.Contains(low, "priority queue") ||
		strings.Contains(low, "binaryheap") || strings.Contains(low, "binary heap") {
		return "A language heap primitive (container/heap, heapq, PriorityQueue) is the unordered-heap interface. An in-process priority-queue library wraps it with typed Push/Pop, empty-pop behavior, and equal-priority FIFO when the sources say so. Compare to the exported API on disk, not to the primitive alone."
	}
	if strings.Contains(low, "channel") || strings.Contains(low, "queue") ||
		strings.Contains(low, "socket") {
		return "This delivery is an in-process library/package/crate/module: callers invoke exported functions in the same process. A language channel, queue, or socket is a different primitive (concurrency or I/O). If sources have no HTTP/CLI surface, do not add one unless you ask for it."
	}
	return "This delivery is a small in-process library/package/crate/module. Compare it to the exported API on disk, not to another framework. If sources have no HTTP/CLI surface, do not add one unless you ask for it."
}

var (
	reGoExport   = regexp.MustCompile(`(?m)^func\s+(?:\(([^)]*)\)\s+)?([A-Z][A-Za-z0-9_]*)\s*(\([^;{]*)`)
	rePyDef      = regexp.MustCompile(`(?m)^def\s+([A-Za-z_][A-Za-z0-9_]*)\s*(\([^)]*\))`)
	reJSExport   = regexp.MustCompile(`(?m)^export\s+(?:async\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)\s*(\([^)]*\))`)
	reRustPub    = regexp.MustCompile(`(?m)^pub\s+(?:async\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)\s*(\([^)]*\))`)
	reJavaPublic = regexp.MustCompile(`(?m)public\s+(?:static\s+)?[\w.<>,\[\]\s]+?\s+([A-Za-z_][A-Za-z0-9_]*)\s*(\([^;{]*)`)
	reCSharpPub  = regexp.MustCompile(`(?m)public\s+(?:static\s+)?[\w.<>,\[\]\s]+?\s+([A-Za-z_][A-Za-z0-9_]*)\s*(\([^;{]*)`)
)

func summarizeWorkspacePublicAPI(root string) string {
	var lines []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() {
				switch d.Name() {
				case ".git", ".avatars", "vendor", "node_modules", "docs", "testdata":
					if path != root {
						return filepath.SkipDir
					}
				}
			}
			return nil
		}
		name := d.Name()
		lower := strings.ToLower(name)
		if strings.Contains(lower, "_test.") || strings.HasPrefix(lower, "test_") ||
			strings.Contains(lower, ".spec.") || strings.Contains(lower, ".test.") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".go", ".py", ".js", ".ts", ".jsx", ".tsx", ".rs", ".java", ".cs":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel := path
		if r, e := filepath.Rel(root, path); e == nil {
			rel = filepath.ToSlash(r)
		}
		for _, sig := range exportedSignaturesFromSource(ext, string(data)) {
			lines = append(lines, rel+": "+sig)
			if len(lines) >= 16 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if len(lines) == 0 {
		return ""
	}
	return "Exported API on disk:\n- " + strings.Join(lines, "\n- ")
}

func goReceiverTypeExported(recv string) bool {
	recv = strings.TrimSpace(recv)
	if recv == "" {
		return true
	}
	fields := strings.Fields(recv)
	typ := fields[len(fields)-1]
	typ = strings.TrimPrefix(typ, "*")
	if typ == "" {
		return false
	}
	r := []rune(typ)
	return r[0] >= 'A' && r[0] <= 'Z'
}

func exportedSignaturesFromSource(ext, src string) []string {
	var out []string
	add := func(re *regexp.Regexp) {
		for _, m := range re.FindAllStringSubmatch(src, 8) {
			if len(m) < 3 {
				continue
			}
			out = append(out, strings.TrimSpace(m[1]+m[2]))
		}
	}
	switch ext {
	case ".go":
		for _, m := range reGoExport.FindAllStringSubmatch(src, 12) {
			if len(m) < 4 {
				continue
			}
			if !goReceiverTypeExported(m[1]) {
				continue
			}
			out = append(out, strings.TrimSpace(m[2]+m[3]))
		}
	case ".py":
		for _, m := range rePyDef.FindAllStringSubmatch(src, 8) {
			if len(m) < 3 || strings.HasPrefix(m[1], "_") {
				continue
			}
			out = append(out, strings.TrimSpace(m[1]+m[2]))
		}
	case ".js", ".ts", ".jsx", ".tsx":
		add(reJSExport)
	case ".rs":
		add(reRustPub)
	case ".java":
		add(reJavaPublic)
	case ".cs":
		add(reCSharpPub)
	}
	return out
}
