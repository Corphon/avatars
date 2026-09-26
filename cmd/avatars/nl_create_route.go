package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// langWord matches a programming-language name as its own word.
// "going" and "javascript" must not count as Go or Java.
var langWord = regexp.MustCompile(`(?:^|[^a-z0-9])(golang|python|typescript|javascript|nodejs|kotlin|csharp|rust|java|node|go|py|js|ts|rs)(?:[^a-z0-9]|$)`)

// creationDelivery is how new software should be produced.
// Determined locally so the intent LLM is not spent on an already-known
// fork (prefix-cache hit rate: skip the router entirely).
type creationDelivery int

const (
	creationDeliveryNone creationDelivery = iota
	creationDeliveryWorkflow
	creationDeliveryClarify
)

// classifyCreationDelivery picks workflow vs one-file vs ask, for any common
// language. It does not rewrite the LLM system prefix.
//
// Policy:
//   - Named file / explicit script / HTML / game → leave to existing script paths.
//   - Empty-scaffold "new project" phrasing → leave to bootstrap.
//   - Product work, self-planned phases, libraries, coded web apps → run workflow.
//   - Language + create, no named file, and no software project yet → ask.
func classifyCreationDelivery(input string) creationDelivery {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if lowered == "" {
		return creationDeliveryNone
	}
	create := looksLikeNewSoftwareCreate(lowered)
	if !create && (isAnalysisRequest(lowered) || isEditIntent(lowered)) {
		return creationDeliveryNone
	}

	explicitFile := extractSimpleScriptPath(input) != ""
	// Logs, notes, and a README are not a project. A manifest or source tree is.
	// This stays in front of the intent LLM so the router prefix is not spent.
	greenfield := !workspaceHasSoftwareProject()

	// Keep legacy empty-scaffold NL on bootstrap. Product work that happens
	// to say "project" still stays there so existing scaffold tests hold.
	if looksLikeBootstrapProjectRequest(lowered) && !looksLikeInProcessLibraryCreate(lowered) {
		return creationDeliveryNone
	}

	if looksLikeInProcessLibraryCreate(lowered) {
		return creationDeliveryWorkflow
	}
	if looksLikeCodedWebApp(lowered) && create && !explicitFile {
		return creationDeliveryWorkflow
	}
	if looksLikeSelfPlannedCreation(lowered) && create && !explicitFile {
		return creationDeliveryWorkflow
	}
	if looksLikeOneShotFileCreate(lowered) && !looksLikeSelfPlannedCreation(lowered) {
		return creationDeliveryNone
	}
	if explicitFile && !looksLikeSelfPlannedCreation(lowered) {
		return creationDeliveryNone
	}
	if create && looksLikeProductBehavior(lowered) && !explicitFile {
		return creationDeliveryWorkflow
	}

	if greenfield && create && !explicitFile && !looksLikeOneShotFileCreate(lowered) {
		if looksLikeVagueCreationScope(lowered) {
			return creationDeliveryClarify
		}
		if hasProgrammingLanguageSignal(lowered) && hasCLISignal(lowered) {
			return creationDeliveryWorkflow
		}
		if hasProgrammingLanguageSignal(lowered) && !looksLikeProductBehavior(lowered) {
			return creationDeliveryClarify
		}
	}
	return creationDeliveryNone
}

// stackSwitchDecision asks before a casual "switch the language" starts a new
// task. Confirmed migrations still go to the router. Skips the intent LLM.
func stackSwitchDecision(input string) (naturalLanguageDecision, bool) {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if !looksLikeUnconfirmedStackSwitch(lowered) {
		return naturalLanguageDecision{}, false
	}
	body := strings.TrimSpace(input)
	return naturalLanguageDecision{
		Kind:     naturalLanguageDecisionClarify,
		Reason:   "language switch needs confirmation before a new task",
		Question: "Switching the project language is a migration, not a small edit. Confirm the scope before I start a new task.",
		Options: []string{
			fmt.Sprintf("avatars run --new-task --permission-mode acceptEdits %q", "migrate the current project: "+body),
			"keep the current language and continue the existing task",
		},
		Confidence: 88,
	}, true
}

func looksLikeUnconfirmedStackSwitch(lowered string) bool {
	if lowered == "" || looksLikeNewSoftwareCreate(lowered) {
		return false
	}
	if !hasProgrammingLanguageSignal(lowered) {
		return false
	}
	if !containsAnyIntentToken(lowered,
		"改成", "换成", "改用", "换用",
		"switch to", "rewrite in", "port to", "migrate to", "convert to", "convert the",
	) {
		return false
	}
	if containsAnyIntentToken(lowered,
		"别改", "不要改", "别换", "不要换", "don't switch", "do not switch", "keep go", "keep python",
		"确认", "按默认", "全量迁移", "go ahead", "do it", "开工",
	) {
		return false
	}
	return true
}

func looksLikeStorageEngineSwitch(lowered string) bool {
	if lowered == "" || looksLikeNewSoftwareCreate(lowered) {
		return false
	}
	if !containsAnyIntentToken(lowered,
		"换成", "改成", "改用", "换用",
		"switch to", "migrate to", "move to",
	) {
		return false
	}
	return containsAnyIntentToken(lowered,
		"sqlite", "postgres", "postgresql", "mysql", "mongodb", "redis",
		"json 文件", "json file", "本地 json",
	)
}

func looksLikeHoldOffStorage(lowered string) bool {
	return looksLikeForbiddenMutationAsk(lowered) || containsAnyIntentToken(lowered,
		"先别动手", "先别动", "确认一下", "再改",
		"not yet", "hold off", "don't start", "do not start yet",
	)
}

// storageSwitchDecision confirms a store-engine change on the current task.
// It does not open a new task and does not call the intent model.
func storageSwitchDecision(input string) (naturalLanguageDecision, bool) {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if !looksLikeStorageEngineSwitch(lowered) {
		return naturalLanguageDecision{}, false
	}
	body := strings.TrimSpace(input)
	hold := looksLikeHoldOffStorage(lowered)
	confirmed := !hold && containsAnyIntentToken(lowered,
		"开工", "开始改", "按这个改", "do it", "go ahead", "apply it", "apply the change",
	)
	if confirmed {
		if transcript := latestResumableTranscriptPath("."); transcript != "" {
			return naturalLanguageDecision{
				Kind:       naturalLanguageDecisionSafeRun,
				Reason:     "storage change stays on the current task",
				Command:    []string{"run", "--resume", transcript, "--permission-mode", "acceptEdits", "apply this storage change on the current task, do not open a new task: " + body},
				Confidence: 90,
			}, true
		}
	}
	opts := []string{"keep the current storage and continue the existing task"}
	if transcript := latestResumableTranscriptPath("."); transcript != "" {
		opts = append(opts, fmt.Sprintf("avatars run --resume %s --permission-mode acceptEdits %q", transcript, "apply this storage change on the current task, do not open a new task: "+body))
	} else {
		opts = append(opts, "apply the storage change on the current task after you confirm; do not open a new task")
	}
	return naturalLanguageDecision{
		Kind:       naturalLanguageDecisionClarify,
		Reason:     "storage change needs confirmation on the current task",
		Question:   "Changing the store stays on the current task. It does not start a new project. If the plan says standard library only, a driver such as sqlite conflicts with that decision. Confirm before any files change.",
		Options:    opts,
		Confidence: 90,
	}, true
}

func creationDeliveryDecision(input string) (naturalLanguageDecision, bool) {
	switch classifyCreationDelivery(input) {
	case creationDeliveryWorkflow:
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionSafeRun,
			Reason:     "new software uses the project workflow, not a one-file script",
			Command:    creationWorkflowRunCommand(input),
			Confidence: 90,
		}, true
	case creationDeliveryClarify:
		path := nextAvailableScriptPath(inferSimpleScriptPath(input))
		body := strings.TrimSpace(input)
		return naturalLanguageDecision{
			Kind:     naturalLanguageDecisionClarify,
			Reason:   "new software in an empty workspace with no file and no product shape",
			Question: "This looks like new software, but I cannot tell whether you want a single file or a module/project that I should plan first. Which delivery do you want?",
			Options: []string{
				fmt.Sprintf("avatars run --new-task --permission-mode acceptEdits %q", body),
				fmt.Sprintf("avatars script --apply --llm %s %q", path, body),
			},
			Confidence: 80,
		}, true
	default:
		return naturalLanguageDecision{}, false
	}
}

func creationWorkflowRunCommand(input string) []string {
	return libraryCreateRunCommand(input)
}

// rewriteScriptAwayFromProjectWorkflow maps script/bootstrap misroutes onto
// acceptEdits run when the NL already classified as project workflow.
// Does not touch LLM system prefixes (intent cache).
func rewriteScriptAwayFromProjectWorkflow(input string, cmd []string) []string {
	if len(cmd) == 0 {
		return cmd
	}
	if classifyCreationDelivery(input) != creationDeliveryWorkflow {
		return cmd
	}
	switch cmd[0] {
	case "run":
		return cmd
	case "script":
		return creationWorkflowRunCommand(input)
	case "bootstrap":
		if looksLikeBootstrapProjectRequest(strings.ToLower(input)) &&
			!looksLikeInProcessLibraryCreate(strings.ToLower(input)) {
			return cmd
		}
		return creationWorkflowRunCommand(input)
	default:
		return cmd
	}
}

func looksLikeNewSoftwareCreate(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"弄一个", "弄个", "写一个", "写个", "做一个", "做个", "搞一个", "搞个", "做成", "新建", "从零",
		"搭一个", "来一个", "来个", "生成一个",
		"create a", "write a", "build a", "make a", "generate a",
		"create an", "write an", "build an", "make an",
	)
}

func looksLikeOneShotFileCreate(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"脚本", "script", "单文件", "single file", "single-file", "one file",
		"网页", "webpage", "web page", "html page", "html file",
		"游戏", "game", "页面",
		"calculator", "计算器",
		"draw a", "paint a",
	)
}

func looksLikeSelfPlannedCreation(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"阶段你自己排", "自己排阶段", "自己规划阶段", "自己规划",
		"分阶段交付", "分阶段", "先出计划",
		"plan the phases", "plan phases yourself", "you plan the phases",
		"arrange the phases", "you decide the phases",
		"multi-phase", "greenfield",
	)
}

func looksLikeProductBehavior(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"去重", "dedup", "dedupe", "md5", "sha256", "sha1",
		"注册", "登录", "jwt", "oauth", "postgresql", "mysql", "sqlite",
		"rest api", "http server", "优先队列", "bloom",
		"保存到", "save to", "本地 json",
		"add/list", "add list",
		"带测试", "最小测试", "with tests", "unit tests",
	)
}

func looksLikeVagueCreationScope(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"你看着办", "你决定", "你来定", "你定", "功能你看着办", "随便", "小东西",
		"you decide", "you pick", "you choose", "your call", "up to you", "whatever you", "small thing",
		"you figure", "anything is fine",
	)
}

func hasCLISignal(lowered string) bool {
	if containsAnyIntentToken(lowered,
		"命令行", "cli tool", "command-line", "command line", "cmdline",
		"-cli", " go-cli", " rust-cli",
	) {
		return true
	}
	if strings.Contains(lowered, " cli") || strings.HasPrefix(lowered, "cli ") || strings.HasSuffix(lowered, " cli") {
		return true
	}
	return false
}

func hasProgrammingLanguageSignal(lowered string) bool {
	if containsAnyIntentToken(lowered,
		"javascript", "typescript", "python", "golang", "rust",
		"csharp", "c#", "c sharp", "kotlin", "nodejs", "node.js", "node js",
		"用go", "用 go", "go写", "go 写", "go语言", "go程序", "go 程序",
		"go cli", "go-cli", "in go", "with go",
		"用python", "用 python", "用py", "用 py",
		"用rust", "用 rust",
		"用js", "用 js", "用ts", "用 ts",
		"用node", "用 node",
		"用c#", "用csharp", "用 csharp",
		".go", ".py", ".rs", ".cs", ".kt",
	) {
		return true
	}
	if strings.Contains(lowered, "java") && !strings.Contains(lowered, "javascript") {
		return true
	}
	if strings.Contains(lowered, " a go ") || strings.HasPrefix(lowered, "go ") {
		return true
	}
	if m := langWord.FindStringSubmatch(lowered); len(m) > 1 {
		switch m[1] {
		case "java":
			return !strings.Contains(lowered, "javascript")
		default:
			return true
		}
	}
	return false
}

// workspaceHasSoftwareProject is true when the workspace already holds a
// language manifest or implementation sources. Notes, logs, and README files
// do not count. Dot directories and the avatars home directory do not count.
func workspaceHasSoftwareProject() bool {
	entries, err := os.ReadDir(".")
	if err != nil {
		return false
	}
	for _, ent := range entries {
		name := ent.Name()
		if name == ".avatars" || name == ".git" || name == "avatars" || strings.HasPrefix(name, ".") {
			continue
		}
		if ent.IsDir() {
			switch strings.ToLower(name) {
			case "cmd", "src", "app", "internal", "crates":
				return true
			}
			continue
		}
		if isProjectManifestName(name) || isImplementationSourceName(name) {
			return true
		}
	}
	return false
}

func isProjectManifestName(name string) bool {
	switch strings.ToLower(name) {
	case "go.mod", "cargo.toml", "package.json", "pyproject.toml", "setup.py",
		"requirements.txt", "pipfile", "pom.xml", "build.gradle", "build.gradle.kts",
		"cmakelists.txt", "composer.json", "gemfile", "mix.exs", "go.sum":
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".csproj") || strings.HasSuffix(lower, ".fsproj") || strings.HasSuffix(lower, ".sln")
}

func isImplementationSourceName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".py", ".rs", ".java", ".kt", ".scala", ".cs", ".fs",
		".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".c", ".h", ".cc", ".cpp", ".hpp",
		".rb", ".php", ".swift", ".vue", ".svelte":
		return true
	default:
		return false
	}
}

// deterministicNLReason reports decisions made before the intent LLM.
// Callers must not spend a second router call on these.
func deterministicNLReason(reason string) bool {
	switch {
	case strings.HasPrefix(reason, "new software"),
		strings.HasPrefix(reason, "off-topic chat"),
		strings.HasPrefix(reason, "talk aside"),
		strings.HasPrefix(reason, "language switch"),
		strings.HasPrefix(reason, "resume continuation"),
		strings.HasPrefix(reason, "nothing to resume"),
		strings.HasPrefix(reason, "already delivered"),
		strings.HasPrefix(reason, "stage gallery"),
		strings.HasPrefix(reason, "no-mutate follow-up"),
		strings.HasPrefix(reason, "storage change"):
		return true
	default:
		return false
	}
}
