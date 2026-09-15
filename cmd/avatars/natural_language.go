package main

import (
	ctxpkg "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"avatars/internal/app"
	"avatars/internal/llm"
	memstore "avatars/internal/memory"
	"avatars/internal/projectfiles"
	"avatars/internal/tasks"
	"avatars/internal/workflow"
)

// llmCallTimeout is the default timeout for LLM classification and intent
// analysis calls. These sit on the critical path for intent routing; 120 s
// gives enough headroom for model latency while preventing indefinite hangs
// that would permanently block the REPL session.
const llmCallTimeout = 120 * time.Second

func llmCallContext() (ctxpkg.Context, func()) {
	ctx, cancel := ctxpkg.WithTimeout(ctxpkg.Background(), llmCallTimeout)
	return ctx, cancel
}

type naturalLanguageContext string

const (
	naturalLanguageContextIntent naturalLanguageContext = "intent"
	naturalLanguageContextREPL   naturalLanguageContext = "repl"
)

type naturalLanguageRouteKind string

const (
	naturalLanguageRouteNone         naturalLanguageRouteKind = "none"
	naturalLanguageRouteDirectAnswer naturalLanguageRouteKind = "direct_answer"
	naturalLanguageRouteSafeRun      naturalLanguageRouteKind = "safe_run"
	naturalLanguageRouteClarify      naturalLanguageRouteKind = "clarify"
)

type naturalLanguageRoute struct {
	Kind       naturalLanguageRouteKind
	Answer     string
	Command    []string
	Summary    string
	Question   string
	Options    []string
	Confidence int
}

type naturalLanguageDecisionKind string

const (
	naturalLanguageDecisionNone    naturalLanguageDecisionKind = "none"
	naturalLanguageDecisionAnswer  naturalLanguageDecisionKind = "answer"
	naturalLanguageDecisionMemory  naturalLanguageDecisionKind = "memory_answer"
	naturalLanguageDecisionSafeRun naturalLanguageDecisionKind = "safe_run"
	naturalLanguageDecisionGuarded naturalLanguageDecisionKind = "guarded"
	naturalLanguageDecisionClarify naturalLanguageDecisionKind = "clarify"
)

type naturalLanguageDecision struct {
	Kind       naturalLanguageDecisionKind
	Reason     string // machine/log-facing diagnosis (may be verbose)
	Question   string // user-facing clarify text (short, actionable)
	Answer     string
	Command    []string
	Options    []string // optional numbered recovery choices for clarify/guarded
	Confidence int
}

type naturalLanguageLLMDecision struct {
	Action     string `json:"action"`
	AnswerID   string `json:"answer_id"`
	Question   string `json:"question"`
	Confidence int    `json:"confidence"`
}

type localQuestionHandler struct {
	Name        string
	Category    string
	Description string
	Priority    int
	Match       func(string) bool
	Answer      func(string) (string, error)
}

var localQuestionHandlers = []localQuestionHandler{
	{
		Name:        "smalltalk-greeting",
		Category:    "conversation",
		Description: "answer harmless greetings without launching a task",
		Priority:    1,
		Match: func(lowered string) bool {
			return looksLikeGreetingQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return summarizeGreeting(), nil
		},
	},
	{
		Name:        "capability-summary",
		Category:    "conversation",
		Description: "explain what the CLI can do and how coding is guarded",
		Priority:    2,
		Match: func(lowered string) bool {
			return looksLikeCapabilityQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return summarizeCapabilities(), nil
		},
	},
	{
		Name:        "last-action-summary",
		Category:    "tasks",
		Description: "explain what the previous routed turn did",
		Priority:    3,
		Match: func(lowered string) bool {
			return looksLikeLastActionQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return summarizeLastAction(root)
		},
	},
	{
		Name:        "conversational-repair",
		Category:    "conversation",
		Description: "repair follow-up complaints without falling back to generic clarification",
		Priority:    9,
		Match: func(lowered string) bool {
			return looksLikeConversationalRepairQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return summarizeConversationalRepair(), nil
		},
	},
	{
		Name:        "analysis-report-quality",
		Category:    "tasks",
		Description: "judge latest analysis report or recall last findings (S6.3 merge)",
		Priority:    8,
		Match: func(lowered string) bool {
			return looksLikeAnalysisReportQualityQuestion(lowered) || looksLikeLastAnalysisFindingsQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			if findings, err := summarizeLastAnalysisFindings(root); err == nil && strings.TrimSpace(findings) != "" {
				return findings, nil
			}
			return summarizeLatestAnalysisReportQuality(root)
		},
	},
	{
		Name:        "active-file-summary",
		Category:    "files",
		Description: "summarize the current attached file context without launching a task",
		Priority:    4,
		Match: func(lowered string) bool {
			if looksLikeContinuationWorkRequest(lowered) {
				return false
			}
			return looksLikeActiveFileSummaryQuestion(lowered) || looksLikeAttachedFileQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			if getReplFileContext() == nil {
				return "File context:\n- no active file. Use @path or --from-file first.", nil
			}
			return summarizeAttachedFile(getReplFileContext().Path, getReplFileContext().Content), nil
		},
	},
	{
		Name:        "project-overview",
		Category:    "docs",
		Description: "summarize project purpose and code layout (S6.3: merged code-location)",
		Priority:    10,
		Match: func(lowered string) bool {
			return looksLikeProjectOverviewQuestion(lowered) || looksLikeProjectCodeLocationQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			overview, err := summarizeProjectOverview(root)
			if err != nil {
				return "", err
			}
			if loc, locErr := summarizeProjectCodeLocation(root); locErr == nil && strings.TrimSpace(loc) != "" {
				return overview + "\n\n" + loc, nil
			}
			return overview, nil
		},
	},
	{
		Name:        "model-identity",
		Category:    "llm",
		Description: "answer what model or LLM is active from local config and runtime status",
		Priority:    10,
		Match: func(lowered string) bool {
			return looksLikeModelIdentityQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return summarizeModelIdentity(root)
		},
	},
	{
		Name:        "project-conversational-evaluation",
		Category:    "conversation",
		Description: "answer lightweight project opinion questions without launching a full task",
		Priority:    11,
		Match: func(lowered string) bool {
			return looksLikeProjectConversationalEvaluationQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return summarizeProjectConversationalEvaluation(root)
		},
	},
	{
		Name:        "project-files",
		Category:    "files",
		Description: "count or list project files (S6.3: merged file_count + file_list)",
		Priority:    20,
		Match: func(lowered string) bool {
			return looksLikeProjectFileCountQuestion(lowered) || looksLikeProjectFileListQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return summarizeProjectFiles(root)
		},
	},
	{
		Name:        "project-todo-fixme-search",
		Category:    "files",
		Description: "search project files for TODO/FIXME/HACK/XXX comments and list them with file:line",
		Priority:    21,
		Match: func(lowered string) bool {
			return looksLikeTodoFixmeSearchQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return searchTodoFixmeInProject(root)
		},
	},
	{
		Name:        "sqlite-db-query",
		Category:    "data",
		Description: "query the project SQLite database for table stats and row counts",
		Priority:    24,
		Match: func(lowered string) bool {
			return looksLikeSQLQueryQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return queryProjectDatabase(root)
		},
	},
	{
		Name:        "shell-analysis-catchall",
		Category:    "code",
		Description: "catch-all shell-based project analysis for tasks without specific handlers",
		Priority:    25,
		Match: func(lowered string) bool {
			return looksLikeShellAnalysisQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return executeShellAnalysis(root)
		},
	},
	{
		Name:        "large-file-tail-read",
		Category:    "files",
		Description: "read the last N lines of a large file (process_record.md, etc.)",
		Priority:    23,
		Match: func(lowered string) bool {
			return looksLikeLargeFileTailQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return readLargeFileTail(root)
		},
	},
	{
		Name:        "project-inspection",
		Category:    "code",
		Description: "project inspection: tree / imports / line-count / missing tests (S6.3 merge)",
		Priority:    22,
		Match: func(lowered string) bool {
			return looksLikeMissingTestsQuestion(lowered) ||
				looksLikeDirectoryTreeQuestion(lowered) ||
				looksLikeTopImportsQuestion(lowered) ||
				looksLikeCodeLineCountQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return summarizeProjectInspection(root)
		},
	},
	{
		Name:        "project-manifest-summary",
		Category:    "manifests",
		Description: "summarize local manifest files and dependency signals",
		Priority:    5,
		Match: func(lowered string) bool {
			return looksLikeProjectManifestQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return summarizeProjectManifests(root)
		},
	},
	{
		Name:        "docs-overview",
		Category:    "docs",
		Description: "summarize the local docs index and markdown entry points",
		Priority:    8,
		Match: func(lowered string) bool {
			return looksLikeDocsOverviewQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			return summarizeDocsOverview(root)
		},
	},
	{
		Name:        "recent-task-summary",
		Category:    "tasks",
		Description: "summarize recent tasks or pending approval (S6.3: merged approval)",
		Priority:    12,
		Match: func(lowered string) bool {
			return looksLikeRecentTaskQuestion(lowered) || looksLikeApprovalCommandQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			if summary := summarizeLatestApprovalBlock(root); summary != "" {
				return summary, nil
			}
			return summarizeRecentTasks(root)
		},
	},
	{
		Name:        "result-location",
		Category:    "tasks",
		Description: "answer where previous results/scripts/reports can be found (S6.3: merged script-location)",
		Priority:    13,
		Match: func(lowered string) bool {
			return looksLikeResultLocationQuestion(lowered) || looksLikeScriptLocationQuestion(lowered)
		},
		Answer: func(root string) (string, error) {
			if loc, err := summarizeScriptLocation(root); err == nil && strings.TrimSpace(loc) != "" {
				return loc, nil
			}
			return summarizeResultLocation(root)
		},
	},
}

// S6.3: skills/memory/workflow/llm status answers are resolved via LLM answer_id
// in answerClassifiedLocalQuestion — not listed here (avoids keyword Match sprawl).

func summarizeProjectFiles(root string) (string, error) {
	files, err := listProjectFiles(root, 24)
	if err != nil {
		return "", err
	}
	count, err := countProjectFiles(root)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return fmt.Sprintf("Project files: %d\nTop entries: none\nExcluded directories: .git, .avatars, avatars, node_modules, vendor, dist, build", count), nil
	}
	return fmt.Sprintf("Project files: %d\nTop entries: %s\nExcluded directories: .git, .avatars, avatars, node_modules, vendor, dist, build", count, strings.Join(files, ", ")), nil
}

func summarizeProjectInspection(root string) (string, error) {
	var parts []string
	if tree, err := generateDirectoryTree(root); err == nil && strings.TrimSpace(tree) != "" {
		parts = append(parts, "Directory tree:\n"+tree)
	}
	if lines, err := countCodeLines(root); err == nil && strings.TrimSpace(lines) != "" {
		parts = append(parts, lines)
	}
	if imports, err := analyzeTopGoImports(root); err == nil && strings.TrimSpace(imports) != "" {
		parts = append(parts, imports)
	}
	if missing, err := findMissingTests(root); err == nil && strings.TrimSpace(missing) != "" {
		parts = append(parts, missing)
	}
	if len(parts) == 0 {
		return "No project inspection signals found.", nil
	}
	return strings.Join(parts, "\n\n"), nil
}

func tryNaturalLanguageDirectAnswer(line string, output io.Writer) (bool, error) {
	route, handled, err := resolveNaturalLanguageQuestion(line, replCommandOptions{}, naturalLanguageContextREPL)
	if err != nil {
		return true, err
	}
	if !handled || route.Kind != naturalLanguageRouteDirectAnswer {
		return false, nil
	}
	fmt.Fprintln(output, route.Answer)
	return true, nil
}

func resolveNaturalLanguageQuestion(input string, options replCommandOptions, context naturalLanguageContext) (naturalLanguageRoute, bool, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return naturalLanguageRoute{}, false, nil
	}

	decision := classifyNaturalLanguageQuestion(trimmed, options, context)
	switch decision.Kind {
	case naturalLanguageDecisionAnswer:
		return naturalLanguageRoute{
			Kind:       naturalLanguageRouteDirectAnswer,
			Answer:     decision.Answer,
			Summary:    decision.Reason,
			Confidence: decision.Confidence,
		}, true, nil
	case naturalLanguageDecisionMemory:
		return naturalLanguageRoute{
			Kind:       naturalLanguageRouteDirectAnswer,
			Answer:     decision.Answer,
			Summary:    decision.Reason,
			Confidence: decision.Confidence,
		}, true, nil
	case naturalLanguageDecisionSafeRun:
		return naturalLanguageRoute{
			Kind:       naturalLanguageRouteSafeRun,
			Command:    decision.Command,
			Summary:    decision.Reason,
			Confidence: decision.Confidence,
		}, true, nil
	case naturalLanguageDecisionGuarded:
		// S1.1: Never dump raw Error: for risky intents. Mirror Claude Code
		// soft_deny — explain risk + give explicit escape-hatch commands.
		return naturalLanguageRoute{
			Kind:       naturalLanguageRouteClarify,
			Summary:    decision.Reason,
			Question:   formatGuardedClarifyQuestion(trimmed, decision),
			Options:    decision.Options,
			Confidence: decision.Confidence,
		}, true, nil
	case naturalLanguageDecisionClarify:
		return naturalLanguageRoute{
			Kind:       naturalLanguageRouteClarify,
			Summary:    decision.Reason,
			Question:   formatUserClarifyQuestion(trimmed, decision),
			Options:    decision.Options,
			Confidence: decision.Confidence,
		}, true, nil
	}
	return naturalLanguageRoute{}, false, nil
}

func classifyNaturalLanguageQuestion(input string, options replCommandOptions, context naturalLanguageContext) naturalLanguageDecision {
	return classifyNaturalLanguageQuestionWithOptions(input, options, context, true)
}

func classifyNaturalLanguageQuestionWithoutLLM(input string, options replCommandOptions, context naturalLanguageContext) naturalLanguageDecision {
	return classifyNaturalLanguageQuestionWithOptions(input, options, context, false)
}

func classifyNaturalLanguageQuestionWithOptions(input string, options replCommandOptions, context naturalLanguageContext, allowLLM bool) naturalLanguageDecision {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if lowered == "" {
		return naturalLanguageDecision{Kind: naturalLanguageDecisionNone}
	}

	// SAFETY (I3 fix): The destructive keyword pre-check has been REMOVED.
	// Keywords like "格式化" were blocking benign requests ("格式化JSON")
	// before the LLM could interpret context. The LLM is now the primary
	// safety classifier.
	//
	// A narrow post-LLM safety audit (in safety_audit.go) catches ONLY
	// unconditionally dangerous patterns (rm -rf /, fork bombs, etc.)
	// as a safety net AFTER the LLM has made its decision.
	//
	// For context-dependent safety concerns (data exfiltration, destructive
	// commands), we rely on the LLM's enhanced system prompt to classify
	// them as "guarded".

	// UX: Reuse previous result for write-to-file follow-up requests.
	if answer, ok, err := answerPreviousResultWriteRequest(strings.TrimSpace(input), options, context); err == nil && ok {
		return naturalLanguageDecision{Kind: naturalLanguageDecisionMemory, Reason: "latest result reused for requested output path", Answer: answer, Confidence: 95}
	}

	// UX: Auto-repair latest verifier failure.
	if command, summary, ok := buildLatestVerifierFailureRepairCommand(strings.TrimSpace(input), context); ok {
		return naturalLanguageDecision{Kind: naturalLanguageDecisionSafeRun, Reason: summary, Command: command, Confidence: 90}
	}

	// UX: Answer from task memory when possible.
	if answer, ok, err := answerTaskMemoryQuestionForClassifiedIntent(strings.TrimSpace(input), options, context); err == nil && ok && looksLikeTaskMemoryFollowUpQuestion(lowered) {
		return naturalLanguageDecision{Kind: naturalLanguageDecisionMemory, Reason: "task memory recall", Answer: answer, Confidence: 92}
	}

	// S2 NL: Wire existing workflow action detectors into the hot path
	// (IsConfirmPlan / IsReplan already used by runtime; were invisible to intent).
	if workflow.IsConfirmPlan(input) || workflow.IsReplan(input) {
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionSafeRun,
			Reason:     "workflow plan action (confirm/replan)",
			Command:    buildWorkflowPlanActionCommand(input, options),
			Confidence: 94,
		}
	}
	if answer, ok := answerWorkflowPhaseStatusQuestion(input); ok {
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionAnswer,
			Reason:     "workflow phase status (local)",
			Answer:     answer,
			Confidence: 95,
		}
	}
	if answer, ok := answerNoMutateFollowUp(input); ok {
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionAnswer,
			Reason:     "no-mutate follow-up (local)",
			Answer:     answer,
			Confidence: 94,
		}
	}
	// S6.3: Status Q&A that must work offline (same pattern as phase status).
	// Resolves via answer_id summarizers — not new localQuestionHandlers.
	if answer, ok := answerOfflineStatusQuestion(input); ok {
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionAnswer,
			Reason:     "offline status answer_id",
			Answer:     answer,
			Confidence: 94,
		}
	}

	// S1.5: High-confidence local Q&A BEFORE the LLM router (Claude Code
	// side-question pattern: prose-only, no agent loop). Whitelist only —
	// never run shell/analysis catchalls here or they steal work requests.
	if answer, ok, err := answerHighConfidenceLocalQuestion(lowered); err == nil && ok {
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionAnswer,
			Reason:     "deterministic local inspection (pre-LLM)",
			Answer:     answer,
			Confidence: 96,
		}
	}

	if nd, ok := continuationWorkSafeRunDecision(input, lowered); ok {
		return nd
	}
	if nd, ok := stageSketchSafeRunDecision(input); ok {
		return nd
	}

	// NL5: LLM-first routing — the primary decision path for work requests.
	// Give the LLM the full CLI_guide.md + cli_actions.yaml catalog.
	if allowLLM && (context == naturalLanguageContextIntent || context == naturalLanguageContextREPL) {
		if decision, ok := routeIntentViaLLM(input); ok {
			nd := llmRouterDecisionToNaturalLanguage(decision)
			if nd.Kind == naturalLanguageDecisionSafeRun {
				nd.Command = rewriteBootstrapAwayFromLibrary(input, nd.Command)
				nd.Command = rewriteCommandTowardStageSketch(input, nd.Command)
			}
			// Checklist-align / resume work must not stay a chat answer.
			if (nd.Kind == naturalLanguageDecisionAnswer || nd.Kind == naturalLanguageDecisionMemory) &&
				(looksLikeChecklistProgressReconcile(lowered) || looksLikeResumeContinuationIntent(lowered)) {
				if coerced, ok := continuationWorkSafeRunDecision(input, lowered); ok {
					coerced.Reason = "coerced status/direct_answer to run (checklist/resume work)"
					return coerced
				}
			}
			// S1.3: Fill empty safe_run command with the original user text.
			if nd.Kind == naturalLanguageDecisionSafeRun {
				nd.Command = restoreOriginalRunTaskText(nd.Command, input)
			}
			if nd.Kind == naturalLanguageDecisionSafeRun && isEmptySafeRunCommand(nd.Command) {
				mode := naturalLanguagePermissionMode(lowered)
				if mode == "" {
					mode = "default"
				}
				if looksLikeResumeContinuationIntent(lowered) {
					if transcript := latestResumableTranscriptPath("."); transcript != "" {
						cmd := []string{"run", "--resume", transcript}
						if id := taskIDFromTranscriptPath(transcript); id != "" {
							cmd = append(cmd, "--task", id)
						}
						cmd = append(cmd, "--permission-mode", mode, strings.TrimSpace(input))
						nd.Command = cmd
						nd.Reason = nd.Reason + "; filled resume command from interrupted-build intent"
					} else {
						nd.Command = []string{"run", "--new-task", "--permission-mode", mode, strings.TrimSpace(input)}
						nd.Reason = nd.Reason + "; filled empty command from user text"
					}
				} else {
					nd.Command = []string{"run", "--new-task", "--permission-mode", mode, strings.TrimSpace(input)}
					nd.Reason = nd.Reason + "; filled empty command from user text"
				}
			}
			if looksLikeNoMutateFollowUp(lowered) || looksLikeForbiddenMutationAsk(lowered) {
				if ans, ok := answerNoMutateFollowUp(input); ok {
					return naturalLanguageDecision{
						Kind:       naturalLanguageDecisionAnswer,
						Reason:     "no-mutate follow-up (overrides run/clarify)",
						Answer:     ans,
						Confidence: 94,
					}
				}
				if looksLikeForbiddenMutationAsk(lowered) && nd.Kind == naturalLanguageDecisionSafeRun {
					return naturalLanguageDecision{
						Kind:       naturalLanguageDecisionAnswer,
						Reason:     "forbidden mutation (overrides run)",
						Answer:     "No files changed. This is a question, not a new coding task.",
						Confidence: 94,
					}
				}
			}
			if nd.Kind == naturalLanguageDecisionAnswer && looksLikeWorkflowPhaseStatusAsk(lowered) {
				if ans, ok := answerWorkflowPhaseStatusQuestion(input); ok {
					nd.Answer = ans
					nd.Reason = "workflow phase status (local; not style-repair)"
				}
			} else if nd.Kind == naturalLanguageDecisionAnswer && nd.Answer != "" {
				// Special case: next_steps uses LLM to generate contextual suggestions.
				if nd.Answer == "next_steps" {
					if suggestions := generateNextSteps(); suggestions != "" {
						nd.Answer = suggestions
					} else {
						nd.Answer = "Unable to generate suggestions. Continue working, or type /help to see available commands."
					}
				} else if answer, ok, _ := answerClassifiedLocalQuestion(input, nd.Answer, options, context); ok {
					nd.Answer = answer
				} else {
					// S1.2: Never leak raw answer_id. Fall back to local handlers
					// keyed by the original user text, then a short clarify.
					if answer, ok, err := answerLocalQuestion(lowered); err == nil && ok {
						nd.Answer = answer
						nd.Reason = "llm answer_id unresolved; local handler fallback"
					} else {
						return naturalLanguageDecision{
							Kind:       naturalLanguageDecisionClarify,
							Reason:     "unresolved answer_id: " + nd.Answer,
							Question:   unresolvedAnswerClarifyQuestion(input, nd.Answer),
							Confidence: 40,
						}
					}
				}
			}
			if nd.Kind == naturalLanguageDecisionSafeRun {
				nd = coerceContinuationRunToResume(nd, input, lowered)
				nd.Command = sanitizeResumeTranscriptArg(nd.Command)
				if errMsg := validateLLMCommand(nd.Command); errMsg != "" {
					return naturalLanguageDecision{
						Kind:       naturalLanguageDecisionClarify,
						Reason:     "LLM chose invalid command: " + errMsg,
						Question:   invalidCommandClarifyQuestion(input, errMsg),
						Confidence: 25,
					}
				}
				// S1: Vague mutate without a target must clarify even if the
				// LLM guessed a generic run — mirrors AskUserQuestion "only
				// when blocking info is missing".
				if looksLikeVagueEditWithoutTarget(lowered) && safeRunLacksEditTarget(nd.Command) {
					opts := []string{
						`avatars edit --apply <file> "<instruction>"`,
						fmt.Sprintf(`avatars route %q`, strings.TrimSpace(input)),
					}
					q := "Which file should I change, and how? Please add a path and a concrete instruction."
					return naturalLanguageDecision{
						Kind:       naturalLanguageDecisionClarify,
						Reason:     "vague edit without target (post-LLM guard)",
						Question:   q,
						Options:    opts,
						Confidence: 70,
					}
				}
				// I3 fix: Post-LLM safety audit. Only overrides for
				// UNCONDITIONALLY dangerous patterns (rm -rf /, fork bombs,
				// sensitive file exfiltration). Context-dependent keywords
				// like "格式化" are NOT checked here — the LLM understands
				// context ("格式化JSON" vs "格式化磁盘").
				if audit := postLLMSafetyAudit(input, nd); shouldOverrideLLMDecision(audit) {
					return naturalLanguageDecision{
						Kind:       naturalLanguageDecisionGuarded,
						Reason:     "safety audit: " + audit.Reason,
						Question:   "",
						Options:    defaultGuardedOptions(input),
						Confidence: 99,
					}
				}
				// I6 fix: If LLM chose edit for a non-existent file,
				// convert to script --apply --llm instead.
				if fixed, ok := fixEditToScriptForNewFile(nd.Command); ok {
					nd.Command = fixed
				}
			}
			return nd
		}
	}

	// Minimal fallback when LLM is unavailable: answer simple questions
	// locally, clarify everything else. We do NOT guess commands with
	// keyword matching anymore.
	//
	// S1 safety: never suggest AutoRun for mass-wipe / system destruction
	// when the LLM is down — that was teaching users to `avatars run "删除所有文件"`.
	if audit := auditInputForUnconditionalRisk(input); shouldOverrideLLMDecision(audit) {
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionGuarded,
			Reason:     "safety audit: " + audit.Reason,
			Question:   "",
			Options:    defaultGuardedOptions(input),
			Confidence: 99,
		}
	}
	if looksLikeVagueEditWithoutTarget(lowered) {
		opts := []string{
			`avatars edit --apply <file> "<instruction>"`,
			fmt.Sprintf(`avatars route %q`, strings.TrimSpace(input)),
		}
		q := "Which file should I change, and how? Please add a path and a concrete instruction."
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionClarify,
			Reason:     "vague edit without target file",
			Question:   q,
			Options:    opts,
			Confidence: 70,
		}
	}
	// S6.3: Offline work routing — reuse existing command builder (no new keywords).
	// allowLLMSpec=false keeps this path free of nested LLM calls.
	// Only invoke when the input already looks like project work; otherwise
	// out-of-scope chat ("weather") would incorrectly become safe_run.
	if looksLikeOfflineWorkRequest(lowered) {
		if command, summary := buildNaturalLanguageTaskCommandWithLLM(input, options, false); len(command) > 0 {
			return naturalLanguageDecision{
				Kind:       naturalLanguageDecisionSafeRun,
				Reason:     summary,
				Command:    command,
				Confidence: 80,
			}
		}
	}
	if answer, ok, err := answerLocalQuestion(lowered); err == nil && ok {
		return naturalLanguageDecision{Kind: naturalLanguageDecisionAnswer, Reason: "deterministic local inspection (LLM unavailable)", Answer: answer, Confidence: 96}
	}

	fb := buildFallbackResponse(input, nil, false, false)
	return naturalLanguageDecision{
		Kind:       naturalLanguageDecisionClarify,
		Reason:     fb.Diagnosis + "\n\n" + fb.Suggestion,
		Question:   fallbackClarifyQuestion(input, fb),
		Options:    fallbackClarifyOptions(input, fb),
		Confidence: 25,
	}
}

func minConfidence(a int, b int) int {
	if a <= 0 {
		return b
	}
	if a < b {
		return a
	}
	return b
}

func naturalLanguageClarifyingQuestion(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed != "" && trimmed != "not a natural-language request" && trimmed != "no safe command route" && trimmed != "llm requested clarification" && trimmed != "outside bounded project task scope" && trimmed != "project question lacks a concrete task action" {
		return trimmed
	}
	return "I cannot determine the next step. Please specify a target, file path, and what action you want me to take."
}

// highConfidenceLocalHandlerNames are safe to answer before the LLM router.
// Catchalls (shell-analysis, broad "分析") must NOT be here — they steal work.
var highConfidenceLocalHandlerNames = map[string]bool{
	"smalltalk-greeting":    true,
	"capability-summary":    true,
	"last-action-summary":   true,
	"conversational-repair": true,
	"result-location":       true,
	"model-identity":        true,
	"project-overview":      true,
	"recent-task-summary":   true,
}

func answerHighConfidenceLocalQuestion(line string) (string, bool, error) {
	lowered := strings.ToLower(strings.TrimSpace(line))
	handlers := append([]localQuestionHandler(nil), localQuestionHandlers...)
	sort.SliceStable(handlers, func(i, j int) bool {
		if handlers[i].Priority == handlers[j].Priority {
			return handlers[i].Name < handlers[j].Name
		}
		return handlers[i].Priority < handlers[j].Priority
	})
	for _, handler := range handlers {
		if !highConfidenceLocalHandlerNames[handler.Name] {
			continue
		}
		if handler.Match != nil && !handler.Match(lowered) {
			continue
		}
		if handler.Answer == nil {
			continue
		}
		answer, err := handler.Answer(".")
		if err != nil {
			return "", true, err
		}
		return answer, true, nil
	}
	return "", false, nil
}

// looksLikeWorkflowPhaseStatusAsk is a planning/progress question about the
// current plan — not a request to implement. Cross-language: same steal
// happens after a Python/JS/Rust/Java/C# library run.
func looksLikeWorkflowPhaseStatusAsk(lowered string) bool {
	lowered = strings.ToLower(strings.TrimSpace(lowered))
	if lowered == "" {
		return false
	}
	if mechanicalLocalAnswersYieldToWork(lowered) {
		return false
	}
	hasPhase := strings.Contains(lowered, "阶段") || strings.Contains(lowered, "phase")
	planning := strings.Contains(lowered, "划阶段") ||
		strings.Contains(lowered, "划分阶段") ||
		strings.Contains(lowered, "阶段划分") ||
		strings.Contains(lowered, "怎么划") ||
		strings.Contains(lowered, "这样划") ||
		strings.Contains(lowered, "几个阶段") ||
		strings.Contains(lowered, "how many phase") ||
		strings.Contains(lowered, "phase count") ||
		(strings.Contains(lowered, "divide") && hasPhase) ||
		(strings.Contains(lowered, "split") && hasPhase)
	progress := strings.Contains(lowered, "做到哪") ||
		strings.Contains(lowered, "进行到哪") ||
		strings.Contains(lowered, "做到第几") ||
		strings.Contains(lowered, "where are we") ||
		(strings.Contains(lowered, "how far") && hasPhase) ||
		looksLikeChecklistContradictionAsk(lowered)
	rationale := (strings.Contains(lowered, "为什么") || strings.Contains(lowered, "依据") || strings.Contains(lowered, "why")) &&
		(hasPhase || strings.Contains(lowered, "划"))
	narrow := strings.Contains(lowered, "当前阶段") ||
		strings.Contains(lowered, "active phase") ||
		strings.Contains(lowered, "which phase") ||
		(strings.Contains(lowered, "第几阶段") &&
			(strings.Contains(lowered, "当前") || strings.Contains(lowered, "现在") ||
				strings.Contains(lowered, "做到") || strings.Contains(lowered, "问一下") ||
				strings.Contains(lowered, "查一下") || strings.Contains(lowered, "看看") ||
				strings.Contains(lowered, "划"))) ||
		(strings.Contains(lowered, "phase") &&
			(strings.Contains(lowered, "current") || strings.Contains(lowered, "status"))) ||
		(strings.Contains(lowered, "进度") && hasPhase)
	return narrow || planning || progress || rationale
}

// answerWorkflowPhaseStatusQuestion answers phase-count / where-are-we questions
// from plan/todo without launching a multi-avatar run.
func answerWorkflowPhaseStatusQuestion(input string) (string, bool) {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if lowered == "" {
		return "", false
	}
	if !looksLikeWorkflowPhaseStatusAsk(lowered) {
		return "", false
	}
	planPath := filepath.Join(".", "docs", "workflow", "avatars_plan.md")
	todoPath := filepath.Join(".", "docs", "workflow", "avatars_todo.md")
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		return "No project plan found yet (docs/workflow/avatars_plan.md). Say what you want to build, or run a task to create the plan.", true
	}
	planStr := string(planBytes)
	meta := workflow.ParsePlanMeta(planStr)
	completed, total := 0, 0
	if todoBytes, err := os.ReadFile(todoPath); err == nil {
		completed, total = workflow.CountTodoProgressFromContent(string(todoBytes))
	}
	activeName := ""
	if meta.ActivePhase >= 1 && meta.ActivePhase <= len(meta.Phases) {
		activeName = strings.TrimSpace(meta.Phases[meta.ActivePhase-1].Name)
	}
	msg := fmt.Sprintf("Phase count: %d. Active phase: %d", meta.PhaseCount, meta.ActivePhase)
	if activeName != "" {
		msg += " (" + activeName + ")"
	}
	if status := strings.TrimSpace(meta.Status); status != "" {
		msg += fmt.Sprintf(". Plan status: %s", status)
	}
	if total > 0 {
		msg += fmt.Sprintf(". Checklist: %d/%d done", completed, total)
	}
	if meta.PhaseCount <= 1 {
		msg += "\nOne phase: a single library/package/crate/module plus its tests is one delivery, not a multi-phase roadmap."
	} else if len(meta.Phases) > 0 {
		var names []string
		for _, p := range meta.Phases {
			if n := strings.TrimSpace(p.Name); n != "" {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			msg += "\nPhases: " + strings.Join(names, " → ")
		}
	}
	if strings.EqualFold(meta.Status, "draft") {
		msg += "\nPlan is still draft — say \"confirm the plan\" when ready."
	}
	if honesty := summarizeCompileTestHonesty("."); honesty != "" {
		msg += "\n" + honesty
	}
	return msg, true
}

// answerOfflineStatusQuestion answers skills/memory/llm/resume/parallel status
// without LLM. Mirrors answerWorkflowPhaseStatusQuestion (S2/S6.3): product
// must not Clarify these when the router key is missing.
func answerOfflineStatusQuestion(input string) (string, bool) {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if lowered == "" {
		return "", false
	}
	switch {
	case strings.Contains(lowered, "技能") ||
		(strings.Contains(lowered, "skill") && (strings.Contains(lowered, "哪些") || strings.Contains(lowered, "what") || strings.Contains(lowered, "list") || strings.Contains(lowered, "有"))):
		ans, err := summarizeSkillsStatus(".")
		if err != nil {
			return "", false
		}
		return ans, true
	case strings.Contains(lowered, "踩过") || strings.Contains(lowered, "坑") || strings.Contains(lowered, "教训") ||
		strings.Contains(lowered, "pitfall") || strings.Contains(lowered, "lesson"):
		ans, err := summarizeMemoryPitfalls(".")
		if err != nil {
			return "", false
		}
		return ans, true
	case strings.Contains(lowered, "max_tokens") || strings.Contains(lowered, "fallback") || strings.Contains(lowered, "备用") ||
		(strings.Contains(lowered, "shell") && (strings.Contains(lowered, "超时") || strings.Contains(lowered, "timeout"))) ||
		strings.Contains(lowered, "genkit") || strings.Contains(lowered, "tool call") ||
		(strings.Contains(lowered, "主模型") && strings.Contains(lowered, "挂")):
		ans, err := summarizeLLMConfig(".")
		if err != nil {
			return "", false
		}
		return ans, true
	case strings.Contains(lowered, "审批") && (strings.Contains(lowered, "继续") || strings.Contains(lowered, "resume") || strings.Contains(lowered, "critic")):
		return "Approval resume: after you approve a write, the scheduler continues remaining DAG nodes (Critic/Synth) from the checkpoint — it is not emit-only. Use `avatars tasks approve <task-id>` or REPL approval prompts.", true
	case looksLikeParallelProgressStatusQuestion(lowered):
		return summarizeParallelProgressUX(), true
	default:
		return "", false
	}
}

// looksLikeParallelProgressStatusQuestion matches operator questions about
// avatars' own parallel DAG / stderr progress tree — not library briefs that
// mention concurrent Runs (F96). Cross-language: Python/JS/Rust greenfield
// prompts also say 并行 / parallel / concurrent.
func looksLikeParallelProgressStatusQuestion(lowered string) bool {
	lowered = strings.ToLower(strings.TrimSpace(lowered))
	if lowered == "" {
		return false
	}
	if mechanicalLocalAnswersYieldToWork(lowered) {
		return false
	}
	progressAsk := strings.Contains(lowered, "进度树") ||
		(strings.Contains(lowered, "stderr") && strings.Contains(lowered, "进度")) ||
		(strings.Contains(lowered, "progress") && (strings.Contains(lowered, "tree") || strings.Contains(lowered, "stderr"))) ||
		containsAnyIntentToken(lowered, "怎么看", "how do i see", "how to see", "how does", "how do")
	parallelAsk := strings.Contains(lowered, "并行") ||
		strings.Contains(lowered, "parallel") ||
		strings.Contains(lowered, "concurrent")
	if strings.Contains(lowered, "进度树") {
		return true
	}
	if strings.Contains(lowered, "stderr") && strings.Contains(lowered, "进度") {
		return true
	}
	return parallelAsk && progressAsk
}

func formatUserClarifyQuestion(input string, decision naturalLanguageDecision) string {
	if q := strings.TrimSpace(decision.Question); q != "" {
		return appendClarifyOptions(q, decision.Options)
	}
	// Prefer a short user-facing question; keep verbose Reason in Summary only.
	reason := strings.TrimSpace(decision.Reason)
	if reason != "" && !strings.Contains(reason, "\n\n") && len([]rune(reason)) <= 160 &&
		!strings.HasPrefix(reason, "llm router:") &&
		!strings.HasPrefix(reason, "LLM chose") &&
		!strings.HasPrefix(reason, "safety audit:") {
		return appendClarifyOptions(reason, decision.Options)
	}
	base := "I need a bit more detail. What should I do next — analyze read-only, edit a file, or run a full task?"
	opts := decision.Options
	if len(opts) == 0 {
		opts = defaultClarifyOptions(input)
	}
	return appendClarifyOptions(base, opts)
}

func formatGuardedClarifyQuestion(input string, decision naturalLanguageDecision) string {
	if q := strings.TrimSpace(decision.Question); q != "" {
		return appendClarifyOptions(q, decision.Options)
	}
	reason := strings.TrimSpace(decision.Reason)
	reason = strings.TrimPrefix(reason, "safety audit: ")
	reason = strings.TrimPrefix(reason, "llm router: ")
	opts := decision.Options
	if len(opts) == 0 {
		opts = defaultGuardedOptions(input)
	}
	msg := "This looks risky, so I will not run it automatically."
	if reason != "" && len([]rune(reason)) <= 120 {
		msg += " Reason: " + reason + "."
	}
	msg += " If you still want it, use an explicit command."
	return appendClarifyOptions(msg, opts)
}

func unresolvedAnswerClarifyQuestion(input string, answerID string) string {
	return appendClarifyOptions(
		"I understood this as a question, but could not resolve a local answer. Rephrase, or run an explicit command.",
		[]string{
			`avatars run --new-task --permission-mode plan "` + strings.TrimSpace(input) + `"`,
			"ask a simpler status/capability question",
		},
	)
}

func invalidCommandClarifyQuestion(input string, errMsg string) string {
	return appendClarifyOptions(
		"I could not map that to a valid avatars command ("+errMsg+"). Try one of these:",
		defaultClarifyOptions(input),
	)
}

func fallbackClarifyQuestion(input string, fb fallbackResult) string {
	if fb.AutoRun {
		return "I could not route that automatically. You can run it as a task, or rephrase as a clearer question."
	}
	return "I could not route that automatically. Tell me the target file/action, or use an explicit command."
}

func fallbackClarifyOptions(input string, fb fallbackResult) []string {
	trimmed := strings.TrimSpace(input)
	opts := []string{}
	if fb.AutoRun {
		opts = append(opts, fmt.Sprintf(`avatars run --new-task %q`, trimmed))
	}
	opts = append(opts, defaultClarifyOptions(input)...)
	return uniqueNonEmptyStrings(opts)
}

func defaultClarifyOptions(input string) []string {
	trimmed := strings.TrimSpace(input)
	return []string{
		fmt.Sprintf(`avatars run --new-task --permission-mode plan %q`, trimmed),
		fmt.Sprintf(`avatars run --new-task --permission-mode acceptEdits %q`, trimmed),
		`avatars route "` + trimmed + `"`,
	}
}

func defaultGuardedOptions(input string) []string {
	trimmed := strings.TrimSpace(input)
	return []string{
		fmt.Sprintf(`avatars run --new-task --permission-mode plan %q`, trimmed),
		"cancel / rephrase without destructive verbs",
		`avatars route "` + trimmed + `"`,
	}
}

func appendClarifyOptions(question string, options []string) string {
	q := strings.TrimSpace(question)
	opts := uniqueNonEmptyStrings(options)
	if len(opts) == 0 {
		return q
	}
	var b strings.Builder
	b.WriteString(q)
	b.WriteString("\n")
	for i, opt := range opts {
		if i >= 3 {
			break
		}
		b.WriteString(fmt.Sprintf("\n%d. %s", i+1, opt))
	}
	return b.String()
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// restoreOriginalRunTaskText puts the operator's original NL back on a run
// command (F101). The LLM router often paraphrases the last arg; that compressed
// text must not become task.json title or user_requirement.md.
func restoreOriginalRunTaskText(command []string, original string) []string {
	original = strings.TrimSpace(original)
	if original == "" || len(command) == 0 || command[0] != "run" {
		return command
	}
	out := append([]string(nil), command...)
	if runCommandHasFromFile(out) {
		// --from-file already supplies the task body. Appending the original
		// NL makes parseRunCommandOptions see both a file and leftover input.
		return out
	}
	idx := lastRunTaskArgIndex(out)
	if idx < 0 {
		return append(out, original)
	}
	if out[idx] != original {
		out[idx] = original
	}
	return out
}

func runCommandHasFromFile(command []string) bool {
	for i := 1; i < len(command); i++ {
		if command[i] == "--from-file" {
			return true
		}
	}
	return false
}

func lastRunTaskArgIndex(command []string) int {
	flagsTakingValue := map[string]bool{
		"--permission-mode": true,
		"--task":            true,
		"--resume":          true,
		"--from-file":       true,
		"--progress":        true,
		"--max-budget":      true,
		"--model":           true,
	}
	last := -1
	for i := 1; i < len(command); i++ {
		arg := command[i]
		if strings.HasPrefix(arg, "-") {
			if flagsTakingValue[arg] && i+1 < len(command) {
				i++
			}
			continue
		}
		last = i
	}
	return last
}

func isEmptySafeRunCommand(command []string) bool {
	if len(command) == 0 {
		return true
	}
	// Placeholder produced by llmRouterDecisionToNaturalLanguage when command was empty.
	if len(command) >= 4 && command[0] == "run" && command[1] == "--new-task" && strings.TrimSpace(command[len(command)-1]) == "" {
		return true
	}
	if len(command) == 2 && command[0] == "run" && command[1] == "--new-task" {
		return true
	}
	return false
}

// safeRunLacksEditTarget is true when the chosen command is a generic run
// (or edit without a real path) — used to block vague "改一下" from executing.
func safeRunLacksEditTarget(command []string) bool {
	if len(command) == 0 {
		return true
	}
	switch command[0] {
	case "run":
		return true
	case "edit", "edit-many":
		joined := strings.Join(command, " ")
		if !strings.Contains(joined, ".") && !strings.Contains(joined, "/") && !strings.Contains(joined, `\`) {
			return true
		}
	}
	return false
}

// looksLikePhaseGuidedImplement is true when the user wants to implement code
// guided by a phase/plan markdown doc — not edit that doc itself (NL smoke #6).
func looksLikePhaseGuidedImplement(lowered string) bool {
	hasPhaseDoc := (strings.Contains(lowered, "phase") && strings.Contains(lowered, ".md")) ||
		strings.Contains(lowered, "avatars_plan.md") ||
		strings.Contains(lowered, "phase1.md") ||
		strings.Contains(lowered, "phase2.md") ||
		strings.Contains(lowered, "phase3.md") ||
		(strings.Contains(lowered, "阶段") && strings.Contains(lowered, ".md"))
	if !hasPhaseDoc {
		return false
	}
	hasGuideOrImpl := strings.Contains(lowered, "按") ||
		strings.Contains(lowered, "根据") ||
		strings.Contains(lowered, "依据") ||
		strings.Contains(lowered, "照着") ||
		strings.Contains(lowered, "according to") ||
		strings.Contains(lowered, "follow") ||
		strings.Contains(lowered, "实现") ||
		strings.Contains(lowered, "写代码") ||
		strings.Contains(lowered, "开始") ||
		strings.Contains(lowered, "implement") ||
		strings.Contains(lowered, "推进") ||
		strings.Contains(lowered, "继续")
	return hasGuideOrImpl
}

// filterWorkflowGuideDocs drops plan/phase markdown paths so they are not
// treated as edit targets when the user is asking to implement code.
func filterWorkflowGuideDocs(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		slashed := strings.ToLower(filepath.ToSlash(p))
		base := filepath.Base(slashed)
		if strings.Contains(slashed, "docs/workflow/") ||
			base == "avatars_plan.md" ||
			base == "avatars_todo.md" ||
			(strings.HasPrefix(base, "phase") && strings.HasSuffix(base, ".md")) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isPrimarilyASCII(s string) bool {
	nonASCII := 0
	for _, r := range s {
		if r > 127 {
			nonASCII++
		}
	}
	return nonASCII == 0 || float64(nonASCII)/float64(len([]rune(s))) < 0.3
}

func looksLikeConversationalRepairQuestion(lowered string) bool {
	// Soft style words like 「人话」often appear inside real work prompts
	// ("用人话写交付说明") and inside status asks. Never let mechanical
	// repair beat coding intents or phase/progress questions.
	workYield := mechanicalLocalAnswersYieldToWork(lowered)
	if looksLikeWorkflowPhaseStatusAsk(lowered) || looksLikeNoMutateFollowUp(lowered) ||
		looksLikeDesignChoiceAsk(lowered) || looksLikeConceptCompareAsk(lowered) ||
		looksLikeExportedAPIAsk(lowered) || looksLikeRetractedAside(lowered) ||
		looksLikeChecklistContradictionAsk(lowered) || looksLikeDeliveryHonestyAsk(lowered) {
		return false
	}

	directAnswerComplaint := (strings.Contains(lowered, "直接回答") ||
		strings.Contains(lowered, "正面回答") ||
		strings.Contains(lowered, "回答问题") ||
		strings.Contains(lowered, "answer directly") ||
		strings.Contains(lowered, "direct answer")) &&
		(strings.Contains(lowered, "不会") ||
			strings.Contains(lowered, "不能") ||
			strings.Contains(lowered, "为什么不") ||
			strings.Contains(lowered, "why don't you") ||
			strings.Contains(lowered, "why wont you") ||
			strings.Contains(lowered, "why won't you") ||
			strings.Contains(lowered, "won't answer") ||
			strings.Contains(lowered, "can't") ||
			strings.Contains(lowered, "cannot") ||
			strings.Contains(lowered, "why"))
	if directAnswerComplaint && !workYield {
		return true
	}
	// Strong meta-complaints about the assistant (OK even mid-thread).
	strong := []string{
		"听不懂", "没听懂", "没明白", "不会说话", "不会聊天",
		"你又审一遍代码", "又审一遍", "又读一遍", "不是这个意思",
		"我是在问", "你到底有没有", "别再读一遍", "别重复",
		"why are you rereading", "why did you reread", "you reread",
	}
	for _, token := range strong {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	// Soft style asks ("说人话" / bare "人话") only when the message is mainly
	// that complaint — not a multi-requirement library build brief.
	if workYield {
		return false
	}
	for _, token := range []string{"说人话", "人话"} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

// mechanicalLocalAnswersYieldToWork is true when local mechanical Q&A
// (phase status, "说人话" repair, etc.) must defer to coding/create routing.
// Cross-language: same steal happens for Python/JS/Rust greenfield briefs.
func mechanicalLocalAnswersYieldToWork(lowered string) bool {
	if looksLikeContinuationWorkRequest(lowered) {
		return true
	}
	if looksLikeResumeContinuationIntent(lowered) {
		return true
	}
	if looksLikeChecklistProgressReconcile(lowered) {
		return true
	}
	if looksLikeGreenfieldCreateRequest(lowered) {
		return true
	}
	runes := len([]rune(lowered))
	if runes < 36 {
		return false
	}
	if !looksLikeMutatingCodingRequest(lowered) {
		return false
	}
	return containsAnyIntentToken(lowered,
		"开源库", "库", "library", "crate", "package", "模块",
		"golang", " go ", ".go", "python", ".py", "javascript", ".js",
		"typescript", ".ts", "rust", "crate",
		"单元测试", "unit test", "unit tests", "写齐",
		"熔断", "circuit", "breaker", "限流", "缓存", "cache",
		"retry", "重试", "jitter", "api", "状态机", "并发",
		"不要 cli", "不要cli",
	)
}

func summarizeConversationalRepair() string {
	return "Repair note:\n- You're right — the last reply was too mechanical.\n- Chat, capability questions, and last-action questions should be answered directly.\n- Follow-up opinion or status questions should reuse task memory first.\n- Fresh survey only when you ask for it explicitly or memory is stale."
}

func looksLikeDeterministicFollowUpQuestion(lowered string) bool {
	return looksLikeResultLocationQuestion(lowered) ||
		looksLikeConversationalRepairQuestion(lowered) ||
		looksLikeAnalysisReportQualityQuestion(lowered)
}

func looksLikeActiveFileSummaryQuestion(lowered string) bool {
	for _, token := range []string{
		"这个文件说了啥",
		"这个文件讲了啥",
		"这个文件是什么",
		"这个文件是干啥的",
		"这个文件是做什么的",
		"这个文件有什么用",
		"这个文件概况",
		"概况项目",
		"概况一下项目",
		"这个文档是干啥的",
		"这个文档是做什么的",
		"这份文件是干啥的",
		"这份文件是做什么的",
		"刚才的文件",
		"刚刚的文件",
		"它是干啥的",
		"它是做什么的",
		"它有什么用",
		"summarize this file",
		"what does this file say",
		"what is this file about",
		"this file",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func looksLikeAttachedFileQuestion(lowered string) bool {
	if looksLikeContinuationWorkRequest(lowered) {
		return false
	}
	if looksLikeActiveFileSummaryQuestion(lowered) || strings.Contains(lowered, "查看这个文件") || strings.Contains(lowered, "阅读这个文件") || strings.Contains(lowered, "分析这个文件") {
		return true
	}
	if getReplFileContext() == nil {
		return false
	}
	// When a file is already attached, the user's question is implicitly about it.
	// Check if it's a question/analysis request without explicit work request tokens.
	fileReferenced := containsAnyIntentToken(lowered, "文件", "文档", "file", "document")
	contextReferenced := containsAnyIntentToken(lowered, "这个", "这份", "刚才", "刚刚", "刚才的", "刚刚的", "它", "attached", "上面")
	asksForContent := containsAnyIntentToken(lowered, "记录", "内容", "写了", "讲了", "说了", "分析", "总结", "概况", "解释", "写出来", "直接写")
	if fileReferenced && (contextReferenced || asksForContent) {
		return true
	}
	// BUG-3.1/4.2: When file is attached, treat analysis/question verbs as file questions
	// even without explicit "文件" keyword. Also recognize "read" as a file question.
	// Exclude work request tokens to avoid misrouting creation requests like "写一个 .html".
	asksForAnalysis := containsAnyIntentToken(lowered, "分析", "功能", "是什么", "干啥的", "做什么的", "有什么用", "说了啥", "讲了啥", "写了啥", "内容", "概况", "总结", "解释", "介绍", "描述", "说明", "read", "阅读", "查看")
	hasWorkRequest := containsAnyIntentToken(lowered, "写", "创建", "生成", "修改", "实现", "开发", "构建", "编码", "新增", "添加", "删除", "移除", "重构", "优化", "调试", "修复", "解决", "处理", "执行", "运行", "测试", "部署", "发布")
	if asksForAnalysis && !hasWorkRequest {
		return true
	}
	return false
}

func attachedFileDefaultInput(content string) string {
	trimmed := strings.TrimSpace(content)
	lowered := strings.ToLower(trimmed)
	// Continuation / resume / checklist-align / task-spec bodies are the user
	// intent. Do not rewrite them to "summarize this attached file".
	if looksLikeContinuationWorkRequest(lowered) || looksLikeResumeContinuationIntent(lowered) ||
		looksLikeChecklistProgressReconcile(lowered) {
		return trimmed
	}
	if looksLikeTaskSpecDocument(content) {
		return trimmed
	}
	if looksLikeVisualSketchSpec(content) {
		return trimmed
	}
	return "summarize this attached file"
}

func attachedFileTaskSpecForceLLM() bool {
	return getReplFileContext() != nil && looksLikeTaskSpecDocument(getReplFileContext().Content)
}

func looksLikeTaskSpecDocument(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	lowered := strings.ToLower(trimmed)
	planSignal := containsAnyIntentToken(lowered,
		"coding plan",
		"todo",
		"task",
		"requirements",
		"requirement",
		"plan",
		"需求",
		"任务",
		"计划",
	)
	// Cross-language: do not require the word "代码/python". A NL library
	// request naming Go/Rust/JS/Python/Java/C# + 做一个/crate/library is still a spec.
	codeSignal := containsAnyIntentToken(lowered,
		"python",
		"golang",
		"javascript",
		"typescript",
		"rust",
		"kotlin",
		"java",
		"csharp",
		"c#",
		"脚本",
		"代码",
		"implement",
		"write",
		"generate",
		"build",
		"create",
		"编写",
		"写一个",
		"实现",
		"生成",
		"修改",
		"做一个",
		"从零",
		"开源库",
		"纯库",
		"library",
		"crate",
		"单元测试",
		"unit test",
		"pytest",
		"jest",
		"html",
		"webpage",
		"web page",
		"网页",
	)
	listSignal := looksLikeNumberedOrBulletList(trimmed)
	imperativeSignal := containsAnyIntentToken(lowered,
		"write a",
		"write an",
		"create a",
		"generate a",
		"build a",
		"implement a",
		"用 python 写",
		"用python写",
		"写一个",
		"写一份",
		"编写一个",
		"生成一个",
		"实现一个",
		"做一个",
		"从零做",
		"请直接动手",
		"动手做",
		"动手实现",
		"修改",
	)
	namedArtifactSignal := regexp.MustCompile(`(?i)\b[\w.-]+\.(?:py|js|mjs|cjs|ps1|sh|go|rs|ts|tsx|jsx|java|rb|kt|cs|php|md|html|htm|css)\b`).FindString(trimmed) != ""
	workRequestSignal := containsAnyIntentToken(lowered,
		"做一个",
		"写一个",
		"从零",
		"动手",
		"实现",
		"编写",
		"创建",
		"create",
		"implement",
		"build",
		"write",
		"generate",
		"library",
		"crate",
		"库",
		"模块",
		"网页",
		"webpage",
		"web page",
		"html",
		"slides",
		"单元测试",
		"unit test",
		"pytest",
		"jest",
		"go test",
		"cargo test",
		"npm test",
		"纯库",
	)
	classic := codeSignal && (listSignal || (planSignal && imperativeSignal) || (imperativeSignal && namedArtifactSignal))
	// r25 slugifyx: 需求 + bullet list + 做一个/库, without the word "代码".
	requirementsList := planSignal && listSignal && workRequestSignal
	return classic || requirementsList || looksLikeVisualSketchSpec(trimmed)
}

func analyzeNaturalLanguageWithLLM(input string, options replCommandOptions, nlContext naturalLanguageContext) (naturalLanguageDecision, bool) {
	if nlContext != naturalLanguageContextIntent && nlContext != naturalLanguageContextREPL {
		return naturalLanguageDecision{}, false
	}
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		return naturalLanguageDecision{}, false
	}
	if cleanup != nil {
		defer cleanup()
	}
	return classifyNaturalLanguageWithLLMClient(input, options, nlContext, client)
}

// naturalLanguageLLMAnalyzer is a mutable package-level var so tests
// can inject a mock LLM analyzer without modifying the classification
// pipeline. Production code must never reassign it after init.
var naturalLanguageLLMAnalyzer = analyzeNaturalLanguageWithLLM

func classifyNaturalLanguageWithLLMClient(input string, options replCommandOptions, nlContext naturalLanguageContext, client llm.Client) (naturalLanguageDecision, bool) {
	if client == nil || (nlContext != naturalLanguageContextIntent && nlContext != naturalLanguageContextREPL) {
		return naturalLanguageDecision{}, false
	}
	llmCtx, llmCancel := llmCallContext()
	defer llmCancel()
	response, err := client.Generate(llmCtx, llm.Request{
		SystemPrompt:     naturalLanguageClassifierSystemPrompt(),
		UserPrompt:       buildNaturalLanguageClassifierPrompt(input, options, nlContext),
		StructuredOutput: true,
		JSONSchema:       llm.ClassifierJSONSchema,
	})
	if err != nil || response.Fallback {
		return naturalLanguageDecision{}, false
	}
	decision, ok := parseNaturalLanguageLLMDecision(response.Text)
	if !ok {
		// LLM returned text that doesn't match the expected JSON schema.
		// Log the raw output so format drift can be diagnosed without
		// losing the routing decision (falls through to deterministic).
		fmt.Fprintf(os.Stderr, "avatars: LLM classifier returned unparseable output (%d chars), falling back to deterministic routing\n", len(response.Text))
		return naturalLanguageDecision{}, false
	}
	confidence := decision.Confidence
	if confidence <= 0 {
		confidence = 60
	}
	switch decision.Action {
	case "direct_answer":
		if answer, ok, err := answerClassifiedLocalQuestion(strings.TrimSpace(input), decision.AnswerID, options, nlContext); err == nil && ok {
			return naturalLanguageDecision{Kind: naturalLanguageDecisionAnswer, Reason: "llm classified as bounded answer", Answer: answer, Confidence: confidence}, true
		}
		return naturalLanguageDecision{}, false
	case "memory_answer":
		if answer, ok, err := answerClassifiedMemoryQuestion(strings.TrimSpace(input), decision.AnswerID, options, nlContext); err == nil && ok {
			return naturalLanguageDecision{Kind: naturalLanguageDecisionMemory, Reason: "llm classified as bounded memory answer", Answer: answer, Confidence: confidence}, true
		}
		return naturalLanguageDecision{}, false
	case "safe_run":
		command, summary := buildNaturalLanguageTaskCommandWithLLM(input, options, true)
		if len(command) == 0 {
			return naturalLanguageDecision{}, false
		}
		return naturalLanguageDecision{Kind: naturalLanguageDecisionSafeRun, Reason: "llm classified as safe task: " + summary, Command: command, Confidence: confidence}, true
	case "guarded":
		return naturalLanguageDecision{Kind: naturalLanguageDecisionGuarded, Reason: "llm classified as guarded", Confidence: confidence}, true
	case "clarify":
		reason := strings.TrimSpace(decision.Question)
		if reason == "" {
			reason = "llm requested clarification"
		}
		return naturalLanguageDecision{Kind: naturalLanguageDecisionClarify, Reason: reason, Confidence: confidence}, true
	default:
		return naturalLanguageDecision{}, false
	}
}

func naturalLanguageClassifierSystemPrompt() string {
	return `Classify one user input for a bounded coding-agent CLI. Return compact JSON only: {"action":"direct_answer|memory_answer|safe_run|clarify|guarded","answer_id":"","question":"","confidence":number}.
Operating contract:
- Interpret natural language first; do not route by keyword guess alone.
- If the user asks a normal chat/status/capability/project question, choose direct_answer when local context can answer it.
- If task memory or the latest result can answer, choose memory_answer before scanning or running again.
- Choose safe_run only for explicit bounded project work that needs the avatars workflow.
- Choose clarify when the goal, target path, or requested action is missing; ask the smallest concrete question.
- Choose guarded for destructive, risky, credential, delete, archive, rollback-apply, or mutating requests needing explicit confirmation.
- User-facing responses should answer first, then give one short reason or next step.
Answer IDs for direct_answer or memory_answer: greeting, capabilities, last_action, active_file_summary, project_overview, model_identity, project_opinion, project_manifest, docs_overview, recent_tasks, result_location, conversational_repair, analysis_report_quality, file_count, file_list, todo_fixme_search. Respond with answer_id=capabilities for capability questions, not safe_run.`
}

func buildNaturalLanguageClassifierPrompt(input string, options replCommandOptions, nlContext naturalLanguageContext) string {
	var builder strings.Builder
	builder.WriteString("User input:\n")
	builder.WriteString(strings.TrimSpace(input))
	if contextBlock := buildNaturalLanguageClassifierContext(options, nlContext); contextBlock != "" {
		builder.WriteString("\n\nAvailable local context:\n")
		builder.WriteString(contextBlock)
	}
	builder.WriteString("\n\nAllowed actions:\n")
	builder.WriteString("- direct_answer: answer from local project context without launching a task.\n")
	builder.WriteString("- memory_answer: answer from task memory without refreshing repository evidence.\n")
	builder.WriteString("- safe_run: run through avatars run; read-only work uses --permission-mode plan, explicit write/code changes use --permission-mode acceptEdits.\n")
	builder.WriteString("- clarify: ask for a clearer project/task request.\n")
	builder.WriteString("- guarded: refuse auto-routing because the request is risky or mutating.\n")
	builder.WriteString("\nDecision rules:\n")
	builder.WriteString("- Answer simple questions directly; do not start a repository survey for chat, capabilities, or status.\n")
	builder.WriteString("- Reuse memory/results before refreshing repository evidence.\n")
	builder.WriteString("- Ask a short clarification question instead of guessing when target/action is unclear.\n")
	builder.WriteString("- Only choose safe_run when the user clearly asks avatars to do project work.\n")
	builder.WriteString("- active_file_summary: only when user asks what the file IS or CONTAINS. If they want to MODIFY, EDIT, CHANGE, CONVERT, or REWRITE the file, use safe_run instead — never active_file_summary.\n")
	builder.WriteString("- If a latest task/result is available and the user asks where the result is or what happened, choose memory_answer or direct_answer with result_location/last_action.\n")
	builder.WriteString("\nAnswer IDs:\n")
	builder.WriteString("- greeting, capabilities, last_action, active_file_summary, project_overview, model_identity, project_opinion, project_manifest, docs_overview, recent_tasks, result_location, conversational_repair, analysis_report_quality, file_count, file_list, todo_fixme_search.\n")
	// P0-8: Inject Intent_Routing_Skill routing rules so the LLM knows
	// which intents map to script/edit/bootstrap/plan paths with keyword mappings.
	builder.WriteString("\n\nIntent routing rules (from Intent_Routing_Skill):\n")
	builder.WriteString("- Script path: for standalone program/game/tool creation. Triggers: write a script, create a game, build a.\n")
	builder.WriteString("- Edit path: for modifying existing files. Triggers: modify, update, fix, change.\n")
	builder.WriteString("- Bootstrap path: for scaffolding new multi-file projects.\n")
	builder.WriteString("- Plan mode: only for genuinely multi-step/ambiguous requests. Do NOT route simple creation here.\n")
	builder.WriteString("- Anti-patterns: 'write a game/script' → script path, NOT plan mode. Complex requests that only look simple still route via intent, not keyword guess.\n")
	builder.WriteString("- Clarify: when intent is genuinely ambiguous (e.g., 'do something with this project' without specifics), return action=clarify.\n")
	builder.WriteString("\nReturn JSON only.")
	return builder.String()
}

func buildNaturalLanguageClassifierContext(options replCommandOptions, nlContext naturalLanguageContext) string {
	lines := []string{}
	if getReplFileContext() != nil && strings.TrimSpace(getReplFileContext().Path) != "" {
		lines = append(lines, "- active_file: "+filepath.ToSlash(getReplFileContext().Path))
		if title := markdownTitle(getReplFileContext().Content); title != "" {
			lines = append(lines, "- active_file_title: "+title)
		}
		if firstLine := firstMeaningfulMarkdownLine(getReplFileContext().Content); firstLine != "" {
			lines = append(lines, "- active_file_summary: "+firstLine)
		}
		// Inject full file content so the LLM classifier can see intent signals
		// anywhere in the file, not just in the title/summary/first-line.
		// This fixes the bug where @file content was invisible to intent routing.
		// Use the same cap as always-on skill bodies (4000 chars) as a safety valve
		// for extremely large files, but preserve newlines for readability.
		if trimmedContent := strings.TrimRight(getReplFileContext().Content, "\n"); trimmedContent != "" {
			if len(trimmedContent) > 4000 {
				lines = append(lines, "- active_file_content:")
				lines = append(lines, trimmedContent[:4000])
				lines = append(lines, "[truncated: file content exceeded 4000 chars]")
			} else {
				lines = append(lines, "- active_file_content:")
				lines = append(lines, trimmedContent)
			}
		}
	}
	if nlContext == naturalLanguageContextREPL {
		taskID := strings.TrimSpace(options.TaskID)
		if taskID == "" {
			taskID = defaultREPLTaskID
		}
		if taskID != "" {
			lines = append(lines, "- repl_task: "+taskID)
			if workspace, ok, err := latestTaskWorkspace("."); err == nil && ok && strings.TrimSpace(workspace.LatestSummary) != "" {
				lines = append(lines, "- latest_task_summary: "+conciseNLContextLine(workspace.LatestSummary, 260))
			}
		}
	}
	if workspace, ok, err := latestReportedTaskWorkspace("."); err == nil && ok {
		if reportPath := latestAnalysisReportPath(workspace); reportPath != "" {
			lines = append(lines, "- latest_report: "+filepath.ToSlash(reportPath))
		}
		if transcript := strings.TrimSpace(workspace.LatestTranscriptPath()); transcript != "" {
			lines = append(lines, "- latest_transcript: "+filepath.ToSlash(transcript))
		}
	}
	return strings.Join(lines, "\n")
}

func conciseNLContextLine(value string, limit int) string {
	trimmed := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit <= 0 {
		return trimmed
	}
	r := []rune(trimmed)
	if len(r) <= limit {
		return trimmed
	}
	return strings.TrimSpace(string(r[:limit])) + "..."
}

func compactAnswerPreviewMarkdown(value string, limit int) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"))
	if trimmed == "" {
		return ""
	}
	const lineLimit = 40
	lines := strings.Split(trimmed, "\n")
	previewLines := make([]string, 0, len(lines))
	runeCount := 0
	previousBlank := false
	truncated := false
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, " \t")
		blank := strings.TrimSpace(line) == ""
		if blank {
			if len(previewLines) == 0 || previousBlank {
				continue
			}
			line = ""
		}
		previousBlank = blank
		if len(previewLines) >= lineLimit {
			truncated = true
			break
		}
		addedRunes := len([]rune(line))
		if len(previewLines) > 0 {
			addedRunes++
		}
		if limit > 0 && runeCount+addedRunes > limit {
			remaining := limit - runeCount
			if len(previewLines) > 0 {
				remaining--
			}
			if remaining > 0 {
				runes := []rune(line)
				if remaining < len(runes) {
					line = strings.TrimRight(string(runes[:remaining]), " \t")
				}
				if line != "" || blank {
					previewLines = append(previewLines, line)
				}
			}
			truncated = true
			break
		}
		previewLines = append(previewLines, line)
		runeCount += addedRunes
	}
	preview := strings.TrimSpace(strings.Join(previewLines, "\n"))
	if truncated {
		if preview != "" {
			preview += "\n\n"
		}
		preview += "[preview truncated; see Full answer below]"
	}
	return preview
}

// robustExtractJSON attempts to extract a JSON object from an LLM
// response that may contain markdown fences, extra text, or minor
// syntax issues (trailing commas). Tries multiple strategies before
// giving up.
func robustExtractJSON(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	// Strategy 1: Strip markdown ```json fences.
	if strings.HasPrefix(trimmed, "```") {
		if idx := strings.Index(trimmed, "\n"); idx >= 0 {
			trimmed = trimmed[idx+1:]
		}
		if last := strings.LastIndex(trimmed, "```"); last >= 0 {
			trimmed = trimmed[:last]
		}
		trimmed = strings.TrimSpace(trimmed)
	}
	// Strategy 2: Find { } block.
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		return trimmed[start : end+1], true
	}
	return "", false
}

// cleanJSONString removes common LLM JSON syntax issues.
func cleanJSONString(raw string) string {
	// Remove trailing commas before closing braces/brackets.
	re := regexp.MustCompile(`,(\s*[}\]])`)
	cleaned := re.ReplaceAllString(raw, "$1")
	return cleaned
}

func parseNaturalLanguageLLMDecision(text string) (naturalLanguageLLMDecision, bool) {
	raw, ok := robustExtractJSON(text)
	if !ok {
		return naturalLanguageLLMDecision{}, false
	}
	raw = cleanJSONString(raw)
	var decision naturalLanguageLLMDecision
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return naturalLanguageLLMDecision{}, false
	}
	decision.Action = strings.ToLower(strings.TrimSpace(decision.Action))
	decision.AnswerID = strings.ToLower(strings.TrimSpace(decision.AnswerID))
	switch decision.Action {
	case "direct_answer", "memory_answer", "safe_run", "clarify", "guarded":
		return decision, true
	default:
		return naturalLanguageLLMDecision{}, false
	}
}

func answerClassifiedLocalQuestion(input string, answerID string, options replCommandOptions, context naturalLanguageContext) (string, bool, error) {
	id := strings.ToLower(strings.TrimSpace(answerID))
	switch id {
	case "greeting":
		return summarizeGreeting(), true, nil
	case "capabilities":
		return summarizeCapabilities(), true, nil
	case "last_action":
		if looksLikeContinuationWorkRequest(strings.ToLower(strings.TrimSpace(input))) {
			return "", false, nil
		}
		answer, err := summarizeLastAction(".")
		return answer, true, err
	case "active_file_summary":
		if looksLikeContinuationWorkRequest(strings.ToLower(strings.TrimSpace(input))) {
			return "", false, nil
		}
		if getReplFileContext() == nil {
			return "File context:\n- no active file. Use @path or --from-file first.", true, nil
		}
		// When the question contains execution or modification
		// verbs, the user wants to act on the file, not just
		// view it. Fall through to work-request routing.
		lowered := strings.ToLower(strings.TrimSpace(input))
		if attachedFileTriggersWorkRequest(lowered) ||
			strings.Contains(lowered, "修改") || strings.Contains(lowered, "改") ||
			strings.Contains(lowered, "转换") || strings.Contains(lowered, "变成") ||
			strings.Contains(lowered, "更新") || strings.Contains(lowered, "调整") ||
			strings.Contains(lowered, "modify") || strings.Contains(lowered, "edit") ||
			strings.Contains(lowered, "change") || strings.Contains(lowered, "convert") ||
			strings.Contains(lowered, "update") || strings.Contains(lowered, "rewrite") {
			return "", false, nil
		}
		return summarizeAttachedFile(getReplFileContext().Path, getReplFileContext().Content), true, nil
	case "project_overview":
		answer, err := summarizeProjectOverview(".")
		return answer, true, err
	case "model_identity":
		answer, err := summarizeModelIdentity(".")
		return answer, true, err
	case "project_opinion":
		if answer, ok, err := answerTaskMemoryQuestionForClassifiedIntent(input, options, context); err == nil && ok {
			return answer, true, nil
		}
		answer, err := summarizeProjectConversationalEvaluation(".")
		return answer, true, err
	case "project_manifest":
		answer, err := summarizeProjectManifests(".")
		return answer, true, err
	case "docs_overview":
		answer, err := summarizeDocsOverview(".")
		return answer, true, err
	case "recent_tasks":
		answer, err := summarizeRecentTasks(".")
		return answer, true, err
	case "result_location":
		answer, err := summarizeResultLocation(".")
		return answer, true, err
	case "conversational_repair":
		return summarizeConversationalRepair(), true, nil
	case "analysis_report_quality":
		answer, err := summarizeLatestAnalysisReportQuality(".")
		return answer, true, err
	case "file_count", "file_list":
		answer, err := summarizeProjectFiles(".")
		return answer, true, err
	case "todo_fixme_search":
		answer, err := searchTodoFixmeInProject(".")
		return answer, true, err
	case "skills_status":
		answer, err := summarizeSkillsStatus(".")
		return answer, true, err
	case "memory_pitfalls":
		answer, err := summarizeMemoryPitfalls(".")
		return answer, true, err
	case "workflow_phase":
		if ans, ok := answerWorkflowPhaseStatusQuestion(input); ok {
			return ans, true, nil
		}
		return "No workflow plan/todo phase status found yet.", true, nil
	case "resume_status":
		return "Approval resume: after you approve a write, the scheduler continues remaining DAG nodes (Critic/Synth) from the checkpoint — it is not emit-only. Use `avatars tasks approve <task-id>` or REPL approval prompts.", true, nil
	case "parallel_status", "progress_ux":
		return summarizeParallelProgressUX(), true, nil
	case "llm_config":
		answer, err := summarizeLLMConfig(".")
		return answer, true, err
	default:
		return "", false, nil
	}
}

// summarizeParallelProgressUX answers parallel / progress-tree questions (S6.5).
func summarizeParallelProgressUX() string {
	return strings.TrimSpace(`Parallel + live progress:
- Independent ready-set nodes run concurrently (capped by max_parallel_avatars).
- One node failure does not abort siblings; dependents of successful siblings can proceed.
- During a run, stderr shows handoff/assigned/spoke lines (reusing hot-event summaries) and prefixes parallel survey nodes as [node-survey-docs|src|cfg] so waves are attributable.
- Compact mode: AVATARS_PROGRESS=0 (or --progress off) with AVATARS_COMPACT_HEARTBEAT=1 keeps a 15s pulse.
- Lively progress (default): human-readable stage/tool/LLM lines on stderr; AVATARS_PROGRESS=json for NDJSON monitoring.
- LLM wait heartbeat every ~25s while drafting so long runs don't look hung.
- Post-run: avatars tasks show <task-id> prints the execution chain.`)
}

func answerClassifiedMemoryQuestion(input string, answerID string, options replCommandOptions, context naturalLanguageContext) (string, bool, error) {
	if answer, ok, err := answerTaskMemoryQuestionForClassifiedIntent(input, options, context); err != nil || ok {
		return answer, ok, err
	}
	return answerClassifiedLocalQuestion(input, answerID, options, context)
}

func summarizeAttachedFile(path string, content string) string {
	trimmedPath := strings.TrimSpace(path)
	trimmedContent := strings.TrimSpace(content)
	title := markdownTitle(trimmedContent)
	if title == "" {
		title = "unknown"
	}
	summary := firstMeaningfulMarkdownLine(trimmedContent)
	if summary == "" {
		summary = "No readable summary line found."
	}
	lines := []string{
		"File context:",
		"- path: " + filepath.ToSlash(trimmedPath),
		"- title: " + title,
		"- summary: " + summary,
	}
	if strings.Contains(strings.ToLower(trimmedContent), "## ") {
		if bullets := markdownBulletsUnderHeading(trimmedContent, "Request", 3); len(bullets) > 0 {
			lines = append(lines, "- request: "+strings.Join(bullets, " | "))
		}
		if bullets := markdownBulletsUnderHeading(trimmedContent, "Project Purpose", 2); len(bullets) > 0 {
			lines = append(lines, "- project purpose: "+strings.Join(bullets, " | "))
		}
	}
	if len(trimmedContent) > 0 {
		lines = append(lines, "- length: "+fmt.Sprintf("%d chars", len(trimmedContent)))
	}
	return strings.Join(lines, "\n")
}

func answerPreviousResultWriteRequest(input string, options replCommandOptions, context naturalLanguageContext) (string, bool, error) {
	if context != naturalLanguageContextREPL && context != naturalLanguageContextIntent {
		return "", false, nil
	}
	if !looksLikeOutputArtifactRequest(strings.ToLower(strings.TrimSpace(input))) {
		return "", false, nil
	}
	if !looksLikeResultWriteFollowUpQuestion(strings.ToLower(strings.TrimSpace(input))) {
		return "", false, nil
	}
	targetPath, ok := requestedMarkdownOutputPath(input)
	if !ok {
		return "Previous result reuse failed:\n- requested output path is missing or not markdown", true, nil
	}
	if looksLikePreviousAnswerWriteFollowUpQuestion(strings.ToLower(strings.TrimSpace(input))) {
		answer, workspace, ok, err := latestRunAnswerContent(".")
		if err != nil {
			return fmt.Sprintf("Previous answer reuse failed:\n- error: %v", err), true, nil
		}
		if !ok {
			return "Previous answer reuse failed:\n- no previous answer content found\n- run a question first, or ask where the result is to inspect latest task state", true, nil
		}
		if err := os.WriteFile(targetPath, []byte(answer), 0o644); err != nil {
			return fmt.Sprintf("Previous answer reuse failed:\n- target: %s\n- error: %v", filepath.ToSlash(targetPath), err), true, nil
		}
		lines := []string{
			"Previous answer reused:",
			"- source: latest answer",
			"- target: " + filepath.ToSlash(targetPath),
		}
		if transcript := strings.TrimSpace(workspace.LatestTranscriptPath()); transcript != "" {
			lines = append(lines, "- transcript: "+filepath.ToSlash(transcript))
		}
		return strings.Join(lines, "\n"), true, nil
	}
	workspace, ok, err := latestReportedTaskWorkspace(".")
	if err != nil {
		return fmt.Sprintf("Previous result reuse failed:\n- error: %v", err), true, nil
	}
	if !ok {
		return "Previous result reuse failed:\n- no previous analysis report found\n- run an analysis first, or ask where the result is to inspect latest task state", true, nil
	}
	reportPath := latestAnalysisReportPath(workspace)
	if reportPath == "" {
		return "Previous result reuse failed:\n- latest task has no analysis report path\n- run an analysis that writes a .md report first, or ask where the result is to inspect latest task state", true, nil
	}
	sourcePath := resolveAnalysisReportReadPath(".", reportPath)
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Sprintf("Previous result reuse failed:\n- source: %s\n- error: %v", filepath.ToSlash(reportPath), err), true, nil
	}
	if err := os.WriteFile(targetPath, content, 0o644); err != nil {
		return fmt.Sprintf("Previous result reuse failed:\n- target: %s\n- error: %v", filepath.ToSlash(targetPath), err), true, nil
	}
	lines := []string{
		"Previous result reused:",
		"- source: " + filepath.ToSlash(reportPath),
		"- target: " + filepath.ToSlash(targetPath),
	}
	if transcript := strings.TrimSpace(workspace.LatestTranscriptPath()); transcript != "" {
		lines = append(lines, "- transcript: "+filepath.ToSlash(transcript))
	}
	if summary := strings.TrimSpace(workspace.LatestSummary); summary != "" {
		lines = append(lines, "- summary: "+conciseNLContextLine(summary, 300))
	}
	return strings.Join(lines, "\n"), true, nil
}

func looksLikeGreetingQuestion(lowered string) bool {
	trimmed := strings.TrimSpace(lowered)
	if looksLikeExplicitProjectWorkRequest(trimmed) {
		return false
	}
	for _, token := range []string{"hi", "hello", "hey", "你好", "您好", "哈喽", "嗨", "早上好", "上午好", "下午好", "晚上好"} {
		if trimmed == token || strings.HasPrefix(trimmed, token+" ") || strings.HasPrefix(trimmed, token+"，") || strings.HasPrefix(trimmed, token+",") || strings.Contains(trimmed, token+"啊") {
			return true
		}
	}
	return false
}

func looksLikeIdentityQuestion(lowered string) bool {
	if looksLikeModelIdentityQuestion(lowered) {
		return false
	}
	for _, token := range []string{
		"你是干啥的",
		"你是干什么的",
		"你干啥的",
		"你干什么的",
		"你是谁",
		"what are you",
		"who are you",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func looksLikeAbilityConfirmationQuestion(lowered string) bool {
	if !containsAnyIntentToken(lowered, "你会", "你能", "can you", "could you") {
		return false
	}
	if containsAnyIntentToken(lowered, "写", "代码", "py", "python", "go", "js", "typescript", "修复", "改代码", "coding", "code") {
		return true
	}
	return false
}

func looksLikeCapabilityQuestion(lowered string) bool {
	if looksLikeIdentityQuestion(lowered) || looksLikeAbilityConfirmationQuestion(lowered) {
		return true
	}
	// BUG-3.3/4.1: When a file is attached and user asks analysis/ability questions,
	// don't match capability question — let active-file-summary handle it.
	// But exclude AI-referencing ability verbs (你能/你会) — those are capability questions.
	if getReplFileContext() != nil {
		// Check if user is asking about the FILE (not the AI)
		asksAboutFile := containsAnyIntentToken(lowered, "文件", "这个文件", "这份文件", "这个", "这份", "它")
		asksAnalysis := containsAnyIntentToken(lowered, "分析", "功能", "是什么", "干啥的", "做什么的", "有什么用", "说了啥", "讲了啥", "写了啥", "内容", "概况", "总结", "解释", "介绍", "描述", "说明", "能干啥", "能干什么", "能做什么", "擅长什么", "擅长干啥", "擅长干什么")
		asksAboutAI := containsAnyIntentToken(lowered, "你能", "你会", "你能做", "你会做", "你能干", "你会干")
		if asksAboutFile && asksAnalysis && !asksAboutAI {
			return false
		}
		if asksAnalysis && !asksAboutAI && !containsAnyIntentToken(lowered, "文件", "这个", "这份", "它") {
			// Ambiguous ability verb without file reference — still exclude if file attached
			// because user likely means "what can this file do"
			return false
		}
	}
	// Do NOT match bare "功能" — it appears in task descriptions like
	// "新建评论功能" and steals work into capability-summary (NL smoke 2026-07-10).
	for _, token := range []string{
		"what can you do",
		"capabilities",
		"能干什么",
		"能做什么",
		"能干啥",
		"擅长什么",
		"擅长干啥",
		"擅长干什么",
		"擅长改代码",
		"擅长写代码",
		"会改代码",
		"你能做啥",
		"你能写代码",
		"会写py",
		"会写 python",
		"会写python",
		"会写代码",
		"有哪些能力",
		"有啥功能",
		"有啥能力",
		"哪些功能",
		"你能干啥",
		"你能干啥呢",
		"那你能干啥",
		"你有什么功能",
		"有什么功能",
		"支持哪些功能",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func looksLikeLastActionQuestion(lowered string) bool {
	trimmed := strings.TrimSpace(lowered)
	// Continuation, resume, and checklist-align work are runs, not a recap.
	// Bare "继续" / "continue" / "继续推进" mean keep going, not "what did you just do".
	if looksLikeContinuationWorkRequest(trimmed) || looksLikeResumeContinuationIntent(trimmed) ||
		looksLikeChecklistProgressReconcile(trimmed) {
		return false
	}
	// Continuation commands — these should trigger Run, not just report.
	for _, token := range []string{
		"继续推进", "接着做", "继续完成", "接着来", "继续执行",
		"继续完善", "接着完善", "继续开发", "继续写",
		"继续做完", "继续把", "请继续",
	} {
		if trimmed == token || strings.Contains(trimmed, token) {
			return false
		}
	}
	for _, token := range []string{
		"刚才干了什么",
		"刚才做了什么",
		"刚才改了什么",
		"刚刚改了什么",
		"上一步改了什么",
		"你干了什么",
		"你做了什么",
		"你生成了什么",
		"生成成功了吗",
		"这个skill文件生成成功了",
		"上一步",
		"上一轮",
		"last action",
		"what did you do",
	} {
		if !strings.Contains(lowered, token) {
			continue
		}
		if (token == "上一轮" || token == "上一步") && lastRoundPhraseIsWork(trimmed) {
			continue
		}
		return true
	}
	return false
}

// lastRoundPhraseIsWork is true when "上一轮/上一步" is continue-the-task
// language, not "what did you do last round".
func lastRoundPhraseIsWork(lowered string) bool {
	for _, q := range []string{
		"做了什么", "干了什么", "改了什么", "生成了什么",
		"what did you", "what happened last",
	} {
		if strings.Contains(lowered, q) {
			return false
		}
	}
	for _, w := range []string{
		"接着", "请接着", "继续", "对齐", "修绿", "修到", "任务做",
		"align", "continue", "fix", "implement", "checklist", "清单",
	} {
		if strings.Contains(lowered, w) {
			return true
		}
	}
	return false
}

// looksLikeChecklistProgressReconcile is true when the user wants workflow
// checklists aligned with disk (and optionally which phase), not a file
// summary or last-action recap. Cross-language: same ask after go test /
// pytest / cargo test / npm test already went green.
func looksLikeChecklistProgressReconcile(lowered string) bool {
	lowered = strings.ToLower(strings.TrimSpace(lowered))
	if lowered == "" {
		return false
	}
	checklistHit := containsAnyIntentToken(lowered,
		"清单", "checklist", "avatars_todo", "空勾", "unchecked",
	)
	alignHit := containsAnyIntentToken(lowered,
		"对齐", "align", "真实进度", "real progress",
		"勾成", "勾清单", "tick the",
		"划阶段", "阶段划分", "which phase", "第几阶段",
	)
	if checklistHit && alignHit {
		return true
	}
	if strings.Contains(lowered, "align") && strings.Contains(lowered, "checklist") {
		return true
	}
	if strings.Contains(lowered, "清单") &&
		(strings.Contains(lowered, "空") || strings.Contains(lowered, "进度")) {
		return true
	}
	return false
}

func summarizeGreeting() string {
	return "Hi. I can answer local project questions, look up task memory, run plan-mode workflows, find result locations, check report quality, and handle resume/rollback. Just say the goal or a file path."
}

func summarizeCapabilities() string {
	return "Capabilities:\n" +
		"1. Direct project questions (file count, last action, where results are)\n" +
		"2. Read-only repo analysis: say \"analyze this project\" or \"find issues\"\n" +
		"3. Edit existing files / write new scripts: name the file and the change\n" +
		"4. Full tasks (multi-step implementation): describe what you want built\n" +
		"5. Risky operations are clarified first, never auto-run\n" +
		"You can also use explicit commands: avatars run / edit / script / skills / verify."
}

func summarizeLastAction(root string) (string, error) {
	if answer, ok, err := summarizeREPLLastTurn(root); err != nil || ok {
		return answer, err
	}
	workspace, ok, err := latestTaskWorkspace(root)
	if err != nil {
		return "", err
	}
	if !ok {
		return "Last action:\n- no task workspace found yet", nil
	}
	lines := []string{"Last action:"}
	if summary := strings.TrimSpace(workspace.LatestSummary); summary != "" {
		lines = append(lines, "- summary: "+summary)
	}
	if reportPath := latestAnalysisReportPath(workspace); reportPath != "" {
		lines = append(lines, "- analysis report: "+filepath.ToSlash(reportPath))
	}
	if transcriptPath := strings.TrimSpace(workspace.LatestTranscriptPath()); transcriptPath != "" {
		lines = append(lines, "- transcript: "+filepath.ToSlash(transcriptPath))
	}
	if len(lines) == 1 {
		lines = append(lines, "- latest task: "+workspace.ID)
	}
	return strings.Join(lines, "\n"), nil
}

func answerLocalQuestion(line string) (string, bool, error) {
	lowered := strings.ToLower(strings.TrimSpace(line))
	handlers := append([]localQuestionHandler(nil), localQuestionHandlers...)
	sort.SliceStable(handlers, func(i, j int) bool {
		if handlers[i].Priority == handlers[j].Priority {
			return handlers[i].Name < handlers[j].Name
		}
		return handlers[i].Priority < handlers[j].Priority
	})
	for _, handler := range handlers {
		if handler.Match != nil && !handler.Match(lowered) {
			continue
		}
		if handler.Answer == nil {
			continue
		}
		answer, err := handler.Answer(".")
		if err != nil {
			return "", true, err
		}
		return answer, true, nil
	}
	return "", false, nil
}

func answerTaskMemoryQuestion(input string, options replCommandOptions, context naturalLanguageContext) (string, bool, error) {
	if context != naturalLanguageContextREPL {
		return "", false, nil
	}
	if !looksLikeTaskMemoryFollowUpQuestion(strings.ToLower(strings.TrimSpace(input))) {
		return "", false, nil
	}
	return answerTaskMemoryQuestionForClassifiedIntent(input, options, context)
}

func answerTaskMemoryQuestionForClassifiedIntent(input string, options replCommandOptions, context naturalLanguageContext) (string, bool, error) {
	if context != naturalLanguageContextREPL {
		return "", false, nil
	}
	taskID := strings.TrimSpace(options.TaskID)
	if taskID == "" {
		taskID = defaultREPLTaskID
	}
	if taskID == "" {
		return "", false, nil
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, err := manager.Load(taskID)
	if err != nil {
		return "", false, nil
	}
	if workspace.ID != taskID {
		return "", false, nil
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		return "", false, nil
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	snapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		return "", false, nil
	}
	if strings.TrimSpace(snapshot.TaskSummary) == "" && len(snapshot.ProjectLessons) == 0 && len(snapshot.WarmLessons) == 0 && len(snapshot.EvaluationRecords) == 0 {
		return "", false, nil
	}
	if answer, ok := summarizeTaskMemoryFollowUp(input, workspace.ID, snapshot); ok {
		return answer, true, nil
	}
	return "", false, nil
}

func summarizeTaskMemoryFollowUp(input string, taskID string, snapshot memstore.Snapshot) (string, bool) {
	lowered := strings.ToLower(strings.TrimSpace(input))
	lines := []string{}
	if taskSummary := strings.TrimSpace(snapshot.TaskSummary); taskSummary != "" {
		lines = append(lines, "Task memory: "+taskSummary)
	}
	if projectLesson := latestProjectLessonSummary(snapshot.ProjectLessons); projectLesson != "" {
		lines = append(lines, "Project lesson: "+projectLesson)
	}
	if warmLesson := latestWarmLessonSummary(snapshot.WarmLessons); warmLesson != "" {
		lines = append(lines, "Recent lesson: "+warmLesson)
	}
	if len(lines) == 0 {
		return "", false
	}
	if looksLikeProjectOpinionQuestion(lowered) {
		if !memoryLinesRelevantToProjectOpinion(lines) {
			return "", false
		}
		return "Memory recall:\n- task=" + taskID + "\n- " + strings.Join(lines, "\n- "), true
	}
	if looksLikeAnalysisReportQualityQuestion(lowered) {
		return "Memory recall:\n- task=" + taskID + "\n- " + strings.Join(lines, "\n- "), true
	}
	if looksLikeProjectStatusQuestion(lowered) {
		return "Memory recall:\n- task=" + taskID + "\n- " + strings.Join(lines, "\n- "), true
	}
	if lowered != "" {
		return "Memory recall:\n- task=" + taskID + "\n- " + strings.Join(lines, "\n- "), true
	}
	return "", false
}

func memoryLinesRelevantToProjectOpinion(lines []string) bool {
	joined := strings.ToLower(strings.Join(lines, "\n"))
	for _, token := range []string{
		"project is",
		"project looks",
		"project lesson",
		"project opinion",
		"promising",
		"viable",
		"needs clearer issue proof",
		"follow-up questions should reuse memory",
		"项目",
		"仓库",
	} {
		if strings.Contains(joined, token) {
			return true
		}
	}
	return false
}

func latestProjectLessonSummary(lessons []memstore.ProjectLesson) string {
	if len(lessons) == 0 {
		return ""
	}
	lesson := lessons[0]
	summary := strings.TrimSpace(lesson.Summary)
	if summary == "" {
		return ""
	}
	if len(summary) > 180 {
		summary = strings.TrimSpace(summary[:180]) + "..."
	}
	return summary
}

func latestWarmLessonSummary(lessons []memstore.WarmLesson) string {
	if len(lessons) == 0 {
		return ""
	}
	lesson := lessons[0]
	summary := strings.TrimSpace(lesson.Summary)
	if summary == "" {
		return ""
	}
	if len(summary) > 180 {
		summary = strings.TrimSpace(summary[:180]) + "..."
	}
	return summary
}

func buildWorkflowPlanActionCommand(input string, options replCommandOptions) []string {
	trimmed := strings.TrimSpace(input)
	mode := "default"
	// Compound confirm+implement needs write permission after ConfirmPlan.
	if workflow.WantsImplementAfterConfirm(trimmed) {
		mode = "acceptEdits"
	}
	if strings.TrimSpace(options.TaskID) != "" && !options.ForceNewTask {
		return []string{"run", "--task", options.TaskID, "--permission-mode", mode, trimmed}
	}
	return []string{"run", "--new-task", "--permission-mode", mode, trimmed}
}

func buildNaturalLanguageTaskCommand(input string, options replCommandOptions) ([]string, string) {
	return buildNaturalLanguageTaskCommandWithLLM(input, options, true)
}

func buildNaturalLanguageTaskCommandWithLLM(input string, options replCommandOptions, allowLLMSpec bool) ([]string, string) {
	trimmed := strings.TrimSpace(input)
	lowered := strings.ToLower(trimmed)
	if patchSpec, ok := extractExplicitPatchSpec(trimmed); ok {
		return []string{"patch", "--permission-mode", "acceptEdits", patchSpec.Path, patchSpec.Old, patchSpec.New}, "apply an explicit string replacement"
	}
	if looksLikeImplementationWorkIntent(lowered) {
		// NL smoke #6: "按 phase1.md 实现" is guided implementation, not edit of the md.
		if !looksLikePhaseGuidedImplement(lowered) {
			if paths, err := editablePathsFromInstruction(trimmed); err == nil {
				paths = filterWorkflowGuideDocs(paths)
				if len(paths) >= 2 {
					return []string{"edit-many", "--apply", trimmed}, "edit multiple existing files from explicit implementation request"
				}
				if len(paths) == 1 {
					return []string{"edit", "--apply", paths[0], trimmed}, "edit one existing file from explicit implementation request"
				}
			}
		}
	}
	if allowLLMSpec {
		spec, ok := editIntentAnalyzer(trimmed)
		if ok && spec.Action == "edit" {
			if command := editCommandFromLLMSpec(trimmed, spec); len(command) > 0 {
				return command, "edit one existing file from LLM intent spec"
			}
			// LLM classified as "edit" but could not produce a valid command.
			// Return nil so the caller falls back to a clarify decision.
			return nil, "edit intent detected but LLM could not determine the specific edit target; please clarify"
		}
	}
	if looksLikeSimpleScriptRequest(lowered) {
		llmSpecFailed := false
		if allowLLMSpec {
			spec, ok := scriptIntentAnalyzer(trimmed)
			if ok && spec.Action == "script" {
				if command := scriptCommandFromLLMSpec(trimmed, spec); len(command) > 0 {
					return command, "write a bounded simple script from LLM intent spec"
				}
				// LLM classified as "script" but could not produce a valid
				// command. If the input has concrete requirements, fall
				// through to script --apply --llm; otherwise clarify.
				if !shouldForceLLMScriptGeneration(trimmed) {
					return nil, "script intent detected but LLM could not produce a valid script command; please clarify the script target"
				}
			} else if ok {
				// LLM returned a valid spec but action != "script".
				// If the input has no concrete requirements, return nil so the
				// caller falls back to a clarify decision rather than
				// reaching script --apply which will fail at scaffold time.
				if !shouldForceLLMScriptGeneration(trimmed) {
					return nil, "script intent detected but LLM could not determine the specific script target; please clarify"
				}
			} else {
				// LLM call failed entirely (e.g. API returning unparseable
				// output). Track this so we force --llm in the fallthrough.
				llmSpecFailed = true
			}
			// When !ok (LLM call failed) or input has concrete requirements:
			// fall through to the deterministic script --apply --llm path below.
		}
		scriptPath := extractSimpleScriptPath(trimmed)
		inferred := false
		if scriptPath == "" {
			scriptPath = inferSimpleScriptPath(trimmed)
			inferred = true
		}
		if scriptPath != "" {
			if inferred {
				scriptPath = nextAvailableScriptPath(scriptPath)
			}
			command := []string{"script", "--apply"}
			if shouldForceLLMScriptGeneration(trimmed) || llmSpecFailed {
				// Force --llm when the spec analyzer was available but
				// failed to produce a result (e.g., API returning
				// unparseable output). The deterministic scaffold needs
				// the LLM to generate content.
				command = append(command, "--llm")
			}
			command = append(command, scriptPath, trimmed)
			return command, "write a bounded simple script"
		}
	}
	if looksLikeInProcessLibraryCreate(lowered) {
		return libraryCreateRunCommand(trimmed), "in-process library/package — run, not bootstrap CLI scaffold"
	}
	if (looksLikeBootstrapProjectRequest(lowered) || looksLikeBootstrapLanguageFollowUp(lowered)) && currentWorkspaceLooksBootstrapEmpty() {
		spec := bootstrapSpecFromNaturalLanguage(trimmed)
		if allowLLMSpec {
			if llmSpec, ok := bootstrapIntentAnalyzer(trimmed); ok && llmSpec.Action == "bootstrap" {
				if strings.TrimSpace(llmSpec.Name) != "" {
					spec.Name = llmSpec.Name
				}
				if strings.TrimSpace(llmSpec.Module) != "" {
					spec.Module = llmSpec.Module
				}
				if strings.TrimSpace(llmSpec.Stack) != "" {
					spec.Stack = llmSpec.Stack
				}
				if llmSpec.Custom {
					spec.Spec = trimmed
				}
			}
		}
		if spec.Spec == "" && hasConcreteBootstrapRequirements(trimmed) {
			spec.Spec = trimmed
		}
		if spec.Name == "" {
			spec.Name = latestBootstrapProjectNameFromAnswer(".")
		}
		if spec.Name == "" {
			if cwd, err := os.Getwd(); err == nil {
				spec.Name = sanitizeBootstrapName(filepath.Base(cwd))
			}
		}
		if spec.Name == "" {
			spec.Name = "app"
		}
		command := []string{"bootstrap", "--apply", "--name", spec.Name}
		if spec.Stack != "" {
			command = append(command, "--stack", spec.Stack)
		}
		if spec.Module != "" {
			command = append(command, "--module", spec.Module)
		}
		if spec.Spec != "" {
			command = append(command, "--spec", spec.Spec)
		}
		return command, "bootstrap an empty project scaffold"
	}
	taskID := strings.TrimSpace(options.TaskID)
	if taskID == "" {
		taskID = taskIDFromIntent(trimmed)
	}

	permissionMode := naturalLanguagePermissionMode(lowered)
	if mode := strings.TrimSpace(options.PermissionMode); mode != "" {
		permissionMode = mode
	}
	// Batch2/B: "继续上一个被中断的构建…" → --resume latest transcript, not --new-task.
	if looksLikeResumeContinuationIntent(lowered) {
		if transcript := latestResumableTranscriptPath("."); transcript != "" {
			cmd := []string{"run", "--resume", transcript}
			if id := taskIDFromTranscriptPath(transcript); id != "" {
				cmd = append(cmd, "--task", id)
			}
			cmd = append(cmd, "--permission-mode", permissionMode, trimmed)
			return cmd, "resume the interrupted build from the latest transcript"
		}
	}
	if options.ForceNewTask || taskID == "" {
		return []string{"run", "--new-task", "--permission-mode", permissionMode, trimmed}, "run the safe question as a fresh task"
	}
	return []string{"run", "--task", taskID, "--permission-mode", permissionMode, trimmed}, "run the safe question in the named workspace"
}

// looksLikeContinuationWorkRequest is true when the user wants the agent to
// keep implementing (F107). Cross-language: "finish remaining" / "还缺 worker"
// must not be stolen by File-context or Last-action Q&A.
func looksLikeContinuationWorkRequest(lowered string) bool {
	lowered = strings.ToLower(strings.TrimSpace(lowered))
	if lowered == "" {
		return false
	}
	if file, rest := splitTrailingGuidanceFile(lowered); file != "" {
		if strings.TrimSpace(rest) == "" {
			return false
		}
		lowered = rest
	}
	if looksLikeBareKeepGoingPhrase(lowered) {
		return true
	}
	tokens := []string{
		"继续做完", "继续把", "继续完成", "请继续把", "请继续做",
		"继续推进", "接着推进", "往下推", "接着干",
		"剩余能力", "还缺", "立刻写", "不要只回答", "不要只报",
		"把剩余", "接着做完", "接着完成", "继续写",
		"finish remaining", "keep going", "continue implementing",
		"continue advancing", "keep advancing",
		"still missing", "please continue", "write the remaining",
		"finish the library", "finish the package", "finish the crate",
		"keep implementing", "don't just answer", "do not just answer",
		"remaining work", "remaining capability",
	}
	for _, t := range tokens {
		if strings.Contains(lowered, t) {
			return true
		}
	}
	if strings.Contains(lowered, "继续") &&
		(strings.Contains(lowered, "做完") || strings.Contains(lowered, "剩余") ||
			strings.Contains(lowered, "写") || strings.Contains(lowered, "实现") ||
			strings.Contains(lowered, "完成") || strings.Contains(lowered, "推进") ||
			strings.Contains(lowered, "执行") || strings.Contains(lowered, "开发") ||
			strings.Contains(lowered, "完善")) {
		return true
	}
	if strings.Contains(lowered, "continue") &&
		(strings.Contains(lowered, "finish") || strings.Contains(lowered, "implement") ||
			strings.Contains(lowered, "write") || strings.Contains(lowered, "remaining") ||
			strings.Contains(lowered, "complete") || strings.Contains(lowered, "advancing")) {
		return true
	}
	return false
}

func looksLikeBareKeepGoingPhrase(lowered string) bool {
	s := strings.ToLower(strings.TrimSpace(lowered))
	s = strings.TrimPrefix(s, "请")
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	prefixes := []string{
		"continue advancing", "keep advancing", "continue implementing",
		"keep going", "please continue", "go on", "continue",
		"继续推进", "接着推进", "继续执行", "继续完善", "继续开发",
		"继续完成", "继续做完", "接着做", "接着来", "接着干", "往下推",
		"继续吧", "接着吧", "继续", "接着",
	}
	for _, p := range prefixes {
		if keepGoingPrefixMatch(s, p) {
			return true
		}
	}
	return false
}

func keepGoingPrefixMatch(s, prefix string) bool {
	if s == prefix {
		return true
	}
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	rest := s[len(prefix):]
	if rest == "" {
		return true
	}
	if isASCIIWord(prefix) {
		switch rest[0] {
		case ' ', ',', '.', ':', ';', '!', '?':
			return true
		default:
			return false
		}
	}
	return true
}

func isASCIIWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' {
			continue
		}
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

func splitTrailingGuidanceFile(input string) (file, rest string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", ""
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", trimmed
	}
	last := fields[len(fields)-1]
	last = strings.Trim(last, `"'`)
	if strings.Contains(last, "://") {
		return "", trimmed
	}
	ext := strings.ToLower(filepath.Ext(last))
	if ext != ".md" && ext != ".txt" {
		return "", trimmed
	}
	rest = strings.TrimSpace(strings.Join(fields[:len(fields)-1], " "))
	return last, rest
}

// looksLikeResumeContinuationIntent is true when the user asks to continue an
// interrupted / previous build rather than start a fresh --new-task.
// Cross-language: "接着修 / 刚才那轮 / fix the failing tests" are the same
// continuation, whether the gate is go test, pytest, npm test, or cargo test.
func looksLikeResumeContinuationIntent(lowered string) bool {
	if looksLikeGreenfieldCreateRequest(lowered) {
		return false
	}
	if looksLikeContinuationWorkRequest(lowered) {
		return true
	}
	tokens := []string{
		"继续上一个", "接着上一个", "从中断", "中断处继续", "被中断",
		"继续上次", "接着上次", "resume from", "resume the", "continue from interrupt",
		"继续被中断", "中断的构建", "interrupted build", "from where we left",
		"刚才那轮", "刚才那次", "请接着", "接着修", "没过门", "修到能过",
		"接着上一轮", "继续上一轮", "continue last round", "last round's task",
		"还是红", "修到 go test", "修到 pytest", "修到 npm test", "修到 cargo",
		"fix the failing", "fix failing tests", "make tests pass", "make the tests pass",
		"continue fixing", "tests still fail", "test still fail",
	}
	for _, t := range tokens {
		if strings.Contains(lowered, t) {
			return true
		}
	}
	// "继续" + (构建|实现|任务|中断) without "确认计划"
	if strings.Contains(lowered, "继续") &&
		(strings.Contains(lowered, "构建") || strings.Contains(lowered, "实现") ||
			strings.Contains(lowered, "中断") || strings.Contains(lowered, "任务")) &&
		!strings.Contains(lowered, "确认计划") {
		return true
	}
	if strings.Contains(lowered, "接着") &&
		(strings.Contains(lowered, "修") || strings.Contains(lowered, "过门") ||
			strings.Contains(lowered, "绿") || strings.Contains(lowered, "fail") ||
			strings.Contains(lowered, "test")) {
		return true
	}
	return false
}

// looksLikeUserRejectedResume is true when the user wants a fresh --new-task
// rather than resuming. "不要 resume 那个数字" is NOT a reject — they still
// want the real transcript, not a snowflake run id (F114).
func looksLikeUserRejectedResume(lowered string) bool {
	lowered = strings.ToLower(strings.TrimSpace(lowered))
	if lowered == "" {
		return false
	}
	reject := strings.Contains(lowered, "不要 resume") || strings.Contains(lowered, "不要resume") ||
		strings.Contains(lowered, "别 resume") || strings.Contains(lowered, "别resume") ||
		strings.Contains(lowered, "don't resume") || strings.Contains(lowered, "do not resume") ||
		strings.Contains(lowered, "dont resume")
	if !reject {
		return false
	}
	if strings.Contains(lowered, "数字") || strings.Contains(lowered, "number") ||
		strings.Contains(lowered, "snowflake") || strings.Contains(lowered, "run id") ||
		strings.Contains(lowered, "run-id") || strings.Contains(lowered, "那个id") ||
		strings.Contains(lowered, "那个 id") {
		return false
	}
	return true
}

// coerceContinuationRunToResume rewrites LLM-chosen `run --new-task` into
// `--resume` when the user is clearly continuing the last attempt (F87).
func coerceContinuationRunToResume(nd naturalLanguageDecision, input, lowered string) naturalLanguageDecision {
	if nd.Kind != naturalLanguageDecisionSafeRun {
		return nd
	}
	if looksLikeUserRejectedResume(lowered) {
		return nd
	}
	if !looksLikeResumeContinuationIntent(lowered) {
		return nd
	}
	cmd := nd.Command
	if len(cmd) == 0 || cmd[0] != "run" {
		return nd
	}
	for _, a := range cmd {
		if a == "--resume" {
			return nd
		}
	}
	transcript := latestResumableTranscriptPath(".")
	if transcript == "" {
		return nd
	}
	nd.Command = rewriteRunCommandToResume(cmd, transcript, strings.TrimSpace(input))
	nd.Reason = nd.Reason + "; coerced --new-task to --resume (continuation)"
	return nd
}

// continuationWorkSafeRunDecision routes checklist-align and resume-repair
// utterances to run (resume when a transcript exists). Cross-language: the
// same steal happens after pytest / npm test / cargo test already went green.
func continuationWorkSafeRunDecision(input, lowered string) (naturalLanguageDecision, bool) {
	if !looksLikeChecklistProgressReconcile(lowered) && !looksLikeResumeContinuationIntent(lowered) {
		return naturalLanguageDecision{}, false
	}
	mode := naturalLanguagePermissionMode(lowered)
	if mode == "" {
		mode = "acceptEdits"
	}
	trimmed := strings.TrimSpace(input)
	file, _ := splitTrailingGuidanceFile(trimmed)
	if transcript := latestResumableTranscriptPath("."); transcript != "" {
		cmd := []string{"run", "--resume", transcript}
		if id := taskIDFromTranscriptPath(transcript); id != "" {
			cmd = append(cmd, "--task", id)
		}
		cmd = append(cmd, "--permission-mode", mode)
		if file != "" {
			cmd = append(cmd, "--from-file", file)
		} else {
			cmd = append(cmd, trimmed)
		}
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionSafeRun,
			Reason:     "checklist-align or resume continuation is a run, not a status recap",
			Command:    cmd,
			Confidence: 93,
		}, true
	}
	if file != "" {
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionSafeRun,
			Reason:     "checklist-align or resume continuation is a run, not a status recap",
			Command:    []string{"run", "--new-task", "--permission-mode", mode, "--from-file", file},
			Confidence: 90,
		}, true
	}
	return naturalLanguageDecision{
		Kind:       naturalLanguageDecisionSafeRun,
		Reason:     "checklist-align or resume continuation is a run, not a status recap",
		Command:    []string{"run", "--new-task", "--permission-mode", mode, trimmed},
		Confidence: 90,
	}, true
}

func rewriteRunCommandToResume(cmd []string, transcript, fallbackUser string) []string {
	mode := "default"
	user := strings.TrimSpace(fallbackUser)
	fromFile := ""
	skipNext := false
	for i := 1; i < len(cmd); i++ {
		if skipNext {
			skipNext = false
			continue
		}
		a := cmd[i]
		switch a {
		case "--new-task":
			continue
		case "--permission-mode":
			if i+1 < len(cmd) {
				mode = cmd[i+1]
				skipNext = true
			}
			continue
		case "--from-file":
			if i+1 < len(cmd) {
				fromFile = cmd[i+1]
				skipNext = true
			}
			continue
		case "--task", "--resume", "--progress":
			skipNext = true
			continue
		}
		if strings.HasPrefix(a, "--") {
			if !strings.Contains(a, "=") {
				skipNext = true
			}
			continue
		}
		user = a
	}
	out := []string{"run", "--resume", transcript}
	if id := taskIDFromTranscriptPath(transcript); id != "" {
		out = append(out, "--task", id)
	}
	if mode != "" {
		out = append(out, "--permission-mode", mode)
	}
	if fromFile != "" {
		out = append(out, "--from-file", fromFile)
		return out
	}
	if user != "" {
		out = append(out, user)
	}
	return out
}

// sanitizeResumeTranscriptArg replaces hallucinated --resume values (numeric
// run ids, missing files) with the latest real session jsonl (F113).
func sanitizeResumeTranscriptArg(cmd []string) []string {
	if len(cmd) < 2 || cmd[0] != "run" {
		return cmd
	}
	out := append([]string(nil), cmd...)
	resumeAt := -1
	hasTask := false
	for i := 1; i < len(out); i++ {
		switch out[i] {
		case "--resume":
			resumeAt = i
		case "--task":
			hasTask = true
		}
	}
	if resumeAt < 0 {
		return out
	}
	arg := ""
	hasValue := resumeAt+1 < len(out) && !strings.HasPrefix(out[resumeAt+1], "--")
	if hasValue {
		arg = strings.TrimSpace(out[resumeAt+1])
	}
	needReplace := !hasValue || looksLikeRunSnowflakeID(arg) || !looksLikeTranscriptPath(arg)
	if !needReplace {
		if _, err := os.Stat(arg); err != nil {
			needReplace = true
		}
	}
	if !needReplace {
		return out
	}
	tp := latestResumableTranscriptPath(".")
	if tp == "" {
		return out
	}
	if hasValue {
		out[resumeAt+1] = tp
	} else {
		tail := append([]string{tp}, out[resumeAt+1:]...)
		out = append(out[:resumeAt+1], tail...)
	}
	if !hasTask {
		if id := taskIDFromTranscriptPath(tp); id != "" {
			idx := resumeAt + 2
			if idx > len(out) {
				idx = len(out)
			}
			rest := append([]string{"--task", id}, out[idx:]...)
			out = append(out[:idx], rest...)
		}
	}
	return out
}

func looksLikeTranscriptPath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	return strings.HasSuffix(strings.ToLower(filepath.ToSlash(p)), ".jsonl")
}

func looksLikeRunSnowflakeID(p string) bool {
	p = strings.TrimSpace(p)
	if len(p) < 8 {
		return false
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// latestResumableTranscriptPath finds the best session jsonl under .avatars/tasks.
// Prefer the most substantial transcript (original run) over a tiny newer one
// created by a failed continuation (F114). Penalize prompt-filename slugs.
func latestResumableTranscriptPath(root string) string {
	tasksRoot := filepath.Join(root, ".avatars", "tasks")
	entries, err := os.ReadDir(tasksRoot)
	if err != nil {
		return ""
	}
	var best string
	var bestScore int64
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		sessions := filepath.Join(tasksRoot, ent.Name(), "sessions")
		sessEntries, err := os.ReadDir(sessions)
		if err != nil {
			continue
		}
		polluted := resumableTaskSlugPolluted(ent.Name())
		for _, s := range sessEntries {
			if s.IsDir() || !strings.HasSuffix(s.Name(), ".jsonl") {
				continue
			}
			full := filepath.Join(sessions, s.Name())
			info, err := s.Info()
			if err != nil {
				continue
			}
			size := info.Size()
			if size <= 0 {
				continue
			}
			score := size*1_000_000 + info.ModTime().Unix()
			if polluted {
				score = size*1_000 + info.ModTime().Unix()
			}
			if score > bestScore {
				bestScore = score
				rel, relErr := filepath.Rel(root, full)
				if relErr == nil {
					best = rel
				} else {
					best = full
				}
			}
		}
	}
	return filepath.ToSlash(best)
}

func resumableTaskSlugPolluted(taskID string) bool {
	lower := strings.ToLower(strings.TrimSpace(taskID))
	if lower == "" {
		return false
	}
	return strings.HasPrefix(lower, "prompttxt-") ||
		strings.Contains(lower, "-prompttxt-") ||
		strings.HasPrefix(lower, "prompt-") ||
		strings.Contains(lower, "-skill-go-go-test")
}

// taskIDFromTranscriptPath extracts `.avatars/tasks/<id>/sessions/...` id so
// --resume can bind --task without re-deriving a new slug (Batch5/P).
func taskIDFromTranscriptPath(transcriptPath string) string {
	cleaned := filepath.ToSlash(filepath.Clean(transcriptPath))
	parts := strings.Split(cleaned, "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "tasks" {
			id := parts[i+1]
			if id != "" && id != "." && id != ".." {
				return id
			}
		}
	}
	return ""
}

type explicitPatchSpec struct {
	Path string
	Old  string
	New  string
}

type scriptIntentSpec struct {
	Action         string `json:"action"`
	Language       string `json:"language"`
	Filename       string `json:"filename"`
	Topic          string `json:"topic"`
	ExpectedOutput string `json:"expected_output"`
	Confidence     int    `json:"confidence"`
}

type editIntentSpec struct {
	Action      string `json:"action"`
	Path        string `json:"path"`
	Instruction string `json:"instruction"`
	Confidence  int    `json:"confidence"`
}

var editIntentAnalyzer = analyzeEditIntentWithLLM

func analyzeEditIntentWithLLM(input string) (editIntentSpec, bool) {
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		if cleanup != nil {
			cleanup()
		}
		return editIntentSpec{}, false
	}
	if cleanup != nil {
		defer cleanup()
	}
	llmCtx, llmCancel := llmCallContext()
	defer llmCancel()
	response, err := client.Generate(llmCtx, llm.Request{
		SystemPrompt:     editIntentSystemPrompt(),
		UserPrompt:       editIntentUserPrompt(input),
		StructuredOutput: true,
	})
	if err != nil || response.Fallback {
		return editIntentSpec{}, false
	}
	return parseEditIntentSpec(response.Text)
}

func editIntentSystemPrompt() string {
	return `Classify one user request for a CLI harness. Return compact JSON only:
{"action":"edit|not_edit|clarify","path":"","instruction":"","confidence":0}
Rules:
- Choose action=edit only when the user asks to change, add, remove, refactor, fix, or update an existing file.
- Extract the exact relative file path if present. Do not invent a path.
- Put the concrete requested code/content change in instruction, preserving function names, behavior, constraints, and requested output.
- Choose not_edit for new script/project generation, repository analysis, chat, status, result-location, or vague questions.
- Choose not_edit when the user wants to CREATE something new (game, app, tool, script). These belong on the script path, not edit.
- Choose clarify when the request asks for an edit but lacks the target path or concrete requested change.
- Do not include paths outside the current directory.
- Chinese creation requests (写一个游戏, 搞一个工具, etc.) → action=not_edit, NOT edit.
- Only "修改/更新/修复/改" + existing file → action=edit.`
}

func editIntentUserPrompt(input string) string {
	return "User request:\n" + strings.TrimSpace(input)
}

func parseEditIntentSpec(text string) (editIntentSpec, bool) {
	raw, ok := robustExtractJSON(text)
	if !ok {
		return editIntentSpec{}, false
	}
	raw = cleanJSONString(raw)
	var spec editIntentSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return editIntentSpec{}, false
	}
	spec.Action = strings.ToLower(strings.TrimSpace(spec.Action))
	spec.Path = strings.TrimSpace(spec.Path)
	spec.Instruction = strings.TrimSpace(spec.Instruction)
	if spec.Action == "" {
		return editIntentSpec{}, false
	}
	return spec, true
}

func editCommandFromLLMSpec(input string, spec editIntentSpec) []string {
	if spec.Action != "edit" {
		return nil
	}
	path := strings.TrimSpace(spec.Path)
	if path == "" || !looksLikePatchableRelativePath(path) {
		return nil
	}
	instruction := strings.TrimSpace(spec.Instruction)
	if instruction == "" {
		instruction = strings.TrimSpace(input)
	}
	if instruction == "" {
		return nil
	}
	return []string{"edit", "--apply", filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))), instruction}
}

var scriptIntentAnalyzer = analyzeScriptIntentWithLLM

func analyzeScriptIntentWithLLM(input string) (scriptIntentSpec, bool) {
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		if cleanup != nil {
			cleanup()
		}
		return scriptIntentSpec{}, false
	}
	if cleanup != nil {
		defer cleanup()
	}
	llmCtx, llmCancel := llmCallContext()
	defer llmCancel()
	response, err := client.Generate(llmCtx, llm.Request{
		SystemPrompt:     scriptIntentSystemPrompt(),
		UserPrompt:       scriptIntentUserPrompt(input),
		StructuredOutput: true,
	})
	if err != nil || response.Fallback {
		return scriptIntentSpec{}, false
	}
	return parseScriptIntentSpec(response.Text)
}

func scriptIntentSystemPrompt() string {
	return `Classify one user request for a CLI harness. Return compact JSON only:
{"action":"script|not_script|clarify","language":"python|javascript|powershell|shell|unknown","filename":"","topic":"","expected_output":"","confidence":0}
Rules:
- Choose action=script ONLY when the user wants to CREATE a standalone program, game, app, tool, or utility.
- Choose action=not_script for analysis questions, status queries, chat, edits to existing files, or vague requests.
- Choose action=clarify when the request is ambiguous about WHAT to create.
- Infer filename when absent. Use safe lowercase ASCII with extension. Prefer descriptive names over script.py.
- Fill topic when the script has domain behavior, algorithm, puzzle, calculation, report, visualization, parser, or any user-specific output.
- Leave topic empty only for generic placeholder requests such as "write a simple script hello.py".
- If the request includes a problem statement after ":" or "：", summarize it in topic.
- Do not include paths outside current directory.
- When topic is non-empty AND the script's output is deterministic and predictable (table, sequence, formula, fixed text), also fill expected_output with the exact stdout the script should emit. Use \n for newlines. Leave expected_output empty when the output is non-deterministic, side-effecting, time-dependent, network-dependent, or interactive.
Routing guidance:
- User says "write a game", "create a tool", "make a calculator", "帮我写一个游戏", "写脚本", "搞一个小工具", "弄一个程序" → action=script, NOT not_script.
- Chinese creation verbs (写/搞/弄/做) + creation targets (游戏/程序/应用/工具/脚本) = action=script.
- If the user says "write a game" or "写一个游戏", choose action=script with a descriptive filename like riddle_game.py or number_guess.py. Do NOT classify this as not_script.
- Single-file creation intent → action=script. Multi-file project intent → action=clarify (bootstrap path). Package/library/module creation is NOT a script: "Go package", "Go工具包", "Python library", "npm package", "Rust crate" → action=not_script. "创建Go包", "写一个Go库", "Python模块" → action=not_script. Requests with "package", "包", "library", "库", "module", "模块", "crate" + language → action=not_script.`
}

func scriptIntentUserPrompt(input string) string {
	return "User request:\n" + strings.TrimSpace(input)
}

func parseScriptIntentSpec(text string) (scriptIntentSpec, bool) {
	raw, ok := robustExtractJSON(text)
	if !ok {
		return scriptIntentSpec{}, false
	}
	raw = cleanJSONString(raw)
	var spec scriptIntentSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return scriptIntentSpec{}, false
	}
	spec.Action = strings.ToLower(strings.TrimSpace(spec.Action))
	spec.Language = strings.ToLower(strings.TrimSpace(spec.Language))
	spec.Filename = strings.TrimSpace(spec.Filename)
	spec.Topic = strings.TrimSpace(spec.Topic)
	if spec.Action == "" {
		return scriptIntentSpec{}, false
	}
	return spec, true
}

func scriptCommandFromLLMSpec(input string, spec scriptIntentSpec) []string {
	if spec.Action != "script" {
		return nil
	}
	path := strings.TrimSpace(spec.Filename)
	if path == "" || !looksLikePatchableRelativePath(path) {
		path = inferScriptPathFromLLMSpec(input, spec)
	}
	if _, err := safeRelativeScriptPath(path); err != nil {
		path = inferScriptPathFromLLMSpec(input, spec)
	}
	if path == "" {
		return nil
	}
	path = nextAvailableScriptPath(path)
	command := []string{"script", "--apply"}
	if strings.TrimSpace(spec.Topic) != "" || shouldForceLLMScriptGeneration(input) {
		command = append(command, "--llm")
	}
	if sidecar, ok := writeScriptExpectedOutputSidecar(path, spec.ExpectedOutput); ok {
		command = append(command, "--expected-output-file", sidecar)
	}
	command = append(command, path, strings.TrimSpace(input))
	return command
}

// writeScriptExpectedOutputSidecar persists spec.ExpectedOutput to a
// sidecar file co-located with the script and returns the relative path
// suitable for passing as --expected-output-file. Returns ok=false when
// the spec did not provide an expected output, or when the sidecar
// could not be written. Sidecar writes that fail are non-fatal: the
// script command can still proceed with the weak (exit 0 + non-empty
// stdout) verifier fallback.
func writeScriptExpectedOutputSidecar(scriptPath string, expectedOutput string) (string, bool) {
	expected := strings.TrimSpace(expectedOutput)
	if expected == "" {
		return "", false
	}
	sidecar := expectedOutputSidecarPath(scriptPath)
	if err := os.WriteFile(sidecar, []byte(expectedOutput), 0o644); err != nil {
		return "", false
	}
	return sidecar, true
}

func expectedOutputSidecarPath(scriptPath string) string {
	base := strings.TrimSuffix(scriptPath, filepath.Ext(scriptPath))
	return base + ".expected.txt"
}

func inferScriptPathFromLLMSpec(input string, spec scriptIntentSpec) string {
	extension := ".py"
	switch spec.Language {
	case "javascript":
		extension = ".js"
	case "powershell":
		extension = ".ps1"
	case "shell":
		extension = ".sh"
	case "python":
		extension = ".py"
	}
	topic := strings.TrimSpace(spec.Topic)
	if topic == "" {
		topic = extractSpecificScriptTopic(input)
	}
	base := sanitizeBootstrapName(topic)
	if base == "" || base == "app" {
		base = inferSimpleScriptBaseName(input)
	}
	return base + extension
}

func shouldForceLLMScriptGeneration(input string) bool {
	return hasSpecificScriptTopic(input) || attachedFileTaskSpecForceLLM() || hasConcreteScriptRequirements(input)
}

func hasConcreteScriptRequirements(input string) bool {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return false
	}
	remainder := stripSimpleScriptBoilerplate(trimmed)
	if len([]rune(remainder)) < 8 {
		return false
	}
	if strings.ContainsAny(remainder, "(),，:：=+-*/%0123456789") {
		return true
	}
	return len([]rune(remainder)) >= 18
}

func stripSimpleScriptBoilerplate(input string) string {
	trimmed := strings.ToLower(strings.TrimSpace(input))
	if path := extractSimpleScriptPath(trimmed); path != "" {
		trimmed = strings.ReplaceAll(trimmed, strings.ToLower(path), " ")
	}
	replacer := strings.NewReplacer(
		"write a simple script", " ",
		"write a script", " ",
		"write script", " ",
		"create script", " ",
		"generate script", " ",
		"simple script", " ",
		"write a game", " ",
		"write game", " ",
		"create game", " ",
		"make game", " ",
		"build game", " ",
		"write a program", " ",
		"write program", " ",
		"create program", " ",
		"write app", " ",
		"create app", " ",
		"build app", " ",
		"python", " ",
		"javascript", " ",
		"node.js", " ",
		"nodejs", " ",
		"powershell", " ",
		"shell", " ",
		"bash", " ",
		"用 python", " ",
		"用python", " ",
		"用 py", " ",
		"用py", " ",
		"python脚本", " ",
		"py脚本", " ",
		"写一个简单脚本", " ",
		"写一个脚本", " ",
		"写脚本", " ",
		"写一个游戏", " ",
		"写游戏", " ",
		"写程序", " ",
		"写一个程序", " ",
		"写应用", " ",
		"写工具", " ",
		"写一个小", " ",
		"生成脚本", " ",
		"创建脚本", " ",
		"简单脚本", " ",
		"脚本", " ",
		"帮我", " ",
		"请", " ",
	)
	return strings.Join(strings.Fields(replacer.Replace(trimmed)), " ")
}

func extractExplicitPatchSpec(input string) (explicitPatchSpec, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return explicitPatchSpec{}, false
	}
	patterns := []struct {
		Pattern string
		Path    int
		Old     int
		New     int
	}{
		{Pattern: `(?i)(?:把|将)\s*([^\s，。；;]+)\s*(?:里的|中的|里|中)\s*(.+?)\s*(?:改成|替换为|换成)\s*(.+)$`, Path: 1, Old: 2, New: 3},
		{Pattern: `(?i)replace\s+(.+?)\s+with\s+(.+?)\s+in\s+([^\s]+)`, Path: 3, Old: 1, New: 2},
	}
	for _, item := range patterns {
		match := regexp.MustCompile(item.Pattern).FindStringSubmatch(trimmed)
		if len(match) <= item.Path || len(match) <= item.Old || len(match) <= item.New {
			continue
		}
		path := trimPatchToken(match[item.Path])
		oldValue := trimPatchToken(match[item.Old])
		newValue := trimPatchToken(match[item.New])
		if path == "" || oldValue == "" || newValue == "" {
			continue
		}
		if !looksLikePatchableRelativePath(path) {
			continue
		}
		return explicitPatchSpec{Path: filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))), Old: oldValue, New: newValue}, true
	}
	return explicitPatchSpec{}, false
}

func trimPatchToken(value string) string {
	return strings.Trim(strings.TrimSpace(value), "\"'“”`，。；;")
}

func looksLikePatchableRelativePath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || filepath.IsAbs(trimmed) {
		return false
	}
	cleaned := filepath.Clean(filepath.FromSlash(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return false
	}
	return strings.Contains(filepath.Base(cleaned), ".")
}

// isAnalysisRequest returns true when the input is an analysis/read-only request
// that mentions file types (like "统计Go文件", "找出所有Python文件"). This prevents
// false-positive script routing where "go文件" / ".py" etc. would otherwise match
// script-creation tokens in looksLikeSimpleScriptRequest.
func isAnalysisRequest(lowered string) bool {
	// Analysis verbs: asking to count, find, search, list, or inspect — NOT create.
	analysisVerbs := []string{
		"find", "search", "count", "list", "grep", "scan", "locate",
		"统计", "找出", "搜索", "查找", "列出", "扫描", "定位", "检索",
		"how many", "多少", "几个",
	}
	hasAnalysisVerb := false
	for _, v := range analysisVerbs {
		if strings.Contains(lowered, v) {
			hasAnalysisVerb = true
			break
		}
	}
	if !hasAnalysisVerb {
		return false
	}
	// Must also mention source files or project scope to be an analysis request.
	// This avoids over-matching on generic questions like "how many functions?"
	fileTokens := []string{
		"文件", "file", ".go", ".py", ".js", ".ts", ".rs", ".java", ".cs",
		"代码", "code", "项目", "project", "repository", "repo", "仓库",
		"todo", "fixme", "注释", "comment",
	}
	for _, t := range fileTokens {
		if strings.Contains(lowered, t) {
			return true
		}
	}
	return false
}

// isEditIntent returns true when the instruction describes editing or modifying
// an existing file rather than creating a new one. This prevents the broad
// .go/.py/.js extension match from routing edit requests to script --apply.
func isEditIntent(lowered string) bool {
	// Must contain both an edit verb AND a file path reference.
	hasEditVerb := containsAnyIntentToken(lowered,
		"modify", "edit", "update", "change", "fix", "refactor", "rename",
		"修改", "改", "编辑", "更新", "修复", "重构", "更改",
	)
	// Check for file path patterns: main.go, cmd/foo/bar.go, src/index.js etc.
	hasFilePath := strings.Contains(lowered, ".") && (strings.Contains(lowered, ".go") || strings.Contains(lowered, ".py") ||
		strings.Contains(lowered, ".js") || strings.Contains(lowered, ".ts") ||
		strings.Contains(lowered, ".rs") || strings.Contains(lowered, ".java") ||
		strings.Contains(lowered, ".cs") || strings.Contains(lowered, ".md") ||
		strings.Contains(lowered, ".json") || strings.Contains(lowered, ".yaml") ||
		strings.Contains(lowered, ".yml") || strings.Contains(lowered, ".toml") ||
		strings.Contains(lowered, ".html") || strings.Contains(lowered, ".css") ||
		strings.Contains(lowered, ".xml") || strings.Contains(lowered, ".sql"))
	// Also match instructions like "修改 README" or "改一下 main.go"
	if !hasFilePath {
		// Check for bare filename references like "readme", "main", "index"
		hasFilePath = strings.Contains(lowered, "readme") ||
			strings.Contains(lowered, "main.go") || strings.Contains(lowered, "main.py") ||
			strings.Contains(lowered, "index.js") || strings.Contains(lowered, "主程序")
	}
	return hasEditVerb && hasFilePath
}

func looksLikeSimpleScriptRequest(lowered string) bool {
	if containsAnyIntentToken(lowered, "do not modify", "don't modify", "read-only", "readonly", "不要改", "只读", "只分析") {
		return false
	}
	// Package/library/module creation is NOT a simple script.
	if (strings.Contains(lowered, "包") || strings.Contains(lowered, "package") ||
		strings.Contains(lowered, "库") || strings.Contains(lowered, "library") ||
		strings.Contains(lowered, "模块") || strings.Contains(lowered, "module") ||
		strings.Contains(lowered, "crate")) &&
		!strings.Contains(lowered, "脚本") && !strings.Contains(lowered, "script") {
		return false
	}
	// Analysis-context guard: when the user asks to count/find/search/list files,
	// it's an analysis request, not a script creation request. Without this guard,
	// "统计这个项目有多少Go文件" would trigger script routing because "go文件"
	// matches the file-type token below.
	if isAnalysisRequest(lowered) {
		return false
	}
	// I10 fix: Edit-intent guard. "修改 main.go 里的函数" should route to
	// edit, not script. The broad .go/.py/.js match below catches ALL
	// instructions containing file extensions — including edit requests.
	// This guard excludes edit/modify requests so they fall through to
	// looksLikeImplementationWorkIntent which handles edits correctly.
	if isEditIntent(lowered) {
		return false
	}

	// Direct check for Chinese and broad creation intent tokens.
	// NOTE: containsAnyIntentToken variadic has encoding issues with multi-byte
	// CJK characters in long token lists on some Go compiler versions, so we
	// use direct strings.Contains checks for the critical tokens.
	if strings.Contains(lowered, "游戏") || strings.Contains(lowered, "程序") ||
		strings.Contains(lowered, "应用") || strings.Contains(lowered, "工具") ||
		strings.Contains(lowered, "小游戏") || strings.Contains(lowered, "写脚本") ||
		strings.Contains(lowered, "写一个脚本") || strings.Contains(lowered, "写一个简单脚本") ||
		strings.Contains(lowered, "生成脚本") || strings.Contains(lowered, "创建脚本") ||
		strings.Contains(lowered, "脚本") || strings.Contains(lowered, "写一个游戏") ||
		strings.Contains(lowered, "写游戏") || strings.Contains(lowered, "做游戏") ||
		strings.Contains(lowered, "写程序") || strings.Contains(lowered, "做一个程序") ||
		strings.Contains(lowered, "写应用") || strings.Contains(lowered, "写工具") ||
		strings.Contains(lowered, "写一个小") || strings.Contains(lowered, "搞一个") ||
		strings.Contains(lowered, "弄一个") || strings.Contains(lowered, "做一个小") ||
		// BUG-3.4: Add .html/.htm creation detection
		strings.Contains(lowered, ".html") || strings.Contains(lowered, ".htm") ||
		strings.Contains(lowered, "网页") || strings.Contains(lowered, "页面") ||
		strings.Contains(lowered, "写html") || strings.Contains(lowered, "创建html") ||
		strings.Contains(lowered, "写一个html") || strings.Contains(lowered, "写一个网页") ||
		// write a [language] script patterns
		(strings.Contains(lowered, "write") && strings.Contains(lowered, "script")) ||
		// P0-2 fix: detect source file creation across all supported languages
		strings.Contains(lowered, ".go") || strings.Contains(lowered, ".py") ||
		strings.Contains(lowered, ".js") || strings.Contains(lowered, ".ts") ||
		strings.Contains(lowered, ".rs") || strings.Contains(lowered, ".java") ||
		strings.Contains(lowered, ".cs") || strings.Contains(lowered, ".sql") ||
		strings.Contains(lowered, ".xml") ||
		strings.Contains(lowered, "go程序") || strings.Contains(lowered, "go 程序") ||
		strings.Contains(lowered, "go文件") || strings.Contains(lowered, "go 文件") ||
		strings.Contains(lowered, "create a file") || strings.Contains(lowered, "write a file") ||
		strings.Contains(lowered, "generate a file") || strings.Contains(lowered, "create file") ||
		strings.Contains(lowered, "write file") || strings.Contains(lowered, "generate file") {
		return true
	}
	return containsAnyIntentToken(lowered,
		"write a script",
		"write script",
		"create script",
		"generate script",
		"simple script",
		"write a game",
		"write game",
		"create game",
		"make game",
		"build game",
		"write a program",
		"write program",
		"create program",
		"write app",
		"create app",
		"build app",
		"write tool",
		"create tool",
		"write utility",
		"make tool",
		"write calculator",
		"game",
		// BUG-3.4: Add .html creation detection
		"write html",
		"create html",
		"write a html",
		"create a html",
		"write webpage",
		"create webpage",
		"build webpage",
		"write page",
		"create page",
		// P0-2 fix: generic file creation detection
		"create a file",
		"write a file",
		"generate a file",
		"create file",
		"write file",
		".go",
		".py",
		".rs",
		".java",
		".cs",
		".sql",
		".xml",
		".ts",
	)
}

func inferSimpleScriptPath(input string) string {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if lowered == "" {
		return ""
	}
	extension := ".py"
	switch {
	case containsAnyIntentToken(lowered, "javascript", "node", "nodejs", "node.js", "用js", "用 js"):
		extension = ".js"
	case containsAnyIntentToken(lowered, "powershell", "pwsh", "用powershell"):
		extension = ".ps1"
	case containsAnyIntentToken(lowered, "shell", "bash", "sh脚本"):
		extension = ".sh"
	case containsAnyIntentToken(lowered, "python", "用python", "用 python", "py脚本", "用py", "用 py"):
		extension = ".py"
	// HTML and web file types
	case containsAnyIntentToken(lowered, "html", "htm", "网页", "页面", ".html", ".htm"):
		extension = ".html"
	case containsAnyIntentToken(lowered, "css", "样式", "样式表", "stylesheet"):
		extension = ".css"
	case containsAnyIntentToken(lowered, "markdown", "md文件", "readme", "文档", ".md"):
		extension = ".md"
	case containsAnyIntentToken(lowered, "json", "json文件", ".json"):
		extension = ".json"
	case containsAnyIntentToken(lowered, "yaml", "yml", "yaml文件", "yml文件", ".yaml", ".yml"):
		extension = ".yaml"
	case containsAnyIntentToken(lowered, "toml", "toml文件", "config", "配置", ".toml"):
		extension = ".toml"
	case containsAnyIntentToken(lowered, "txt", "text", "文本", "纯文本", ".txt"):
		extension = ".txt"
	case containsAnyIntentToken(lowered, "typescript", "ts", "tsx", ".ts", ".tsx"):
		extension = ".ts"
	case containsAnyIntentToken(lowered, "go语言", "golang", "go", ".go"):
		extension = ".go"
	case containsAnyIntentToken(lowered, "rust", "rust语言", ".rs"):
		extension = ".rs"
	case containsAnyIntentToken(lowered, "java", "java语言", ".java"):
		extension = ".java"
	case containsAnyIntentToken(lowered, "c#", "csharp", "c sharp", ".cs"):
		extension = ".cs"
	case containsAnyIntentToken(lowered, "sql", "sql文件", "sql查询", ".sql"):
		extension = ".sql"
	case containsAnyIntentToken(lowered, "xml", "xml文件", ".xml"):
		extension = ".xml"
	case containsAnyIntentToken(lowered, "csv", "csv文件", ".csv"):
		extension = ".csv"
	case containsAnyIntentToken(lowered, "ini", "ini文件", "ini配置", ".ini"):
		extension = ".ini"
	case containsAnyIntentToken(lowered, "cfg", "cfg文件", ".cfg"):
		extension = ".cfg"
	case containsAnyIntentToken(lowered, "env", "env文件", "环境变量", ".env"):
		extension = ".env"
	}
	return inferSimpleScriptBaseName(input) + extension
}

func inferSimpleScriptBaseName(input string) string {
	if topic := extractSpecificScriptTopic(input); topic != "" {
		if slug := sanitizeBootstrapName(topic); slug != "" && slug != "app" {
			return slug
		}
		sum := sha256.Sum256([]byte(topic))
		return "topic-" + hex.EncodeToString(sum[:])[:8]
	}
	return "script"
}

func nextAvailableScriptPath(path string) string {
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if _, err := os.Stat(filepath.FromSlash(cleaned)); os.IsNotExist(err) {
		return cleaned
	}
	extension := filepath.Ext(cleaned)
	base := strings.TrimSuffix(cleaned, extension)
	for index := 2; index < 1000; index++ {
		candidate := fmt.Sprintf("%s-%d%s", base, index, extension)
		if _, err := os.Stat(filepath.FromSlash(candidate)); os.IsNotExist(err) {
			return candidate
		}
	}
	return cleaned
}

func extractSimpleScriptPath(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}
	// NL1: Skip paths that match the currently loaded @file context.
	// When user says "根据 X.md 的指导，生成 HTML", X.md is a reference
	// file, not the creation target. The regex below would greedily
	// extract X.md; we must filter it out so the caller falls through
	// to inferSimpleScriptPath which generates a topic-based name.
	var skipPath string
	if fc := getReplFileContext(); fc != nil {
		if p := strings.TrimSpace(fc.Path); p != "" {
			skipPath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
		}
	}
	matches := regexp.MustCompile(`(?i)([a-zA-Z0-9][a-zA-Z0-9._/-]*\.(?:json|mjs|js|tsx|ts|go|rs|java|cs|ps1|sh|html|css|md|txt|ya?ml|toml|sql|xml|csv|ini|cfg|env|py))`).FindAllStringSubmatch(trimmed, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		path := strings.Trim(match[1], "\"'“”")
		if _, err := safeRelativeScriptPath(path); err == nil {
			normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
			// NL1: exclude the loaded @file context path.
			if skipPath != "" && strings.EqualFold(normalized, skipPath) {
				continue
			}
			return normalized
		}
	}
	return ""
}

func looksLikeApprovalCommandQuestion(lowered string) bool {
	approvalSignal := containsAnyIntentToken(lowered, "approval", "approve", "审批", "批准")
	commandSignal := containsAnyIntentToken(lowered, "command", "cli", "指令", "命令", "怎么", "是什么")
	return approvalSignal && commandSignal
}

func looksLikeScriptLocationQuestion(lowered string) bool {
	return containsAnyIntentToken(lowered, "脚本在哪", "脚本在哪里", "写的脚本在哪", "script location", "where is the script", "where did you write")
}

func summarizeScriptLocation(root string) (string, error) {
	if approval := summarizeLatestApprovalBlock(root); approval != "" {
		return "Script location:\n- not written yet; latest write is awaiting approval\n" + approval, nil
	}
	if artifact, ok, err := loadREPLLastTurn(root); err != nil {
		return "", err
	} else if ok && artifact.Kind == "script" {
		lines := []string{"Script location:"}
		if artifact.Status != "" {
			lines = append(lines, "- status: "+artifact.Status)
		}
		for _, path := range artifact.Artifacts {
			if strings.TrimSpace(path) != "" {
				lines = append(lines, "- "+filepath.ToSlash(path))
			}
		}
		if len(lines) > 2 {
			return strings.Join(lines, "\n"), nil
		}
	}
	files, err := listProjectFiles(root, -1)
	if err != nil {
		return "", err
	}
	scripts := []string{}
	for _, file := range files {
		extension := strings.ToLower(filepath.Ext(file))
		if extension == ".py" || extension == ".js" || extension == ".mjs" || extension == ".ps1" || extension == ".sh" {
			scripts = append(scripts, filepath.ToSlash(file))
		}
	}
	if len(scripts) == 0 {
		return "Script location:\n- no script file found in project root scan", nil
	}
	limit := 5
	if len(scripts) < limit {
		limit = len(scripts)
	}
	lines := []string{"Script location:"}
	for index := 0; index < limit; index++ {
		lines = append(lines, "- "+scripts[index])
	}
	return strings.Join(lines, "\n"), nil
}

type bootstrapNaturalLanguageSpec struct {
	Name   string
	Module string
	Stack  string
	Spec   string
}

func bootstrapSpecFromNaturalLanguage(input string) bootstrapNaturalLanguageSpec {
	trimmed := strings.TrimSpace(input)
	return bootstrapNaturalLanguageSpec{
		Name:   extractBootstrapProjectName(trimmed),
		Module: extractBootstrapModule(trimmed),
		Stack:  extractBootstrapStack(trimmed),
	}
}

type bootstrapIntentSpec struct {
	Action     string `json:"action"`
	Name       string `json:"name"`
	Module     string `json:"module"`
	Stack      string `json:"stack"`
	Custom     bool   `json:"custom"`
	Confidence int    `json:"confidence"`
}

var bootstrapIntentAnalyzer = analyzeBootstrapIntentWithLLM

func analyzeBootstrapIntentWithLLM(input string) (bootstrapIntentSpec, bool) {
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		if cleanup != nil {
			cleanup()
		}
		return bootstrapIntentSpec{}, false
	}
	if cleanup != nil {
		defer cleanup()
	}
	llmCtx, llmCancel := llmCallContext()
	defer llmCancel()
	response, err := client.Generate(llmCtx, llm.Request{
		SystemPrompt:     bootstrapIntentSystemPrompt(),
		UserPrompt:       bootstrapIntentUserPrompt(input),
		StructuredOutput: true,
	})
	if err != nil || response.Fallback {
		return bootstrapIntentSpec{}, false
	}
	return parseBootstrapIntentSpec(response.Text)
}

func bootstrapIntentSystemPrompt() string {
	return `Classify one empty-project bootstrap request. Return compact JSON only:
{"action":"bootstrap|not_bootstrap|clarify","name":"","module":"","stack":"go-cli|python-cli|node-cli|rust-cli|","custom":false,"confidence":0}
Rules:
- Choose bootstrap only when the user asks to create/scaffold/start a project in an empty workspace.
- Infer name from explicit app/project/CLI names such as "todo-cli" or "ledger-cli"; leave empty if no name is present.
- Choose stack from the requested language/runtime.
- Set custom=true when the request includes domain behavior, commands, storage, architecture, tests, API, UI, data model, or anything beyond a generic empty scaffold.
- Set custom=false only for a generic empty project with no product behavior.
- Choose not_bootstrap for editing existing files, writing one standalone script, repository analysis, chat, status, or result questions.
- Do not include paths outside the current directory.`
}

func bootstrapIntentUserPrompt(input string) string {
	return "User request:\n" + strings.TrimSpace(input)
}

func parseBootstrapIntentSpec(text string) (bootstrapIntentSpec, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return bootstrapIntentSpec{}, false
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		trimmed = trimmed[start : end+1]
	}
	var spec bootstrapIntentSpec
	if err := json.Unmarshal([]byte(trimmed), &spec); err != nil {
		return bootstrapIntentSpec{}, false
	}
	spec.Action = strings.ToLower(strings.TrimSpace(spec.Action))
	spec.Name = sanitizeBootstrapName(spec.Name)
	spec.Module = strings.TrimSpace(spec.Module)
	spec.Stack = strings.ToLower(strings.TrimSpace(spec.Stack))
	switch spec.Stack {
	case "go-cli", "python-cli", "node-cli", "rust-cli", "":
	default:
		spec.Stack = ""
	}
	if spec.Action == "" {
		return bootstrapIntentSpec{}, false
	}
	return spec, true
}

// naturalLanguagePermissionMode picks a CLI `--permission-mode` value
// for a free-form request, based on the request's intent.
//
// Three tiers:
//   - "plan": read-only / analysis. The LLM must not write files or
//     run mutating actions without explicit approval.
//   - "acceptEdits": mutating coding (create/edit files) and explicit
//     before/after replacements. Interactive approval for named mode
//     `default` is not implemented (S7A); writes use acceptEdits.
//
// Anything that does not look mutating falls back to "plan" so the
// LLM does not silently start writing files. Explicit replacements
// are checked first because they are the most specific signal.
func naturalLanguagePermissionMode(lowered string) string {
	if looksLikeExplicitStringReplacement(lowered) {
		return "acceptEdits"
	}
	// Repair/make-tests-green beats checklist-align and "only analyze" phrasing.
	if looksLikeTestRepairRequest(lowered) {
		return "acceptEdits"
	}
	if looksLikeNegatedReadOnlyOnly(lowered) && looksLikeMutatingCodingRequest(lowered) {
		return "acceptEdits"
	}
	// Analysis/report intents stay plan even when they also "写入 foo.md".
	if looksLikeReadOnlySurveyIntent(lowered) {
		return "plan"
	}
	if looksLikeContinuationWorkRequest(lowered) {
		return "acceptEdits"
	}
	if looksLikeMutatingCodingRequest(lowered) {
		return "acceptEdits"
	}
	return "plan"
}

func looksLikeNegatedReadOnlyOnly(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"别只分析", "不要只分析", "别再只分析", "不要只是分析",
		"别只读", "不要只读", "别再只读",
		"don't just analyze", "dont just analyze", "do not just analyze",
		"don't only analyze", "do not only analyze",
		"don't just analyse", "do not just analyse",
		"stop just analyzing", "stop only analyzing",
		"not just analyz", "not only analyz",
		"don't just inspect", "stop just inspecting",
		"don't just look", "stop just looking",
		"stop analyzing only",
	)
}

func looksLikeTestRepairRequest(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"修绿", "修到测试", "测试绿", "跑绿", "还是红",
		"make tests pass", "make the tests pass", "fix the failing",
		"fix failing tests", "tests still fail", "test still fail",
		"tests are red", "still red", "until tests pass", "until tests are green",
		"pytest still", "npm test still", "cargo test still", "go test still",
		"tests still red", "keep going until tests",
	)
}

func looksLikeReadOnlySurveyIntent(lowered string) bool {
	if looksLikeNegatedReadOnlyOnly(lowered) {
		return false
	}
	if containsAnyIntentToken(lowered, "do not modify", "don't modify", "read-only", "readonly", "不要改", "不改代码", "只读", "只分析") {
		return true
	}
	if !containsAnyIntentToken(lowered, "analyze", "analyse", "inspect", "review", "分析", "找问题", "评价") {
		return false
	}
	return !containsAnyIntentToken(lowered, "implement", "write a script", "write script", "写代码", "写脚本", "修复")
}

// looksLikeExplicitStringReplacement reports whether the request
// describes a precise before/after change (a patch-style edit),
// which is the trigger for the `acceptEdits` permission mode. We
// keep this deliberately narrow: the user must use words like
// "replace X with Y" or "把 ... 替换成"; we do NOT want "fix the
// typo" or "improve the wording" to skip the approval boundary.
func looksLikeExplicitStringReplacement(lowered string) bool {
	return containsAnyIntentToken(lowered,
		"replace",
		"substitute",
		"swap",
		"替换",
		"替换成",
		"替换为",
		"改为",
		"改成",
	)
}

// isGenericRunFallbackCommand reports whether a built command is the
// plan-mode "run"兜底 produced when no specific intent (script/edit/bootstrap/
// patch) was matched inside buildNaturalLanguageTaskCommandWithLLM. The
// route is a "run --permission-mode plan" with no script/edit/bootstrap shape.
//
// When the LLM classifier returns safe_run with a command of this shape, the
// user's input was too vague for any specific route to fire. Treating it as
// safe_run forces a plan-mode run without an explicit user confirmation; the
// caller should instead produce a Clarify so the user can disambiguate. The
// deterministic callers (looksLikeExplicitProjectWorkRequest /
// looksLikeProjectWorkRequest) keep using this fallback on purpose, because
// the smoke-test contract is that an explicit bounded project request still
// dispatches to a plan-mode run.
func isGenericRunFallbackCommand(command []string) bool {
	if len(command) < 2 || command[0] != "run" {
		return false
	}
	// A run command is a generic fallback when it carries task-like
	// arguments (not sub-commands). The LLM classifier returns
	// safe_run for vague input; this check prevents silently
	// dispatching an unqualified task to the full avatar chain.
	// Previously this only caught --permission-mode plan, which
	// missed --permission-mode default and other modes.
	for _, arg := range command[1:] {
		if arg == "--permission-mode" || arg == "--task" || arg == "--new-task" || arg == "--resume" || arg == "--from-file" {
			return true
		}
	}
	return false
}

func looksLikeMutatingCodingRequest(lowered string) bool {
	if looksLikeNegatedReadOnlyOnly(lowered) {
		// Negated "only analyze" is a request to write, not a ban.
	} else if containsAnyIntentToken(lowered, "do not modify", "don't modify", "read-only", "readonly", "不要改", "不改代码", "只读", "只分析") ||
		looksLikeForbiddenMutationAsk(lowered) {
		return false
	}
	if looksLikeTestRepairRequest(lowered) {
		return true
	}
	return containsAnyIntentToken(lowered,
		"write a script",
		"write script",
		"create script",
		"generate script",
		"script",
		"implement",
		"write code",
		"create file",
		"写脚本",
		"写一个脚本",
		"生成脚本",
		"创建脚本",
		"脚本",
		"写代码",
		"生成代码",
		"改代码",
		"修改代码",
		"修复",
		"创建",
		"生成",
		"新建",
		"构建",
		"实现",
		"写",
		"添加",
		"增加",
		"做一个",
		"搞一个",
		"弄一个",
		"加",
		"整",
		"搞",
		"弄",
		"加个",
		"搞个",
		"整个",
		"弄个",
		"create",
		"build",
		"develop",
		"add",
		"generate",
	)
}

func extractBootstrapProjectName(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}
	patterns := []string{
		`(?i)(?:叫|名为|命名为|名字叫)\s*[“"'` + "`" + `]?([a-zA-Z0-9][a-zA-Z0-9._-]{1,63})`,
		`(?i)(?:named|called)\s*[“"'` + "`" + `]?([a-zA-Z0-9][a-zA-Z0-9._-]{1,63})`,
		`(?i)(?:project|app|application|cli)\s+[“"'` + "`" + `]?([a-zA-Z0-9][a-zA-Z0-9._-]{1,63})`,
		`(?i)([a-zA-Z0-9][a-zA-Z0-9._-]{1,63})\s*(?:空项目|项目)`,
		`(?i)([a-zA-Z0-9][a-zA-Z0-9._-]{1,63}-cli)\b`,
		`(?i)(?:--name|-n)\s+([a-zA-Z0-9][a-zA-Z0-9._-]{1,63})`,
	}
	for _, pattern := range patterns {
		match := regexp.MustCompile(pattern).FindStringSubmatch(trimmed)
		if len(match) >= 2 {
			if name := sanitizeBootstrapName(strings.Trim(match[1], "“”\"'`")); name != "" && name != "app" {
				return name
			}
		}
	}
	return ""
}

func hasConcreteBootstrapRequirements(input string) bool {
	lowered := strings.ToLower(strings.TrimSpace(input))
	if lowered == "" {
		return false
	}
	return containsAnyIntentToken(lowered,
		"支持",
		"命令",
		"保存",
		"存储",
		"本地",
		"测试",
		"架构",
		"说明",
		"readme",
		"json",
		"database",
		"storage",
		"commands",
		"tests",
		"architecture",
		"api",
		"ui",
	)
}

func extractBootstrapModule(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}
	patterns := []string{
		`(?i)(?:module|模块|模块名)\s*[=:：]?\s*([a-zA-Z0-9][a-zA-Z0-9._~/-]{1,127})`,
		`(?i)--module\s+([a-zA-Z0-9][a-zA-Z0-9._~/-]{1,127})`,
	}
	for _, pattern := range patterns {
		match := regexp.MustCompile(pattern).FindStringSubmatch(trimmed)
		if len(match) >= 2 {
			return strings.Trim(match[1], "，。；;,. “”。\"'`")
		}
	}
	return ""
}

func extractBootstrapStack(input string) string {
	lowered := strings.ToLower(strings.TrimSpace(input))
	switch {
	case containsAnyIntentToken(lowered, "python", "用python", "用 python", "用 py"):
		return "python-cli"
	case containsAnyIntentToken(lowered, "node", "nodejs", "node.js", "javascript", "用node", "用 node", "用 js"):
		return "node-cli"
	case containsAnyIntentToken(lowered, "rust", "cargo", "用rust", "用 rust"):
		return "rust-cli"
	case containsAnyIntentToken(lowered, "go-cli", "golang", "用go", "用 go"):
		return "go-cli"
	default:
		return ""
	}
}

func looksLikeBootstrapLanguageFollowUp(lowered string) bool {
	if !containsAnyIntentToken(lowered, "python", "用python", "用 python", "用 py", "node", "nodejs", "node.js", "javascript", "用node", "用 node", "用 js", "rust", "cargo", "用rust", "用 rust", "go", "golang", "用go", "用 go") {
		return false
	}
	return latestAnswerLooksLikeBootstrapContext(".")
}

func latestAnswerLooksLikeBootstrapContext(root string) bool {
	if _, ok := loadBootstrapContext(root); ok {
		return true
	}
	answer, _, ok, err := latestRunAnswerContent(root)
	if err != nil || !ok {
		return false
	}
	lowered := strings.ToLower(answer)
	return strings.Contains(lowered, "bootstrap") && (strings.Contains(lowered, "empty project") || strings.Contains(lowered, "空项目") || strings.Contains(lowered, "project named"))
}

func latestBootstrapProjectNameFromAnswer(root string) string {
	if artifact, ok := loadBootstrapContext(root); ok {
		if name := sanitizeBootstrapName(artifact.Name); name != "" {
			return name
		}
	}
	answer, _, ok, err := latestRunAnswerContent(root)
	if err != nil || !ok {
		return ""
	}
	return extractBootstrapProjectName(answer)
}

func looksLikeBootstrapProjectRequest(lowered string) bool {
	if strings.TrimSpace(lowered) == "" {
		return false
	}
	return containsAnyIntentToken(lowered,
		"bootstrap",
		"scaffold",
		"new project",
		"create project",
		"create app",
		"generate project",
		"empty project",
		"搭项目",
		"搭建项目",
		"创建项目",
		"新项目",
		"空项目",
		"写架构",
		"生成代码",
		"生成一个项目",
		"搭一个项目",
	)
}

func currentWorkspaceLooksBootstrapEmpty() bool {
	empty, _, err := bootstrapWorkspaceEmpty(".")
	return err == nil && empty
}

// attachedFileTriggersWorkRequest returns true when the user has attached a file
// via @file and the suffix contains an action verb (执行/按照/run/do/etc),
// indicating they want the file content to be acted upon, not just summarized.
// This fixes the bug where @file content was invisible to intent routing.
func attachedFileTriggersWorkRequest(lowered string) bool {
	if getReplFileContext() == nil {
		return false
	}
	// File must have task-spec-like content (VBA, code, requirements, etc.)
	// or the user explicitly references the file content.
	fileHasTaskContent := looksLikeTaskSpecDocument(getReplFileContext().Content) ||
		strings.Contains(getReplFileContext().Content, "```") ||
		strings.Contains(getReplFileContext().Content, "Sub ") ||
		strings.Contains(getReplFileContext().Content, "Function ") ||
		strings.Contains(getReplFileContext().Content, "def ") ||
		strings.Contains(getReplFileContext().Content, "import ")
	if !fileHasTaskContent {
		return false
	}
	// User suffix must contain an action verb confirming execution intent.
	actionVerbs := []string{
		"执行", "按照", "根据", "运行", "实现", "生成", "创建",
		"run", "execute", "implement", "generate", "create",
		"写一个", "写", "做一个", "做", "搞一个", "搞", "弄一个", "弄",
		"do", "apply", "follow", "按照内容", "按内容", "make", "build",
	}
	for _, verb := range actionVerbs {
		if strings.Contains(lowered, verb) {
			return true
		}
	}
	return false
}

func looksLikeExplicitProjectWorkRequest(lowered string) bool {
	if looksLikeProjectOpinionQuestion(lowered) && !looksLikeOutputArtifactRequest(lowered) {
		return false
	}
	for _, token := range []string{
		"analyze",
		"analyse",
		"analysis",
		"inspect",
		"review",
		"audit",
		"summarize",
		"scan",
		"find",
		"fix",
		"implement",
		"add",
		"support",
		"modify",
		"change",
		"update",
		"write",
		"write script",
		"generate",
		"script",
		"create",
		"run",
		"test",
		"verify",
		"build",
		"report",
		"todo",
		"plan",
		"分析",
		"检查",
		"查看",
		"审查",
		"回审",
		"总结",
		"扫描",
		"查找",
		"找问题",
		"找出",
		"修复",
		"实现",
		"增加",
		"新增",
		"添加",
		"支持",
		"修改",
		"更新",
		"写入",
		"写",
		"脚本",
		"生成",
		"创建",
		"加",
		"整",
		"搞",
		"弄",
		"运行",
		"测试",
		"验证",
		"构建",
		"报告",
		"建立",
		"整理",
		"对比",
		"比较",
		"继续推进",
		"接着做",
		"继续完成",
		"继续完善",
		"接着完善",
		"继续开发",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func looksLikeProjectOpinionQuestion(lowered string) bool {
	projectSignal := strings.Contains(lowered, "project") ||
		strings.Contains(lowered, "repo") ||
		strings.Contains(lowered, "repository") ||
		strings.Contains(lowered, "codebase") ||
		strings.Contains(lowered, "项目") ||
		strings.Contains(lowered, "仓库") ||
		strings.Contains(lowered, "代码")
	if !projectSignal {
		return false
	}
	for _, token := range []string{
		"what do you think",
		"your opinion",
		"how is this",
		"how do you feel",
		"worthwhile",
		"promising",
		"good",
		"bad",
		"评价",
		"看法",
		"怎么看",
		"认为",
		"觉得",
		"觉得如何",
		"的如何",
		"怎么样",
		"好不好",
		"有没有价值",
		"靠谱吗",
		"成熟吗",
		"前景",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func looksLikeProjectStatusQuestion(lowered string) bool {
	projectSignal := strings.Contains(lowered, "project") ||
		strings.Contains(lowered, "repo") ||
		strings.Contains(lowered, "repository") ||
		strings.Contains(lowered, "codebase") ||
		strings.Contains(lowered, "项目") ||
		strings.Contains(lowered, "仓库") ||
		strings.Contains(lowered, "代码")
	if !projectSignal {
		return false
	}
	for _, token := range []string{
		"status",
		"state",
		"progress",
		"current",
		"latest",
		"recent",
		"now",
		"currently",
		"状态",
		"现状",
		"进展",
		"当前",
		"最新",
		"最近",
		"现在",
		"情况",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func looksLikeTaskMemoryFollowUpQuestion(lowered string) bool {
	if looksLikeProjectOpinionQuestion(lowered) {
		return true
	}
	if looksLikeAnalysisReportQualityQuestion(lowered) {
		return true
	}
	return looksLikeProjectStatusQuestion(lowered)
}

func buildLatestVerifierFailureRepairCommand(input string, context naturalLanguageContext) ([]string, string, bool) {
	if context != naturalLanguageContextREPL && context != naturalLanguageContextIntent {
		return nil, "", false
	}
	lowered := strings.ToLower(strings.TrimSpace(input))
	if !looksLikeVerifierFailureRepairRequest(lowered) {
		return nil, "", false
	}
	report, reportPath, ok := latestAnalysisReportContent(".")
	if !ok {
		return nil, "", false
	}
	target := latestVerifierFailureRepairTarget(report)
	if target == "" {
		return nil, "", false
	}
	instruction := strings.TrimSpace(input)
	contextExcerpt := latestVerifierFailureContextExcerpt(report)
	if contextExcerpt != "" {
		instruction += "\n\nLatest verifier failure context from " + filepath.ToSlash(reportPath) + ":\n" + contextExcerpt
	}
	return []string{"edit", "--apply", filepath.ToSlash(target), instruction}, "repair latest focused verifier failure from task memory", true
}

func looksLikeVerifierFailureRepairRequest(lowered string) bool {
	repairSignal := containsAnyIntentToken(lowered, "fix", "repair", "resolve", "修改", "修复", "解决", "改")
	failureSignal := containsAnyIntentToken(lowered, "verifier", "verification", "test", "failure", "failed", "失败", "报错", "测试")
	recencySignal := containsAnyIntentToken(lowered, "latest", "previous", "last", "刚才", "刚刚", "上次", "上一轮")
	return repairSignal && failureSignal && recencySignal
}

func latestAnalysisReportContent(root string) (string, string, bool) {
	workspace, ok, err := latestReportedTaskWorkspace(root)
	if err == nil && ok {
		reportPath := latestAnalysisReportPath(workspace)
		if strings.TrimSpace(reportPath) != "" {
			resolved := resolveAnalysisReportReadPath(root, reportPath)
			content, err := os.ReadFile(resolved)
			if err == nil {
				return string(content), reportPath, true
			}
		}
	}
	return latestRootAnalysisReportContent(root)
}

func latestRootAnalysisReportContent(root string) (string, string, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", "", false
	}
	type candidate struct {
		path    string
		content string
		modTime int64
	}
	candidates := []candidate{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		if isReservedProjectMarkdownName(name) {
			continue
		}
		path := filepath.Join(root, name)
		content, err := os.ReadFile(path)
		if err != nil || !looksLikeAvatarsAnalysisReportContent(string(content)) {
			continue
		}
		info, err := entry.Info()
		var modTime int64
		if err == nil {
			modTime = info.ModTime().UnixNano()
		}
		candidates = append(candidates, candidate{path: name, content: string(content), modTime: modTime})
	}
	if len(candidates) == 0 {
		return "", "", false
	}
	sort.Slice(candidates, func(i int, j int) bool {
		if candidates[i].modTime == candidates[j].modTime {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].modTime > candidates[j].modTime
	})
	return candidates[0].content, candidates[0].path, true
}

func isReservedProjectMarkdownName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "readme.md", "coding_plan.md", "process_record.md", "skills.md", "cli_guide.md":
		return true
	default:
		return false
	}
}

func looksLikeAvatarsAnalysisReportContent(content string) bool {
	lowered := strings.ToLower(content)
	if !(strings.Contains(content, "## Evidence Reviewed") || strings.Contains(content, "## Evidence Coverage") || strings.Contains(content, "## Proven Findings")) {
		return false
	}
	return strings.Contains(content, "## Verification Results") ||
		strings.Contains(content, "## Candidate Risks With Evidence") ||
		strings.Contains(content, "## Evidence-Tied Risks") ||
		strings.Contains(lowered, "focused verifier failed") ||
		strings.Contains(lowered, "verification finished with")
}

func latestVerifierFailureRepairTarget(report string) string {
	if target := targetFromFocusedVerifierTestName(report); target != "" {
		return target
	}
	if match := regexp.MustCompile("(?i)Source target:\\s*`([^`]+)`").FindStringSubmatch(report); len(match) == 2 {
		target := strings.TrimSpace(match[1])
		if paren := strings.Index(target, " ("); paren >= 0 {
			target = strings.TrimSpace(target[:paren])
		}
		if looksLikePatchableRelativePath(target) {
			return filepath.Clean(filepath.FromSlash(target))
		}
	}
	if match := regexp.MustCompile("(?i)(internal[/\\\\][^\\s`]+\\.go)").FindStringSubmatch(report); len(match) == 2 {
		target := strings.TrimSpace(match[1])
		if looksLikePatchableRelativePath(target) {
			return filepath.Clean(filepath.FromSlash(target))
		}
	}
	return ""
}

func targetFromFocusedVerifierTestName(report string) string {
	match := regexp.MustCompile(`TestRunner_Run_([A-Za-z0-9]+)`).FindStringSubmatch(report)
	if len(match) != 2 {
		return ""
	}
	base := strings.TrimSuffix(match[1], "Task")
	snake := camelToSnake(base)
	if snake == "" {
		return ""
	}
	for _, candidate := range []string{
		filepath.Join("internal", "auto", snake+".go"),
		filepath.Join("internal", "auto", snake+"_task.go"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return filepath.Clean(candidate)
		}
	}
	return ""
}

func camelToSnake(value string) string {
	var builder strings.Builder
	for index, r := range value {
		if r >= 'A' && r <= 'Z' {
			if index > 0 {
				builder.WriteByte('_')
			}
			builder.WriteRune(r + ('a' - 'A'))
			continue
		}
		builder.WriteRune(r)
	}
	return strings.Trim(builder.String(), "_")
}

func latestVerifierFailureContextExcerpt(report string) string {
	lines := []string{}
	capturing := false
	for _, line := range strings.Split(report, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## Proven Findings") || strings.HasPrefix(trimmed, "## Verification Results") || strings.HasPrefix(trimmed, "## Remediation Leads") {
			capturing = true
			continue
		}
		if capturing && strings.HasPrefix(trimmed, "## ") {
			capturing = false
		}
		if !capturing || trimmed == "" {
			continue
		}
		if strings.Contains(strings.ToLower(trimmed), "fail") ||
			strings.Contains(trimmed, "失败") ||
			strings.Contains(trimmed, "expected") ||
			strings.Contains(trimmed, "Source target") ||
			strings.Contains(trimmed, "TestRunner") ||
			strings.Contains(trimmed, "go test") {
			lines = append(lines, trimmed)
		}
		if len(lines) >= 8 {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func looksLikeAnalysisReportQualityQuestion(lowered string) bool {
	reportSignal := strings.Contains(lowered, "analysis report") ||
		strings.Contains(lowered, "report") ||
		strings.Contains(lowered, "分析报告") ||
		strings.Contains(lowered, "报告") ||
		strings.Contains(lowered, "分析") ||
		strings.Contains(lowered, "结论")
	if !reportSignal {
		return false
	}
	qualitySignal := strings.Contains(lowered, "shallow") ||
		strings.Contains(lowered, "superficial") ||
		strings.Contains(lowered, "quality") ||
		strings.Contains(lowered, "credible") ||
		strings.Contains(lowered, "reliable") ||
		strings.Contains(lowered, "肤浅") ||
		strings.Contains(lowered, "浅") ||
		strings.Contains(lowered, "质量") ||
		strings.Contains(lowered, "深度") ||
		strings.Contains(lowered, "靠谱吗") ||
		strings.Contains(lowered, "可信") ||
		strings.Contains(lowered, "证据") ||
		strings.Contains(lowered, "是否")
	return qualitySignal
}

func looksLikeProjectOverviewQuestion(lowered string) bool {
	if looksLikeExplicitProjectWorkRequest(lowered) {
		return false
	}
	projectSignal := strings.Contains(lowered, "project") ||
		strings.Contains(lowered, "repo") ||
		strings.Contains(lowered, "repository") ||
		strings.Contains(lowered, "项目") ||
		strings.Contains(lowered, "仓库")
	overviewSignal := strings.Contains(lowered, "what does") ||
		strings.Contains(lowered, "what is") ||
		strings.Contains(lowered, "overview") ||
		strings.Contains(lowered, "purpose") ||
		strings.Contains(lowered, "干啥") ||
		strings.Contains(lowered, "干嘛") ||
		strings.Contains(lowered, "干什么") ||
		strings.Contains(lowered, "是什么") ||
		strings.Contains(lowered, "用途") ||
		strings.Contains(lowered, "概览") ||
		strings.Contains(lowered, "概述")
	return projectSignal && overviewSignal
}

func looksLikeModelIdentityQuestion(lowered string) bool {
	modelSignal := strings.Contains(lowered, "model") ||
		strings.Contains(lowered, "llm") ||
		strings.Contains(lowered, "gpt") ||
		strings.Contains(lowered, "chatgpt") ||
		strings.Contains(lowered, "modeling") ||
		strings.Contains(lowered, "大模型") ||
		strings.Contains(lowered, "模型") ||
		strings.Contains(lowered, "是什么模型")
	identitySignal := strings.Contains(lowered, "what are you") ||
		strings.Contains(lowered, "are you gpt") ||
		strings.Contains(lowered, "are you chatgpt") ||
		strings.Contains(lowered, "are you an llm") ||
		strings.Contains(lowered, "are you a model") ||
		strings.Contains(lowered, "who are you") ||
		strings.Contains(lowered, "which model") ||
		strings.Contains(lowered, "你是啥") ||
		strings.Contains(lowered, "你是什么") ||
		strings.Contains(lowered, "你是谁") ||
		strings.Contains(lowered, "你是gpt") ||
		strings.Contains(lowered, "你是chatgpt") ||
		strings.Contains(lowered, "你是什么大模型") ||
		strings.Contains(lowered, "你是哪个模型") ||
		strings.Contains(lowered, "当前模型") ||
		strings.Contains(lowered, "现在用的模型") ||
		strings.Contains(lowered, "当前llm")
	return modelSignal && identitySignal
}

func looksLikeProjectConversationalEvaluationQuestion(lowered string) bool {
	return looksLikeProjectOpinionQuestion(lowered) && !looksLikeOutputArtifactRequest(lowered)
}

func looksLikeOutputArtifactRequest(lowered string) bool {
	// Only match file extensions when co-occurring with an output verb.
	// Plain mentions like "README.md在哪" should not trigger this.
	hasOutputVerb := strings.Contains(lowered, "写入") || strings.Contains(lowered, "输出") ||
		strings.Contains(lowered, "保存") || strings.Contains(lowered, "write") ||
		strings.Contains(lowered, "output") || strings.Contains(lowered, "save")
	if hasOutputVerb && (strings.Contains(lowered, ".md") || strings.Contains(lowered, ".txt") || strings.Contains(lowered, ".json")) {
		return true
	}
	for _, token := range []string{
		"write to",
		"save to",
		"output to",
		"summarize in",
		"report to",
		"写入",
		"输出到",
		"保存到",
		"总结写入",
		"结果写入",
		"写到",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func looksLikeResultWriteFollowUpQuestion(lowered string) bool {
	hasPreviousSignal := false
	for _, token := range []string{
		"刚才",
		"上次",
		"上一步",
		"上一条",
		"上述",
		"上面",
		"刚刚",
		"previous",
		"last",
		"prior",
		"earlier",
	} {
		if strings.Contains(lowered, token) {
			hasPreviousSignal = true
			break
		}
	}
	if !hasPreviousSignal {
		return false
	}
	for _, token := range []string{
		"结果",
		"报告",
		"analysis",
		"outcome",
		"result",
		"写入",
		"保存",
		"output",
		"生成结果",
		"copy",
		"write",
		"save",
		"answer",
		"回答",
		"回复",
		"显示不完整",
		"完整",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func looksLikePreviousAnswerWriteFollowUpQuestion(lowered string) bool {
	if !looksLikeResultWriteFollowUpQuestion(lowered) {
		return false
	}
	for _, token := range []string{
		"answer",
		"回答",
		"回复",
		"上述",
		"上面",
		"上一条",
		"显示不完整",
		"没显示完整",
		"完整答案",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func requestedMarkdownOutputPath(input string) (string, bool) {
	for _, field := range strings.Fields(strings.TrimSpace(input)) {
		candidate := strings.Trim(field, " \t\r\n`'\"，,。.;:：；()（）[]【】")
		if cleaned, ok := cleanRequestedMarkdownOutputCandidate(candidate); ok {
			return cleaned, true
		}
	}
	for _, candidate := range regexp.MustCompile(`(?i)[A-Za-z0-9_.\-/\\]+\.md`).FindAllString(input, -1) {
		if cleaned, ok := cleanRequestedMarkdownOutputCandidate(candidate); ok {
			return cleaned, true
		}
	}
	return "", false
}

func cleanRequestedMarkdownOutputCandidate(candidate string) (string, bool) {
	candidate = strings.Trim(candidate, " \t\r\n`'\"，,。.;:：；()（）[]【】")
	lowered := strings.ToLower(candidate)
	if !strings.HasSuffix(lowered, ".md") {
		if match := regexp.MustCompile(`(?i)[A-Za-z0-9_.\-/\\]+\.md`).FindString(candidate); match != "" {
			candidate = match
			lowered = strings.ToLower(candidate)
		}
	}
	if !strings.HasSuffix(lowered, ".md") {
		return "", false
	}
	cleaned := filepath.Clean(filepath.FromSlash(candidate))
	if filepath.IsAbs(cleaned) || cleaned == "." || strings.HasPrefix(cleaned, "..") {
		return "", false
	}
	return cleaned, true
}

func looksLikeResultLocationQuestion(lowered string) bool {
	if looksLikeExplicitProjectWorkRequest(lowered) {
		return false
	}
	resultSignal := strings.Contains(lowered, "result") ||
		strings.Contains(lowered, "output") ||
		strings.Contains(lowered, "report") ||
		strings.Contains(lowered, "结果") ||
		strings.Contains(lowered, "输出") ||
		strings.Contains(lowered, "报告") ||
		strings.Contains(lowered, "成果")
	locationSignal := strings.Contains(lowered, "where") ||
		strings.Contains(lowered, "path") ||
		strings.Contains(lowered, "file") ||
		strings.Contains(lowered, "在哪") ||
		strings.Contains(lowered, "哪里") ||
		strings.Contains(lowered, "哪呢") ||
		strings.Contains(lowered, "文件") ||
		strings.Contains(lowered, "在那") ||
		strings.Contains(lowered, "呢")
	return resultSignal && locationSignal
}

func looksLikeNaturalLanguageDestructiveIntent(lowered string) bool {
	if looksLikeFeatureImplementationWithDestructiveVerb(lowered) {
		return false
	}
	for _, token := range []string{
		"delete",
		"remove",
		"drop",
		"erase",
		"wipe",
		"destroy",
		"purge",
		"format",
		"uninstall",
		"truncate",
		"删除",
		"移除",
		"擦除",
		"销毁",
		"清空",
		"格式化",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func looksLikeFeatureImplementationWithDestructiveVerb(lowered string) bool {
	hasDestructiveVerb := containsAnyIntentToken(lowered, "delete", "remove", "drop", "删除", "移除")
	if !hasDestructiveVerb {
		return false
	}
	featureSignal := containsAnyIntentToken(lowered,
		"command", "subcommand", "cli", "feature", "function", "endpoint", "handler",
		"implement", "add", "support", "build", "create",
		"命令", "子命令", "功能", "函数", "接口", "实现", "增加", "新增", "支持",
	)
	if !featureSignal {
		return false
	}
	directDestructiveObject := containsAnyIntentToken(lowered,
		"all files", "all data", "entire project", "workspace", "repository", "repo",
		"directory", "folder", "disk", "database", "table",
		"所有文件", "全部文件", "所有数据", "整个项目", "工作区", "代码库", "目录", "文件夹", "数据库", "数据表", "清空",
	)
	return !directDestructiveObject
}

func summarizeProjectOverview(root string) (string, error) {
	readmePath, readmeContent := firstExistingTextFile(root, projectfiles.ReadmeCandidates())
	title := markdownTitle(readmeContent)
	if title == "" {
		title = "unknown"
	}
	purpose := firstMeaningfulMarkdownLine(readmeContent)
	if purpose == "" {
		purpose = "Project purpose was not found in README."
	}
	stack := projectStackSignals(root)
	parts := []string{
		"Project overview:",
		"- README: " + readmePath,
		"- Title: " + title,
		"- Purpose: " + purpose,
	}
	if len(stack) == 0 {
		parts = append(parts, "- Stack signals: none found")
	} else {
		parts = append(parts, "- Stack signals: "+strings.Join(stack, ", "))
	}
	return strings.Join(parts, "\n"), nil
}

func summarizeModelIdentity(root string) (string, error) {
	application, err := app.BootstrapWithOptions(app.BootstrapOptions{AllowInvalidLLMConfig: true})
	if err != nil {
		return "", err
	}
	defer func() {
		_ = application.Close()
	}()
	status := application.LLMStatus
	lines := []string{"Model identity:"}
	lines = append(lines, fmt.Sprintf("- Provider: %s", displayRuntimeField(status.Name)))
	lines = append(lines, fmt.Sprintf("- Family: %s", displayRuntimeField(status.Family)))
	lines = append(lines, fmt.Sprintf("- Model: %s", displayRuntimeField(status.Model)))
	if baseURL := strings.TrimSpace(status.BaseURL); baseURL != "" {
		lines = append(lines, "- Base URL: "+baseURL)
	}
	if apiKeyEnv := strings.TrimSpace(status.APIKeyEnv); apiKeyEnv != "" {
		lines = append(lines, "- API key env: "+apiKeyEnv)
	}
	lines = append(lines, fmt.Sprintf("- Web search enabled: %s", yesNo(status.WebSearchEnabled)))
	if status.ThinkMode != "" {
		lines = append(lines, "- Think mode: "+string(status.ThinkMode))
	}
	return strings.Join(lines, "\n"), nil
}

func summarizeProjectConversationalEvaluation(root string) (string, error) {
	overview, err := summarizeProjectOverview(root)
	if err != nil {
		return "", err
	}
	_, readmeContent := firstExistingTextFile(root, projectfiles.ReadmeCandidates())
	docsCount := countExistingPaths(root, []string{projectfiles.DocsDir, projectfiles.CLIGuide})
	stack := projectStackSignals(root)
	assessment := "Project opinion:\n"
	switch {
	case strings.TrimSpace(readmeContent) == "":
		assessment += "- Initial read: hard to judge because README evidence is missing.\n"
	case len(stack) >= 2 && docsCount >= 2:
		assessment += "- Initial read: promising and fairly structured; docs plus stack markers suggest an intentional tool, not a loose script.\n"
	case len(stack) > 0:
		assessment += "- Initial read: viable, but confidence is limited until deeper source and test evidence is reviewed.\n"
	default:
		assessment += "- Initial read: too little local evidence for a strong opinion.\n"
	}
	assessment += "- Confidence: lightweight answer from local metadata only; use `analyze this repository and find concrete issues` for a full run.\n"
	return assessment + "\n" + overview, nil
}

func looksLikeProjectCodeLocationQuestion(lowered string) bool {
	// F81: this matcher is only for "where do new auto-script tasks live?"
	// Bare 自动 / 实现 / 文件在哪 steal greenfield library requests
	// ("自动记成功", "实现熔断器", "做完告诉我文件在哪") before the LLM router.
	if looksLikeGreenfieldCreateRequest(lowered) {
		return false
	}
	locationSignal := containsAnyIntentToken(lowered, "在哪", "哪里", "哪个包", "哪个目录", "哪个文件", "which package", "which directory", "which folder")
	codeSignal := containsAnyIntentToken(lowered, ".go", "go代码", "go 代码")
	autoTaskSignal := containsAnyIntentToken(lowered, "自动脚本", "自动任务", "vba", "automatic script", "automatic task")
	return locationSignal && codeSignal && autoTaskSignal
}

func looksLikeGreenfieldCreateRequest(lowered string) bool {
	create := containsAnyIntentToken(lowered, "从零", "新建", "做一个", "写一个", "create a", "build a", "from scratch")
	artifact := containsAnyIntentToken(lowered, "开源库", "库", "library", "crate", "package", "项目", "project")
	return create && artifact
}

func summarizeProjectCodeLocation(root string) (string, error) {
	readmePath, readmeContent := firstExistingTextFile(root, projectfiles.ReadmeCandidates())
	if !strings.Contains(readmeContent, "internal/auto") {
		return "Code location:\n- I do not have enough local README evidence to name the package confidently.\n- Run a deeper source/docs analysis or point me at the task runner docs.", nil
	}
	lines := []string{
		"Code location:",
		"- New automatic script task `.go` files should go under `internal/auto/`.",
		"- Evidence: " + filepath.ToSlash(readmePath) + " says `internal/auto/` is the automatic task runner and uses one Go file per VBA migration task.",
		"- Usual next step: follow `docs/phases/auto_mode_task_usage.md` for task interface/registration, then keep reusable helpers in `manual`, `plugin`, `template`, or `output` packages.",
	}
	return strings.Join(lines, "\n"), nil
}

func looksLikeProjectManifestQuestion(lowered string) bool {
	// P5-1: Depth guard. When the user asks for detailed dependency
	// analysis (用途/作用/解释/分析/版本), they want pipeline analysis,
	// not a quick manifest summary. The manifest handler only answers
	// simple questions like "what config files exist?"
	deepAnalysisSignal := strings.Contains(lowered, "分析") ||
		strings.Contains(lowered, "用途") ||
		strings.Contains(lowered, "作用") ||
		strings.Contains(lowered, "解释") ||
		strings.Contains(lowered, "详细") ||
		strings.Contains(lowered, "analyze") ||
		strings.Contains(lowered, "explain") ||
		strings.Contains(lowered, "purpose") ||
		strings.Contains(lowered, "role") ||
		strings.Contains(lowered, "版本") ||
		strings.Contains(lowered, "version") ||
		strings.Contains(lowered, "过时") ||
		strings.Contains(lowered, "outdated") ||
		strings.Contains(lowered, "更新") ||
		strings.Contains(lowered, "update")
	if deepAnalysisSignal {
		return false
	}
	manifestSignal := strings.Contains(lowered, "manifest") ||
		strings.Contains(lowered, "dependency") ||
		strings.Contains(lowered, "deps") ||
		strings.Contains(lowered, "go.mod") ||
		strings.Contains(lowered, "package.json") ||
		strings.Contains(lowered, "配置")
	projectSignal := strings.Contains(lowered, "project") ||
		strings.Contains(lowered, "repo") ||
		strings.Contains(lowered, "repository") ||
		strings.Contains(lowered, "项目")
	return manifestSignal && projectSignal
}

func countExistingPaths(root string, paths []string) int {
	count := 0
	for _, path := range paths {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err == nil {
			count++
		}
	}
	return count
}

func looksLikeDocsOverviewQuestion(lowered string) bool {
	docsSignal := strings.Contains(lowered, "docs") ||
		strings.Contains(lowered, "documentation") ||
		strings.Contains(lowered, "文档") ||
		strings.Contains(lowered, "说明")
	overviewSignal := strings.Contains(lowered, "overview") ||
		strings.Contains(lowered, "map") ||
		strings.Contains(lowered, "reading order") ||
		strings.Contains(lowered, "怎么读") ||
		strings.Contains(lowered, "读哪些") ||
		strings.Contains(lowered, "概览") ||
		strings.Contains(lowered, "总结")
	return docsSignal && overviewSignal
}

func looksLikeRecentTaskQuestion(lowered string) bool {
	taskSignal := strings.Contains(lowered, "task") ||
		strings.Contains(lowered, "tasks") ||
		strings.Contains(lowered, "任务")
	recentSignal := strings.Contains(lowered, "recent") ||
		strings.Contains(lowered, "latest") ||
		strings.Contains(lowered, "last") ||
		strings.Contains(lowered, "最近") ||
		strings.Contains(lowered, "最新")
	summarySignal := strings.Contains(lowered, "summary") ||
		strings.Contains(lowered, "summarize") ||
		strings.Contains(lowered, "状态") ||
		strings.Contains(lowered, "总结") ||
		strings.Contains(lowered, "进展")
	return taskSignal && recentSignal && summarySignal
}

func summarizeProjectManifests(root string) (string, error) {
	manifests := []string{
		"go.mod",
		"go.sum",
		"package.json",
		"package-lock.json",
		"pnpm-lock.yaml",
		"pyproject.toml",
		"Cargo.toml",
		"requirements.txt",
	}
	found := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		if _, err := os.Stat(filepath.Join(root, manifest)); err == nil {
			found = append(found, manifest)
		}
	}
	parts := []string{"Project manifest summary:"}
	if len(found) == 0 {
		parts = append(parts, "- Manifests: none found")
	} else {
		parts = append(parts, "- Manifests: "+strings.Join(found, ", "))
	}
	if goModSummary, err := summarizeGoMod(filepath.Join(root, "go.mod")); err == nil && goModSummary != "" {
		parts = append(parts, "- Go module: "+goModSummary)
	}
	if packageSummary, err := summarizePackageJSON(filepath.Join(root, "package.json")); err == nil && packageSummary != "" {
		parts = append(parts, "- Node package: "+packageSummary)
	}
	return strings.Join(parts, "\n"), nil
}

func summarizeDocsOverview(root string) (string, error) {
	docsDir := filepath.Join(root, projectfiles.DocsDir)
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		return "Docs overview:\n- docs/: not found", nil
	}
	files := make([]string, 0, len(entries))
	indexPath := ""
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".md" {
			continue
		}
		relative := filepath.ToSlash(filepath.Join(projectfiles.DocsDir, entry.Name()))
		files = append(files, relative)
		if indexPath == "" && isDocsIndexName(entry.Name()) {
			indexPath = filepath.Join(docsDir, entry.Name())
		}
	}
	sort.Strings(files)
	parts := []string{
		"Docs overview:",
	}
	if indexPath != "" {
		content, err := os.ReadFile(indexPath)
		if err == nil {
			title := markdownTitle(string(content))
			if title != "" {
				parts = append(parts, "- Index: "+filepath.ToSlash(indexPath)+" | Title: "+title)
			} else {
				parts = append(parts, "- Index: "+filepath.ToSlash(indexPath))
			}
		}
	}
	if len(files) == 0 {
		parts = append(parts, "- Docs files: none found")
	} else {
		parts = append(parts, "- Docs files: "+strings.Join(files, ", "))
	}
	return strings.Join(parts, "\n"), nil
}

func isDocsIndexName(name string) bool {
	for _, candidate := range projectfiles.DocsIndexNames() {
		if strings.EqualFold(name, candidate) {
			return true
		}
	}
	return false
}

func summarizeRecentTasks(root string) (string, error) {
	manager := tasks.NewManager(filepath.Join(root, ".avatars", "tasks"))
	workspaces, err := manager.List()
	if err != nil {
		if os.IsNotExist(err) {
			return "Recent tasks:\n- none found", nil
		}
		return "", err
	}
	if len(workspaces) == 0 {
		return "Recent tasks:\n- none found", nil
	}
	limit := 5
	if len(workspaces) < limit {
		limit = len(workspaces)
	}
	lines := []string{"Recent tasks:"}
	for index := 0; index < limit; index++ {
		workspace := workspaces[index]
		summary := strings.TrimSpace(workspace.LatestSummary)
		if summary == "" {
			summary = "no summary"
		}
		if len(summary) > 180 {
			summary = strings.TrimSpace(summary[:180]) + "..."
		}
		lines = append(lines, fmt.Sprintf("- %s [%s, runs=%d]: %s", workspace.ID, workspace.Status, workspace.RunCount, summary))
	}
	return strings.Join(lines, "\n"), nil
}

func latestTaskWorkspace(root string) (tasks.Workspace, bool, error) {
	manager := tasks.NewManager(filepath.Join(root, ".avatars", "tasks"))
	workspaces, err := manager.List()
	if err != nil {
		if os.IsNotExist(err) {
			return tasks.Workspace{}, false, nil
		}
		return tasks.Workspace{}, false, err
	}
	workspace := latestResultWorkspace(workspaces)
	if workspace.ID == "" {
		return tasks.Workspace{}, false, nil
	}
	return workspace, true, nil
}

func summarizeResultLocation(root string) (string, error) {
	if artifact, ok, err := loadREPLLastTurn(root); err != nil {
		return "", err
	} else if ok {
		lines := []string{"Result location:"}
		if artifact.Kind != "" {
			lines = append(lines, "- last action: "+artifact.Kind)
		}
		if artifact.Status != "" {
			lines = append(lines, "- status: "+artifact.Status)
		}
		for _, path := range artifact.Artifacts {
			if strings.TrimSpace(path) != "" {
				lines = append(lines, "- artifact: "+filepath.ToSlash(path))
			}
		}
		if len(artifact.Command) > 0 {
			lines = append(lines, "- command: avatars "+strings.Join(artifact.Command, " "))
		}
		if len(lines) > 1 {
			return strings.Join(lines, "\n"), nil
		}
	}
	workspace, ok, err := latestTaskWorkspace(root)
	if err != nil {
		return "", err
	}
	if !ok {
		return "Result location:\n- no task result found yet", nil
	}
	lines := []string{"Result location:"}
	if transcriptPath := strings.TrimSpace(workspace.LatestTranscriptPath()); transcriptPath != "" {
		lines = append(lines, "- transcript: "+filepath.ToSlash(transcriptPath))
	}
	if reportPath := latestAnalysisReportPath(workspace); reportPath != "" {
		lines = append(lines, "- analysis report: "+filepath.ToSlash(reportPath))
	}
	if summary := strings.TrimSpace(workspace.LatestSummary); summary != "" {
		lines = append(lines, "- summary: "+conciseNLContextLine(summary, 300))
	}
	lines = append(lines, "- latest task: "+workspace.ID)
	return strings.Join(lines, "\n"), nil
}

func summarizeLatestRunAnswer(root string) (string, bool, error) {
	answer, workspace, ok, err := latestRunAnswerContent(root)
	if err != nil || !ok {
		return "", false, err
	}
	lines := []string{"Answer:", compactAnswerPreviewMarkdown(answer, 1600)}
	if artifactPath, err := persistLatestAnswerArtifact(root, workspace, answer); err == nil && artifactPath != "" {
		lines = append(lines, "Full answer: "+filepath.ToSlash(artifactPath))
	}
	return strings.Join(lines, "\n"), true, nil
}

func latestRunAnswerContent(root string) (string, tasks.Workspace, bool, error) {
	workspace, ok, err := latestTaskWorkspace(root)
	if err != nil || !ok {
		return "", tasks.Workspace{}, false, err
	}
	transcriptPath := strings.TrimSpace(workspace.LatestTranscriptPath())
	if transcriptPath == "" {
		return "", workspace, false, nil
	}
	content, err := os.ReadFile(transcriptPath)
	if err != nil {
		return "", workspace, false, err
	}
	lines := strings.Split(string(content), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		answer := latestRunAnswerFromTranscriptLine(lines[index])
		if answer == "" {
			continue
		}
		return answer, workspace, true, nil
	}
	return "", workspace, false, nil
}

func persistLatestAnswerArtifact(root string, workspace tasks.Workspace, answer string) (string, error) {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return "", nil
	}
	path := filepath.Join(root, "answer.md")
	// Engine already rewrote answer.md to match on-disk paths / final gates.
	// Do not clobber that with the raw Synthesizer transcript blob.
	if existing, err := os.ReadFile(path); err == nil {
		if strings.Contains(string(existing), "final-delivery path") {
			if rel, relErr := filepath.Rel(root, path); relErr == nil && rel != "" && !strings.HasPrefix(rel, "..") {
				return rel, nil
			}
			return path, nil
		}
	}
	if err := os.WriteFile(path, []byte(trimmed), 0o644); err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(root, path); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		return rel, nil
	}
	return path, nil
}

func latestRunAnswerFromTranscriptLine(line string) string {
	if strings.TrimSpace(line) == "" {
		return ""
	}
	var envelope struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		return ""
	}
	if envelope.Type != "llm.completed" {
		return ""
	}
	return strings.TrimSpace(payloadStringValue(envelope.Payload, "content"))
}

func summarizeLatestAnalysisReportQuality(root string) (string, error) {
	manager := tasks.NewManager(filepath.Join(root, ".avatars", "tasks"))
	workspaces, err := manager.List()
	if err != nil {
		if os.IsNotExist(err) {
			return "Analysis report quality:\n- verdict: no latest analysis report found", nil
		}
		return "", err
	}
	workspace := latestReportedTaskWorkspaceFromList(workspaces)
	if workspace.ID == "" {
		return "Analysis report quality:\n- verdict: no latest analysis report found", nil
	}
	reportPath := latestAnalysisReportPath(workspace)
	if strings.TrimSpace(reportPath) == "" {
		return "Analysis report quality:\n- verdict: no latest analysis report found", nil
	}
	resolvedPath := resolveAnalysisReportReadPath(root, reportPath)
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return fmt.Sprintf("Analysis report quality:\n- verdict: report path found, but file cannot be read\n- report: %s\n- error: %v", filepath.ToSlash(reportPath), err), nil
	}
	return assessAnalysisReportQuality(reportPath, string(content)), nil
}

func resolveAnalysisReportReadPath(root string, reportPath string) string {
	trimmed := strings.TrimSpace(reportPath)
	if filepath.IsAbs(trimmed) {
		return trimmed
	}
	return filepath.Join(root, filepath.FromSlash(trimmed))
}

func assessAnalysisReportQuality(reportPath string, content string) string {
	lowered := strings.ToLower(content)
	reasons := []string{}
	if !strings.Contains(content, "## Proven Findings") {
		reasons = append(reasons, "missing `Proven Findings`; findings are not separated from risks")
	}
	if !strings.Contains(content, "## Evidence-Tied Risks") {
		reasons = append(reasons, "missing `Evidence-Tied Risks`; unverified concerns can read like proven bugs")
	}
	if !strings.Contains(content, "## Open Questions") {
		reasons = append(reasons, "missing `Open Questions`; gaps are not explicitly tracked")
	}
	if strings.Contains(content, "## Issues") && !strings.Contains(content, "## Proven Findings") {
		reasons = append(reasons, "`Issues` section is too broad for evidence-grade review")
	}
	if strings.Contains(lowered, "individual source files were not displayed") ||
		strings.Contains(lowered, "relies on the aggregated summaries") ||
		strings.Contains(lowered, "no test evidence") ||
		strings.Contains(lowered, "no ci configuration") {
		reasons = append(reasons, "report admits limited evidence, but still makes broad claims")
	}
	if strings.Contains(lowered, "latest stable") ||
		strings.Contains(lowered, "does not exist") ||
		strings.Contains(lowered, "outdated go version") {
		reasons = append(reasons, "contains time-sensitive/toolchain claims that require fresh evidence")
	}
	if !hasReportEvidenceAnchor(content) {
		reasons = append(reasons, "few concrete evidence anchors such as files, functions, or command outputs")
	}
	verdict := "usable but needs evidence-proof pass"
	if len(reasons) >= 3 {
		verdict = "shallow"
	}
	if len(reasons) == 0 {
		verdict = "acceptable"
		reasons = append(reasons, "report uses the expected evidence sections")
	}
	lines := []string{
		"Analysis report quality:",
		"- verdict: " + verdict,
		"- report: " + filepath.ToSlash(reportPath),
	}
	for _, reason := range reasons {
		lines = append(lines, "- reason: "+reason)
	}
	if verdict != "acceptable" {
		lines = append(lines, "- next: run an issue-proof pass; promote only findings with evidence anchors, demote the rest to risks/open questions")
	}
	return strings.Join(lines, "\n")
}

func hasReportEvidenceAnchor(content string) bool {
	for _, token := range []string{
		".go",
		".md",
		"go.mod",
		"package.json",
		"internal/",
		"cmd/",
		"frontend/",
		"func ",
		"go test",
		"Reading:",
		"Read ",
	} {
		if strings.Contains(content, token) {
			return true
		}
	}
	return false
}

func latestResultWorkspace(workspaces []tasks.Workspace) tasks.Workspace {
	if len(workspaces) == 0 {
		return tasks.Workspace{}
	}
	if workspace := latestReportedTaskWorkspaceFromList(workspaces); workspace.ID != "" {
		return workspace
	}
	return workspaces[0]
}

func latestReportedTaskWorkspace(root string) (tasks.Workspace, bool, error) {
	manager := tasks.NewManager(filepath.Join(root, ".avatars", "tasks"))
	workspaces, err := manager.List()
	if err != nil {
		if os.IsNotExist(err) {
			return tasks.Workspace{}, false, nil
		}
		return tasks.Workspace{}, false, err
	}
	workspace := latestReportedTaskWorkspaceFromList(workspaces)
	if workspace.ID == "" {
		return tasks.Workspace{}, false, nil
	}
	return workspace, true, nil
}

func latestReportedTaskWorkspaceFromList(workspaces []tasks.Workspace) tasks.Workspace {
	for _, workspace := range workspaces {
		if strings.TrimSpace(latestAnalysisReportPath(workspace)) != "" {
			return workspace
		}
	}
	return tasks.Workspace{}
}

func latestAnalysisReportPath(workspace tasks.Workspace) string {
	transcriptPath := strings.TrimSpace(workspace.LatestTranscriptPath())
	if transcriptPath != "" {
		content, err := os.ReadFile(transcriptPath)
		if err == nil {
			lines := strings.Split(string(content), "\n")
			for index := len(lines) - 1; index >= 0; index-- {
				trimmed := strings.TrimSpace(lines[index])
				if strings.HasPrefix(trimmed, "Analysis report:") {
					return strings.TrimSpace(strings.TrimPrefix(trimmed, "Analysis report:"))
				}
				if reportPath := latestAnalysisReportPathFromTranscriptLine(trimmed); reportPath != "" {
					return reportPath
				}
			}
		}
	}
	if reportPath, ok := requestedMarkdownOutputPath(workspace.Title); ok {
		return reportPath
	}
	return ""
}

func latestAnalysisReportPathFromTranscriptLine(line string) string {
	if strings.TrimSpace(line) == "" {
		return ""
	}
	if match := regexp.MustCompile(`(?i)analysis report:\s*([^\s"'\\,]+\.md)`).FindStringSubmatch(line); len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	var envelope struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		return ""
	}
	for _, key := range []string{"analysis_report", "analysis_report_path", "report_path", "report", "content", "summary"} {
		value := payloadStringValue(envelope.Payload, key)
		if match := regexp.MustCompile(`(?i)analysis report:\s*([^\s"'\\,]+\.md)`).FindStringSubmatch(value); len(match) == 2 {
			return strings.TrimSpace(match[1])
		}
	}
	return ""
}

func displayRuntimeField(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func firstExistingTextFile(root string, candidates []string) (string, string) {
	for _, candidate := range candidates {
		path := filepath.Join(root, candidate)
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return filepath.ToSlash(candidate), string(content)
	}
	return "not found", ""
}

func markdownTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
	}
	return ""
}

func firstMeaningfulMarkdownLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "```") {
			continue
		}
		if len(trimmed) > 220 {
			return strings.TrimSpace(trimmed[:220]) + "..."
		}
		return trimmed
	}
	return ""
}

func markdownBulletsUnderHeading(content string, heading string, limit int) []string {
	lines := strings.Split(content, "\n")
	capturing := false
	items := []string{}
	headingPrefix := "## " + strings.TrimSpace(heading)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") && strings.EqualFold(trimmed, headingPrefix) {
			capturing = true
			continue
		}
		if capturing && strings.HasPrefix(trimmed, "## ") {
			break
		}
		if !capturing {
			continue
		}
		if item, ok := markdownListItemText(trimmed); ok {
			items = append(items, item)
			if limit > 0 && len(items) >= limit {
				break
			}
		}
	}
	return items
}

func markdownListItemText(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "- ") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")), true
	}
	dotIndex := strings.Index(trimmed, ". ")
	if dotIndex > 0 {
		prefix := trimmed[:dotIndex]
		valid := true
		for _, r := range prefix {
			if r < '0' || r > '9' {
				valid = false
				break
			}
		}
		if valid {
			return strings.TrimSpace(trimmed[dotIndex+2:]), true
		}
	}
	return "", false
}

func projectStackSignals(root string) []string {
	signals := []string{}
	for _, item := range []struct {
		Path   string
		Signal string
	}{
		{"go.mod", "Go module"},
		{"package.json", "Node package"},
		{"pyproject.toml", "Python project"},
		{"Cargo.toml", "Rust crate"},
		{"cmd", "Go-style cmd entrypoints"},
		{"internal", "Go internal packages"},
		{"frontend", "frontend app"},
	} {
		if _, err := os.Stat(filepath.Join(root, item.Path)); err == nil {
			signals = append(signals, item.Signal)
		}
	}
	return signals
}

func summarizeGoMod(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	moduleName := ""
	requireCount := 0
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "module "):
			moduleName = strings.TrimSpace(strings.TrimPrefix(trimmed, "module "))
		case strings.HasPrefix(trimmed, "require "):
			requireCount++
		case strings.HasPrefix(trimmed, "\t") && requireCount >= 0:
			requireCount++
		}
	}
	if moduleName == "" {
		moduleName = "unknown"
	}
	return fmt.Sprintf("%s, requires=%d", moduleName, requireCount), nil
}

func summarizePackageJSON(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	type packageJSON struct {
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Dependencies map[string]string `json:"dependencies"`
	}
	var pkg packageJSON
	if err := json.Unmarshal(content, &pkg); err != nil {
		return "", err
	}
	name := strings.TrimSpace(pkg.Name)
	if name == "" {
		name = "unknown"
	}
	return fmt.Sprintf("%s, dependencies=%d", name, len(pkg.Dependencies)), nil
}

func looksLikeProjectFileCountQuestion(lowered string) bool {
	if looksLikeExplicitProjectWorkRequest(lowered) {
		return false
	}
	// P5-3: Multi-intent guard. When the request has 2+ layers
	// ("read README + count files by tech"), it needs pipeline analysis.
	if hasMultiLayerIntent(lowered) {
		return false
	}
	// P4-8: Same code-entity guard as looksLikeProjectFileListQuestion.
	if strings.Contains(lowered, "func") || strings.Contains(lowered, "函数") ||
		strings.Contains(lowered, "类型") || strings.Contains(lowered, "type") ||
		strings.Contains(lowered, "接口") || strings.Contains(lowered, "interface") ||
		strings.Contains(lowered, "结构") || strings.Contains(lowered, "struct") ||
		strings.Contains(lowered, "方法") || strings.Contains(lowered, "method") ||
		strings.Contains(lowered, "导出") || strings.Contains(lowered, "exported") ||
		strings.Contains(lowered, "包") || strings.Contains(lowered, "package") {
		return false
	}
	fileSignal := strings.Contains(lowered, "file") || strings.Contains(lowered, "文件")
	countSignal := strings.Contains(lowered, "how many") ||
		strings.Contains(lowered, "count") ||
		strings.Contains(lowered, "total") ||
		strings.Contains(lowered, "多少") ||
		strings.Contains(lowered, "总共") ||
		strings.Contains(lowered, "数量")
	projectSignal := strings.Contains(lowered, "project") ||
		strings.Contains(lowered, "repo") ||
		strings.Contains(lowered, "repository") ||
		strings.Contains(lowered, "项目")
	return fileSignal && countSignal && projectSignal
}

// hasMultiLayerIntent returns true when the input contains 2+ distinct
// intent layers (e.g., "read X AND count Y", "analyze A AND list B").
// These need pipeline analysis, not a single direct_answer handler.
// P5-3: prevents multi-layer requests from being captured by simple handlers.
func hasMultiLayerIntent(lowered string) bool {
	readAnalyzeVerbs := []string{
		"read", "读取", "阅读", "查看", "了解", "分析",
		"inspect", "examine", "understand", "analyze",
	}
	countListVerbs := []string{
		"count", "统计", "list", "列出", "how many", "多少", "数量",
	}
	hasRead := false
	for _, v := range readAnalyzeVerbs {
		if strings.Contains(lowered, v) {
			hasRead = true
			break
		}
	}
	if !hasRead {
		return false
	}
	for _, v := range countListVerbs {
		if strings.Contains(lowered, v) {
			return true // both layers present → multi-intent
		}
	}
	return false
}

func looksLikeProjectFileListQuestion(lowered string) bool {
	if looksLikeExplicitProjectWorkRequest(lowered) {
		return false
	}
	// P4-8: Code-entity guard. When the user asks about functions, types,
	// interfaces, structs, methods, or packages, they want code analysis
	// (→ pipeline), not a simple file list (→ direct_answer).
	codeEntitySignal := strings.Contains(lowered, "func") ||
		strings.Contains(lowered, "函数") ||
		strings.Contains(lowered, "类型") ||
		strings.Contains(lowered, "type") ||
		strings.Contains(lowered, "接口") ||
		strings.Contains(lowered, "interface") ||
		strings.Contains(lowered, "结构") ||
		strings.Contains(lowered, "struct") ||
		strings.Contains(lowered, "方法") ||
		strings.Contains(lowered, "method") ||
		strings.Contains(lowered, "导出") ||
		strings.Contains(lowered, "exported") ||
		strings.Contains(lowered, "包") ||
		strings.Contains(lowered, "package")
	if codeEntitySignal {
		return false
	}
	fileSignal := strings.Contains(lowered, "file") || strings.Contains(lowered, "文件")
	listSignal := strings.Contains(lowered, "list") ||
		strings.Contains(lowered, "show") ||
		strings.Contains(lowered, "what files") ||
		strings.Contains(lowered, "which files") ||
		strings.Contains(lowered, "有哪些") ||
		strings.Contains(lowered, "列出") ||
		strings.Contains(lowered, "目录") ||
		strings.Contains(lowered, "tree")
	projectSignal := strings.Contains(lowered, "project") ||
		strings.Contains(lowered, "repo") ||
		strings.Contains(lowered, "repository") ||
		strings.Contains(lowered, "项目")
	return fileSignal && listSignal && projectSignal
}

func looksLikeTodoFixmeSearchQuestion(lowered string) bool {
	// P4-4: Skip the explicit-project-work guard when the input is clearly
	// an analysis/search request. Otherwise "fix" in "fixme" triggers
	// looksLikeExplicitProjectWorkRequest, which causes a false-negative
	// and routes the search to the pipeline instead of direct_answer.
	if looksLikeExplicitProjectWorkRequest(lowered) && !isAnalysisRequest(lowered) {
		return false
	}
	// Analysis verbs required — prevents false match on mere mention of TODO/FIXME
	analysisSignal := strings.Contains(lowered, "find") ||
		strings.Contains(lowered, "search") ||
		strings.Contains(lowered, "list") ||
		strings.Contains(lowered, "grep") ||
		strings.Contains(lowered, "scan") ||
		strings.Contains(lowered, "找出") ||
		strings.Contains(lowered, "搜索") ||
		strings.Contains(lowered, "查找") ||
		strings.Contains(lowered, "列出") ||
		strings.Contains(lowered, "扫描")
	// TODO/FIXME signal
	todoSignal := strings.Contains(lowered, "todo") ||
		strings.Contains(lowered, "fixme") ||
		strings.Contains(lowered, "hack") ||
		strings.Contains(lowered, "xxx") ||
		strings.Contains(lowered, "bug") ||
		strings.Contains(lowered, "待办") ||
		strings.Contains(lowered, "修复") ||
		strings.Contains(lowered, "标记")
	// P6-3: scopeSignal includes "所有/all" so short inputs like
	// "找出所有TODO" match without needing "项目/文件" keywords.
	scopeSignal := strings.Contains(lowered, "file") ||
		strings.Contains(lowered, "文件") ||
		strings.Contains(lowered, "project") ||
		strings.Contains(lowered, "项目") ||
		strings.Contains(lowered, "code") ||
		strings.Contains(lowered, "代码") ||
		strings.Contains(lowered, "comment") ||
		strings.Contains(lowered, "注释") ||
		strings.Contains(lowered, "repo") ||
		strings.Contains(lowered, "所有") || // "找出所有TODO"
		strings.Contains(lowered, "all") // "find all TODOs"
	return analysisSignal && todoSignal && scopeSignal
}

func searchTodoFixmeInProject(root string) (string, error) {
	// Excluded directories for grep
	excludedDirs := []string{".git", ".avatars", "avatars", "node_modules", "vendor", "dist", "build", "__pycache__"}
	var results []string
	patterns := []string{"TODO", "FIXME", "todo", "fixme", "HACK", "XXX"}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable paths
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			for _, excluded := range excludedDirs {
				if name == excluded {
					return filepath.SkipDir
				}
			}
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		// Only check text-ish files (skip binaries, images, minified, lock files)
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if isBinaryExtension(ext) {
			return nil
		}
		// Skip minified, lock, and generated files
		base := strings.ToLower(name)
		if strings.Contains(base, ".min.") || strings.HasSuffix(base, "-lock.json") || strings.HasSuffix(base, ".lock") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(content), "\n")
		relPath := filepath.ToSlash(filepath.Clean(path))
		for lineNum, line := range lines {
			lineLower := strings.ToLower(line)
			for _, pattern := range patterns {
				if strings.Contains(lineLower, strings.ToLower(pattern)) {
					trimmed := strings.TrimSpace(line)
					if len(trimmed) > 120 {
						trimmed = trimmed[:120] + "..."
					}
					results = append(results, fmt.Sprintf("%s:%d: %s", relPath, lineNum+1, trimmed))
					break // one match per line
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		return "No TODO/FIXME/HACK/XXX comments found in project files.", nil
	}

	// Limit output
	maxResults := 50
	truncated := false
	if len(results) > maxResults {
		results = results[:maxResults]
		truncated = true
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d TODO/FIXME/HACK/XXX entries", len(results)))
	if truncated {
		sb.WriteString(fmt.Sprintf(" (showing first %d of %d total)", maxResults, len(results)))
	}
	sb.WriteString(":\n\n")
	for _, r := range results {
		sb.WriteString(r)
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func isBinaryExtension(ext string) bool {
	switch ext {
	case ".exe", ".dll", ".so", ".dylib", ".bin", ".dat",
		".png", ".jpg", ".jpeg", ".gif", ".bmp", ".ico", ".svg",
		".mp3", ".mp4", ".avi", ".mov", ".wav",
		".zip", ".tar", ".gz", ".bz2", ".7z", ".rar",
		".pdf", ".doc", ".docx", ".xls", ".xlsx":
		return true
	}
	return false
}

func countProjectFiles(root string) (int, error) {
	files, err := listProjectFiles(root, -1)
	if err != nil {
		return 0, err
	}
	return len(files), nil
}

func listProjectFiles(root string, limit int) ([]string, error) {
	excludedDirs := map[string]bool{
		".git":         true,
		".avatars":     true,
		"avatars":      true,
		"node_modules": true,
		"vendor":       true,
		"dist":         true,
		"build":        true,
	}
	files := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if excludedDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, filepath.ToSlash(filepath.Clean(path)))
		if limit > 0 && len(files) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}
