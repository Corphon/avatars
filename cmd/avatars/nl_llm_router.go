package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"avatars/internal/app"
	"avatars/internal/arch"
	"avatars/internal/llm"
	memstore "avatars/internal/memory"
)

// llmRouterDecision is the structured output from the LLM intent router.
// The LLM sees CLI_guide.md as its command catalog and returns a specific
// avatars CLI command to execute, following Claude Code's pattern of
// "full context → one LLM decision" instead of keyword pre-classification.
type llmRouterDecision struct {
	Action     string   `json:"action"`     // direct_answer, safe_run, clarify, guarded
	AnswerID   string   `json:"answer_id"`  // for direct_answer: which local handler to use
	Command    []string `json:"command"`    // for safe_run: exact avatars CLI args (without "avatars" prefix)
	Confidence int      `json:"confidence"` // 0-100
	Reason     string   `json:"reason"`     // machine/log-facing explanation
	Question   string   `json:"question"`   // user-facing clarify/guarded prompt (short)
	Options    []string `json:"options"`    // optional recovery choices (label or example command)
}

// llmRouterJSONSchema is the JSON schema for structured LLM output.
// The "command" field carries the full avatars CLI argument array.
var llmRouterJSONSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"action": map[string]any{
			"type": "string",
			"enum": []string{"direct_answer", "safe_run", "clarify", "guarded"},
		},
		"answer_id":  map[string]any{"type": "string"},
		"command":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"confidence": map[string]any{"type": "number"},
		"reason":     map[string]any{"type": "string"},
		"question":   map[string]any{"type": "string"},
		"options":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
	"required":             []string{"action", "confidence"},
	"additionalProperties": false,
}

// knownAvatarsCommands is the set of valid first-arg commands for validation.
// Any command returned by the LLM must start with one of these.
var knownAvatarsCommands = map[string]bool{
	"run": true, "intent": true, "repl": true, "bootstrap": true,
	"script": true, "edit": true, "edit-many": true, "resume": true,
	"tasks": true, "memory": true, "rollback": true, "governance": true,
	"feedback": true, "serve": true, "verify": true, "smoke": true,
	"actions": true, "route": true, "config": true, "llm": true,
	"shell": true, "write": true, "patch": true, "git": true,
	"mcp": true, "plugins": true, "skills": true, "arch": true,
}

// loadCLIGuideContent reads CLI_guide.md from the project root and returns
// a truncated version suitable for injection into the LLM system prompt.
// We cap at 6000 chars to keep the prompt within reasonable token budgets
// while still covering all commands and key usage patterns.
//
// CW1: When architecture.md adds ~1500 chars to the router prompt, we use
// summary mode (command list only, ~2000 chars) instead of full mode (~6000 chars)
// to keep total prompt size manageable.
func loadCLIGuideContent() string {
	return loadCLIGuideWithMode(false)
}

// loadCLIGuideSummary loads a compact version of CLI_guide.md (~2000 chars):
// just the command list and one-line descriptions, without full usage examples.
// Used when architecture.md context is also being injected.
func loadCLIGuideSummary() string {
	return loadCLIGuideWithMode(true)
}

func loadCLIGuideWithMode(summary bool) string {
	// Try project root first, then AVATARS_HOME, then cwd.
	candidates := []string{"CLI_guide.md"}
	if home := os.Getenv("AVATARS_HOME"); home != "" {
		candidates = append(candidates, filepath.Join(home, "CLI_guide.md"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "avatars", "CLI_guide.md"))
	}

	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		// CW1 summary mode: extract just the command list (one line per command).
		if summary {
			content = extractCLIGuideSummary(content)
			if len(content) > 3000 {
				content = content[:3000]
			}
			return content
		}

		// Full mode: include command reference and routing guidance.
		if len(content) > 8000 {
			content = content[:8000]
			if lastNewline := strings.LastIndex(content, "\n"); lastNewline > 0 {
				content = content[:lastNewline]
			}
			content += "\n\n[CLI_guide.md truncated at 8000 chars — full guide available on disk]"
		}
		return content
	}
	return ""
}

// extractCLIGuideSummary extracts a compact command summary from the full
// CLI_guide.md content. Strategy: find command headers (## lines) and capture
// the first descriptive sentence after each. Target ~2000 chars.
func extractCLIGuideSummary(full string) string {
	lines := strings.Split(full, "\n")
	var sb strings.Builder
	sb.WriteString("# Avatars Command Summary\n\n")

	inCommand := false
	cmdName := ""
	count := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Detect command sections: "### run" or "## Commands"
		if strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "## ") {
			cmdName = strings.TrimPrefix(strings.TrimPrefix(trimmed, "### "), "## ")
			if strings.HasPrefix(cmdName, "avatars ") {
				cmdName = strings.TrimPrefix(cmdName, "avatars ")
			}
			inCommand = true
			continue
		}
		if inCommand && trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "```") {
			// Capture first descriptive line.
			if len(trimmed) > 200 {
				trimmed = trimmed[:200]
			}
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", cmdName, trimmed))
			count++
			inCommand = false
			if count >= 25 {
				break
			}
		}
	}

	if sb.Len() == 0 {
		// Fallback: return first 2000 chars of content.
		if len(full) > 2000 {
			return full[:2000]
		}
		return full
	}
	return sb.String()
}

// buildLLMRouterSystemPrompt constructs the system prompt for the LLM intent
// router. It follows Claude Code's layered guidance pattern:
//  1. Role description (who you are)
//  2. Available commands catalog (CLI_guide.md)
//  3. Decision rules (when to use which command)
//  4. Output format (JSON schema)
//
// The LLM is expected to read the full command catalog and select the ONE
// correct avatars CLI command for the user's request. No keyword pre-filtering
// is done before the LLM call.
func buildLLMRouterSystemPrompt() string {
	// CW1/CW2: When architecture.md context will be injected (~1500 chars),
	// use CLI_guide_summary (~2000 chars) instead of full CLI_guide (~6000 chars)
	// to keep total prompt size manageable.
	hasArchCtx := hasArchitectureContext()

	var cliGuide string
	if hasArchCtx {
		cliGuide = loadCLIGuideSummary()
	} else {
		cliGuide = loadCLIGuideContent()
	}
	if cliGuide == "" {
		cliGuide = "[CLI_guide.md not found — use the fallback guidance below]"
	}

	var b strings.Builder
	b.WriteString("You are the intent router for avatars, a coding-agent CLI harness. ")
	b.WriteString("Your job: read the user's natural-language request and select the ONE correct avatars CLI command to execute. ")
	if hasArchCtx {
		b.WriteString("You have a command summary and project architecture context below.\n\n")
	} else {
		b.WriteString("You have access to the complete avatars command catalog below.\n\n")
	}

	b.WriteString("# Avatars Command Catalog\n\n")
	b.WriteString(cliGuide)
	b.WriteString("\n\n")

	// S1.6: Wire existing cli_actions.yaml into the hot-path router prompt
	// (previously only injected into the dead analyzeIntentWithLLM path).
	if actionMap := intentCLIActionPromptSection(); actionMap != "" {
		b.WriteString("# Bounded Action Map (cli_actions.yaml)\n\n")
		b.WriteString(actionMap)
		b.WriteString("\n\nPrefer these bounded actions when they match. Do not invent subcommands outside the catalog.\n\n")
	}

	b.WriteString("# Decision Rules\n\n")
	b.WriteString("Read the user's request carefully. Follow these rules to choose the right command:\n\n")

	b.WriteString("User input may be Chinese or English; classify by intent, not by language.\n\n")
	b.WriteString("## When to use each command type\n\n")
	b.WriteString("- **edit**: User wants to MODIFY, UPDATE, FIX, CHANGE, or ADD TO an EXISTING file. ")
	b.WriteString("Use this for iterative refinement: 'change the title', 'add an animation', 'darken the color', 'add error handling'. ")
	b.WriteString("If the previous turn created a file, user's next vague request almost certainly targets that file. ")
	b.WriteString("One file, one instruction. Example: `avatars edit --apply README.md \"fix the title\"`\n")
	b.WriteString("- **edit-many**: User wants to modify MULTIPLE existing files in one pass. ")
	b.WriteString("Example: `avatars edit-many --apply \"rename all functions\"`\n")
	b.WriteString("- **script --apply --llm**: User wants to CREATE a NEW standalone file — a program, game, tool, HTML page, script, or utility. ")
	b.WriteString("This includes: writing a game, creating a webpage, making a calculator, generating an HTML file, writing a Python/JS/Go script. ")
	b.WriteString("ALWAYS include --llm when the user's request has a concrete topic (not a generic placeholder like 'hello.py'). ")
	b.WriteString("The --llm flag tells avatars to use the LLM to generate the file content. Without --llm, avatars refuses to create topic-specific files. ")
	b.WriteString("Command format: [\"script\", \"--apply\", \"--llm\", \"<filename>\", \"<topic description>\"]. ")
	b.WriteString("Example: `avatars script --apply --llm pond_moonlight.html \"draw a lotus pond under moonlight\"`\n")
	b.WriteString("- **bootstrap --apply** (DEPRECATED): Legacy project scaffolding. Prefer `go mod init && avatars run` for new projects. ")
	b.WriteString("Only use if user explicitly asks for bootstrap. ")
	b.WriteString("Example: `avatars bootstrap --apply --name myapp --stack go-cli`\n")
	b.WriteString("- **run --new-task --permission-mode plan**: User wants READ-ONLY analysis, inspection, review, or issue hunting. ")
	b.WriteString("Use this for: \"analyze this project\", \"find issues\", \"inspect the code\", \"summarize what this does\". ")
	b.WriteString("NO file modifications. Example: `avatars run --new-task --permission-mode plan \"analyze this project\"`\n")
	b.WriteString("- **run --new-task --permission-mode acceptEdits**: User wants to do mutating CODING WORK that may write or modify files. ")
	b.WriteString("Use this for complex multi-step tasks that don't fit script/edit/bootstrap. ")
	b.WriteString("Example: `avatars run --new-task --permission-mode acceptEdits \"implement user authentication\"`\n")
	b.WriteString("- **run --resume**: User is CONTINUING the previous attempt in this workspace — ")
	b.WriteString("fixing a red gate, picking up after max turns, or saying continue the last run / fix the failing tests. ")
	b.WriteString("Do NOT use --new-task for that; a new task re-pads the plan. ")
	b.WriteString("Example: `avatars run --resume <latest-transcript> --permission-mode acceptEdits \"keep going until tests are green\"`\n")
	b.WriteString("- **run --from-file**: User has a detailed task plan in a markdown file. ")
	b.WriteString("Example: `avatars run --from-file plan.md`\n")
	b.WriteString("- **patch**: User gives an EXACT before/after string replacement for a file. ")
	b.WriteString("Example: `avatars patch README.md \"old text\" \"new text\"`\n")
	b.WriteString("- **shell**: User wants to run a system/shell command. ")
	b.WriteString("Example: `avatars shell go test ./...`\n")

	b.WriteString("\n## Key principles\n\n")
	b.WriteString("1. **Prefer edit over script for existing files.** If the file already exists, use edit. If it's new, use script.\n")
	b.WriteString("2. **Read-only → plan mode.** If the user says \"analyze\", \"inspect\", \"review\", \"find issues\", \"don't change code\", use --permission-mode plan.\n")
	b.WriteString("3. **New creation routing:**\n")
	b.WriteString("   - Single-file creation (script, HTML page, game) → **script --apply --llm**\n")
	b.WriteString("   - Multi-file project scaffold (CLI tool, web app, service) → **bootstrap --apply**\n")
	b.WriteString("   - Multiple independent files of different types (e.g., 'a Go program AND an HTML page') → pick the FIRST file for **script --apply --llm**. The user will ask for the second in the next turn. Do NOT use run for new file creation.\n")
	b.WriteString("   Creating something new is mutating work — NEVER use --permission-mode plan for creation.\n")
	b.WriteString("4. **User wants a NEW PROJECT (not just a single file) → bootstrap.**\n")
	b.WriteString("   If the user names a project AND describes features it should have, use bootstrap --apply.\n")
	b.WriteString("   Do NOT use run (which assumes existing code to analyze). Bootstrap creates the project skeleton.\n")
	b.WriteString("5. **Explicit path mentioned by user as TARGET → use it.** If the user says \"write to output.html\", the target is output.html.\n")
	b.WriteString("6. **File mentioned as REFERENCE (loaded via @file, or \"follow X as guidance\") → do NOT use it as the target path.** The reference file provides guidance; the user wants a NEW file created.\n")
	b.WriteString("7. **When uncertain, prefer clarify.** Don't guess a command if the user's intent is genuinely ambiguous.\n")
	b.WriteString("8. **For simple chat/status/capability questions, use direct_answer.** Not every message needs a command.\n")
	b.WriteString("9. **Conversational continuity.** When the user says 'add a feature', 'modify it', 'change the output' WITHOUT naming a specific file → target the LAST CREATED/MODIFIED file from the session context below. Use **edit --apply**. Do NOT use run or plan for simple feature additions to existing files. Only use run for genuinely complex multi-step tasks that need planning.\n")
	b.WriteString("9b. **Repair / continue last run → --resume, never --new-task.** Phrases like 继续, 继续推进, keep going, continue advancing, continue the last run, the last round failed the gate, keep going until go test/pytest/npm test/cargo test is green, or 'fix the failing tests' mean continue the existing task. --new-task would open a new session and inflate Phase Count. If they also name a .md/.txt file (继续推进 phase1.md), use `--from-file` for that file and still `--resume` — do not put leftover NL next to `--from-file`.\n")
	b.WriteString("10. **CW3: Large file edits (>500 lines) need careful scoping.** When the user asks to modify a large file, prefer to scope the edit to specific sections. If the Registration Points in the architecture context include line ranges, reference them to narrow the edit scope. For truly complex large-file changes, route to `run --new-task --permission-mode plan` for analysis first, then edit.\n")
	b.WriteString("11. **Stage is a visual sketch, never delivery source.** Use `stage` only to visualize or illustrate. Implementing features, tests, or packages is `run` / `script` / `edit`.\n")
	b.WriteString("   - Visualize the repo / architecture map / 画个架构图 → `stage --project`\n")
	b.WriteString("   - Particle art, story illustration, ambient HTML → `stage \"<idea>\"`\n")
	b.WriteString("   - Restyle an existing gallery card → `stage --edit latest \"<direction>\"`\n")
	b.WriteString("   - `avatars serve` is the operator dashboard. The creative gallery is `avatars stage --serve` (port 5100).\n")
	b.WriteString("12. **--resume takes a transcript path, never a run id.** The argument must be a `.jsonl` file under `.avatars/tasks/<task>/sessions/`. Never a numeric snowflake. If you do not know the path, omit it and the harness fills the latest real transcript. If the user says do not resume / 不要 resume (without pointing at a fake number), use `--new-task`.\n\n")

	// ARCH-6: Inject architecture.md context when available.
	// This gives the LLM router project-level awareness — entry points,
	// Registration Points (multi-file change patterns), and conventions.
	// For edit/simple ops the context is harmless background; for run/bootstrap/
	// edit-many it's essential for correct routing.
	// I4 fix: Inject project file context before architecture context.
	// Gives the LLM a mental model of the project: files, README, git status.
	projCtx := buildProjectContext()
	if projCtx != "" {
		b.WriteString(projCtx)
		b.WriteString("\n\n")
	}

	archCtx := buildArchRouterContext()
	if archCtx != "" {
		b.WriteString(archCtx)
	}

	b.WriteString("# Output Format\n\n")
	b.WriteString("Return compact JSON only. No markdown fences, no explanation outside the JSON.\n\n")
	b.WriteString("For direct_answer (simple questions, chat, capabilities):\n")
	b.WriteString(`{"action":"direct_answer","answer_id":"capabilities","confidence":95,"reason":"user asks what I can do"}` + "\n\n")
	b.WriteString("For safe_run (the user wants project work done):\n")
	b.WriteString(`{"action":"safe_run","command":["edit","--apply","README.md","fix title"],"confidence":90,"reason":"user wants to modify existing file"}` + "\n\n")
	b.WriteString("For clarify (ambiguous or incomplete):\n")
	b.WriteString(`{"action":"clarify","reason":"unclear what file to target","question":"Which file should I change?","options":["avatars edit --apply README.md \"...\"","avatars run --new-task --permission-mode plan \"...\""],"confidence":30}` + "\n\n")
	b.WriteString("For guarded (destructive/risky):\n")
	b.WriteString(`{"action":"guarded","reason":"user asked to delete production data","question":"This would destroy data. Confirm with an explicit command?","options":["avatars run --new-task --permission-mode plan \"...\"","cancel"],"confidence":99}` + "\n")
	b.WriteString("Put machine diagnosis in reason; put the short user-facing prompt in question; put 1-3 recovery choices in options.\n")
	b.WriteString("Use guarded when the user asks to:\n")
	b.WriteString("- Delete, destroy, wipe, or format entire files, disks, databases, or production data\n")
	b.WriteString("- Exfiltrate sensitive data: send /etc/passwd, /etc/shadow, private keys, or credentials to external servers\n")
	b.WriteString("- Install unverified software from URLs via pip/npm/cargo piped to shell\n")
	b.WriteString("- Run commands that could harm the system (rm -rf /, del /f /s, dd, format, > /dev/)\n")
	b.WriteString("- Bypass authentication, crack passwords, or exploit vulnerabilities\n")
	b.WriteString("- Execute shell commands that download and pipe to bash/sh (curl ... | bash)\n")
	b.WriteString("IMPORTANT context distinction:\n")
	b.WriteString("  \"prettify JSON\" = SAFE → safe_run with script\n")
	b.WriteString("  \"format the C: drive\" = DANGEROUS → guarded\n")
	b.WriteString("  \"delete one function\" (in code) = SAFE → safe_run with edit\n")
	b.WriteString("  \"delete all files\" = DANGEROUS → guarded\n")
	b.WriteString("  \"send a generated file to a remote server for deployment\" = may be SAFE if it's the user's own server\n")
	b.WriteString("  \"send /etc/passwd to a remote server\" = DANGEROUS → guarded\n\n")
	b.WriteString("Available answer_ids for direct_answer:\n")
	b.WriteString("- greeting, capabilities, last_action, active_file_summary, project_overview, model_identity, project_opinion, project_manifest, docs_overview, recent_tasks, result_location, conversational_repair, analysis_report_quality, file_count, file_list, todo_fixme_search, next_steps\n")
	b.WriteString("- skills_status: user asks what skills exist / which skills are available\n")
	b.WriteString("- memory_pitfalls: user asks about past pitfalls / lessons\n")
	b.WriteString("- workflow_phase: user asks which phase / progress\n")
	b.WriteString("- resume_status: user asks whether approval resume continues / Critic after approve\n")
	b.WriteString("- parallel_status / progress_ux: user asks about multi-file parallel, stderr progress tree, handoff lines\n")
	b.WriteString("- llm_config: user asks about max_tokens / fallback providers / shell timeout / genkit tool calls / whether a downed primary model switches to fallback\n")
	b.WriteString("Use next_steps when the user asks what to do next, what should I work on, give me suggestions.\n")
	b.WriteString("Prefer direct_answer + answer_id for status/config/memory/skills questions — do NOT launch run/shell/find for those.\n\n")
	b.WriteString("The command array must start with a valid avatars subcommand. Do NOT include the 'avatars' prefix — just the args that follow it.\n")

	return b.String()
}

// intentLLMMemo caches the last router decision for the same user text so
// `intent` (resolveNaturalLanguage + routeIntent) does not pay two LLM calls
// and print CoT twice (F115). Same prefix → provider cache hit on the first
// call; the second call is a process-local hit (zero tokens).
var intentLLMMemo struct {
	mu       sync.Mutex
	input    string
	decision llmRouterDecision
	ok       bool
}

func recallIntentLLMMemo(input string) (llmRouterDecision, bool, bool) {
	trimmed := strings.TrimSpace(input)
	intentLLMMemo.mu.Lock()
	defer intentLLMMemo.mu.Unlock()
	if trimmed == "" || intentLLMMemo.input != trimmed {
		return llmRouterDecision{}, false, false
	}
	return intentLLMMemo.decision, intentLLMMemo.ok, true
}

func storeIntentLLMMemo(input string, decision llmRouterDecision, ok bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return
	}
	intentLLMMemo.mu.Lock()
	intentLLMMemo.input = trimmed
	intentLLMMemo.decision = decision
	intentLLMMemo.ok = ok
	intentLLMMemo.mu.Unlock()
}

func resetIntentLLMMemo() {
	intentLLMMemo.mu.Lock()
	intentLLMMemo.input = ""
	intentLLMMemo.decision = llmRouterDecision{}
	intentLLMMemo.ok = false
	intentLLMMemo.mu.Unlock()
}

// routeIntentViaLLM sends the user's natural-language input to the LLM with
// the full CLI_guide.md command catalog and returns a structured decision
// that includes the specific CLI command to execute.
//
// This replaces the old multi-stage pipeline:
//
//	keywords → LLM classifier (category only) → keywords → edit/script/bootstrap analyzers
//
// with a single LLM call:
//
//	CLI_guide.md + user input → one LLM call → specific command
func routeIntentViaLLM(input string) (llmRouterDecision, bool) {
	if decision, ok, hit := recallIntentLLMMemo(input); hit {
		return decision, ok
	}

	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		if cleanup != nil {
			cleanup()
		}
		return llmRouterDecision{}, false
	}
	if cleanup != nil {
		defer cleanup()
	}

	llmCtx, llmCancel := llmCallContext()
	defer llmCancel()

	// Progress feedback: write to stderr so it's visible even in compact
	// mode (which only captures stdout). F72: use newline when stderr is
	// redirected / json mode — \r spinners vanish in log files.
	writeCarriageProgress("⌛ thinking…")

	// NL7: Use the classifier_model from agent.yaml if configured,
	// allowing a different (e.g. faster/cheaper) model for intent routing.
	classifierModel := strings.TrimSpace(app.LoadClassifierModel())

	// Add context about recently generated files so the LLM can handle
	// iterative refinement: "change the title" → targets the last generated file.
	recentContext := buildRecentFileContext()

	// Stream CoT while classifying. StructuredOutput normally forces Auto→Off;
	// ThinkModeOn keeps DeepSeek thinking so the user can see the judgment
	// direction before the JSON decision lands. Do not put this text on the
	// prompt prefix (cache).
	printer := newIntentThinkingPrinter()
	// Use json_object (not json_schema) for provider compatibility.
	// DeepSeek and other providers don't support strict json_schema.
	// The JSON output is validated manually in parseLLMRouterDecision.
	response, err := client.Generate(llmCtx, llm.Request{
		SystemPrompt:     buildLLMRouterSystemPrompt(),
		UserPrompt:       "User request:\n" + strings.TrimSpace(input) + recentContext,
		StructuredOutput: true,
		ThinkMode:        llm.ThinkModeOn,
		Model:            classifierModel,
		ThinkingCallback: printer.Write,
	})
	if !printer.Received() {
		clearCarriageProgress()
		if err == nil && strings.TrimSpace(response.Reasoning) != "" {
			printer.Write(response.Reasoning)
		}
	}
	printer.Close()

	if err != nil || response.Fallback {
		storeIntentLLMMemo(input, llmRouterDecision{}, false)
		return llmRouterDecision{}, false
	}

	decision, ok := parseLLMRouterDecision(response.Text)
	if !ok {
		fmt.Fprintf(os.Stderr, "avatars: LLM router returned unparseable output (%d chars), falling back\n", len(response.Text))
		storeIntentLLMMemo(input, llmRouterDecision{}, false)
		return llmRouterDecision{}, false
	}
	storeIntentLLMMemo(input, decision, true)
	writeIntentJudgment(os.Stderr, progressMode(), decision)
	return decision, true
}

// parseLLMRouterDecision extracts and validates the LLM's JSON decision.
func parseLLMRouterDecision(text string) (llmRouterDecision, bool) {
	raw, ok := robustExtractJSON(text)
	if !ok {
		return llmRouterDecision{}, false
	}
	raw = cleanJSONString(raw)
	var decision llmRouterDecision
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return llmRouterDecision{}, false
	}
	decision.Action = strings.ToLower(strings.TrimSpace(decision.Action))
	decision.AnswerID = strings.ToLower(strings.TrimSpace(decision.AnswerID))
	decision.Reason = strings.TrimSpace(decision.Reason)
	decision.Question = strings.TrimSpace(decision.Question)
	if len(decision.Options) > 0 {
		normalizedOpts := make([]string, 0, len(decision.Options))
		for _, opt := range decision.Options {
			opt = strings.TrimSpace(opt)
			if opt != "" {
				normalizedOpts = append(normalizedOpts, opt)
			}
		}
		decision.Options = normalizedOpts
	}

	// DeepSeek sometimes returns command names as actions (e.g., "script"
	// instead of "safe_run"). Remap: if action is a known command name and
	// the command field is empty, move it to command[] and set action=safe_run.
	if decision.Action != "" && decision.Action != "direct_answer" && decision.Action != "safe_run" &&
		decision.Action != "clarify" && decision.Action != "guarded" {
		if knownAvatarsCommands[decision.Action] && len(decision.Command) == 0 {
			decision.Command = []string{decision.Action}
			decision.Action = "safe_run"
		}
	}
	if decision.Action == "" {
		return llmRouterDecision{}, false
	}
	// Normalize command entries.
	normalized := make([]string, 0, len(decision.Command))
	for _, arg := range decision.Command {
		arg = strings.TrimSpace(arg)
		if arg != "" {
			normalized = append(normalized, arg)
		}
	}
	decision.Command = normalized
	return decision, true
}

// fixEditToScriptForNewFile converts an edit command for a non-existent
// file into a script --apply --llm command. This prevents the "edit
// non-existent file" failure (I6): when the LLM routes to edit but the
// target file doesn't exist, the user likely wants to CREATE a new file
// rather than modify an existing one.
//
// Returns the modified command and true if the conversion was applied.
func fixEditToScriptForNewFile(command []string) ([]string, bool) {
	if len(command) < 4 || command[0] != "edit" || command[1] != "--apply" {
		return command, false
	}
	targetPath := command[2]
	instruction := strings.Join(command[3:], " ")
	// Check if target file exists.
	if _, err := os.Stat(targetPath); err == nil {
		return command, false // file exists, keep edit
	}
	// File doesn't exist — convert to script --apply --llm.
	return []string{"script", "--apply", "--llm", targetPath, instruction}, true
}

// validateLLMCommand performs safety checks on the LLM-chosen command.
// Returns an error string if the command is invalid or unsafe.
// Returns empty string if the command passes validation.
func validateLLMCommand(command []string) string {
	if len(command) == 0 {
		return "empty command"
	}
	// Command must start with a known avatars subcommand.
	if !knownAvatarsCommands[command[0]] {
		return fmt.Sprintf("unknown avatars subcommand: %q", command[0])
	}
	// Reject absolute paths and path traversal.
	for _, arg := range command {
		if filepath.IsAbs(arg) {
			return fmt.Sprintf("absolute path not allowed: %q", arg)
		}
		cleaned := filepath.Clean(filepath.FromSlash(arg))
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
			return fmt.Sprintf("path traversal not allowed: %q", arg)
		}
	}
	return ""
}

// llmRouterDecisionToNaturalLanguage converts the LLM router's output
// into the existing naturalLanguageDecision format used by the pipeline.
// This is the bridge between the new LLM-first router and the existing
// REPL dispatch code, minimizing changes to downstream consumers.
func llmRouterDecisionToNaturalLanguage(decision llmRouterDecision) naturalLanguageDecision {
	confidence := decision.Confidence
	if confidence <= 0 {
		confidence = 60
	}
	question := strings.TrimSpace(decision.Question)
	options := uniqueNonEmptyStrings(decision.Options)

	switch decision.Action {
	case "direct_answer":
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionAnswer,
			Reason:     "llm router: " + decision.Reason,
			Answer:     decision.AnswerID,
			Confidence: confidence,
		}
	case "safe_run":
		cmd := decision.Command
		if len(cmd) == 0 {
			// Placeholder only — caller must rewrite with the original user text (S1.3).
			cmd = []string{"run", "--new-task", "--permission-mode", "default", ""}
		}
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionSafeRun,
			Reason:     "llm router: " + decision.Reason,
			Command:    cmd,
			Confidence: confidence,
		}
	case "clarify":
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionClarify,
			Reason:     decision.Reason,
			Question:   question,
			Options:    options,
			Confidence: confidence,
		}
	case "guarded":
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionGuarded,
			Reason:     decision.Reason,
			Question:   question,
			Options:    options,
			Confidence: confidence,
		}
	default:
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionClarify,
			Reason:     "llm router returned unknown action: " + decision.Action,
			Question:   "",
			Confidence: 20,
		}
	}
}

// buildRecentFileContext returns SQLite-backed accumulated session context
// for the LLM router. Each REPL turn appends to the context via
// memstore.UpdateReplContext (called from repl.go). The LLM sees all files
// and recent actions, enabling multi-turn conversational coding.
func buildRecentFileContext() string {
	// First, bootstrap from last-turn artifact if the SQLite store is empty.
	ctx := memstore.LoadReplContext()
	if len(ctx.Files) == 0 && len(ctx.Actions) == 0 {
		artifact, ok, _ := loadREPLLastTurn(".")
		if ok && (len(artifact.Artifacts) > 0 || artifact.Input != "") {
			memstore.UpdateReplContext(artifact.Input, artifact.Kind, artifact.Artifacts)
		}
	}
	return memstore.BuildReplContextForLLM()
}

// generateNextSteps uses the LLM to suggest actionable next steps based on
// the current project context (files, recent actions, conversation summary)
// and the available CLI commands (from CLI_guide.md). This gives the user
// intelligent, contextual guidance rather than a static help message.
func generateNextSteps() string {
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		if cleanup != nil {
			cleanup()
		}
		return ""
	}
	if cleanup != nil {
		defer cleanup()
	}

	ctx := memstore.LoadReplContext()
	if len(ctx.Files) == 0 && len(ctx.Actions) == 0 {
		return "The project is empty. Try saying: create a Go project, write a web game, or analyze this project."
	}

	// Build a compact prompt with project context + CLI commands.
	var b strings.Builder
	b.WriteString("You are a helpful coding assistant. Based on the project context below, suggest 3-5 concrete, actionable next steps the user can take. Be specific and practical. Mention exact commands or natural language phrases they can type.\n\n")
	b.WriteString("Project context:\n")
	b.WriteString(ctx.ConversationSummary + "\n")
	b.WriteString(fmt.Sprintf("Files: %d files tracked\n", len(ctx.Files)))
	if len(ctx.Actions) > 0 {
		b.WriteString("Recent actions:\n")
		for i, a := range ctx.Actions {
			if i >= 3 {
				break
			}
			b.WriteString(fmt.Sprintf("  - %s\n", a))
		}
	}
	b.WriteString("\nAvailable commands: script (create files), edit (modify files), bootstrap (new project), stage (visualize), run (analyze).\n")
	b.WriteString("\nRespond in English. Keep it concise — just a bullet list of suggestions. Each suggestion should start with a dash. Example:\n")
	b.WriteString("- Run go test to verify the code\n- Use stage --project to visualize the project\n- Add error handling to main.go\n")

	llmCtx, llmCancel := llmCallContext()
	defer llmCancel()

	resp, err := client.Generate(llmCtx, llm.Request{
		SystemPrompt: "You are a helpful coding assistant. Suggest next steps based on project context.",
		UserPrompt:   b.String(),
	})
	if err != nil || resp.Fallback || strings.TrimSpace(resp.Text) == "" {
		return ""
	}
	return strings.TrimSpace(resp.Text)
}

// hasArchitectureContext is a lightweight check (no full injection built)
// that returns true if architecture.md exists and would be injected.
// Used by buildLLMRouterSystemPrompt to decide between full/summary CLI guide.
func hasArchitectureContext() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	doc, err := arch.ReadArchDoc(cwd)
	if err != nil || doc == nil {
		return false
	}
	return arch.InjectionWorthy(doc, cwd)
}

// buildArchRouterContext reads the project's architecture.md (if it exists and
// is confirmed) and returns a compact injection string for the LLM router's
// system prompt. This gives the router project-level awareness:
//   - Entry points (what the project is)
//   - Registration Points (what files to modify when adding a component)
//   - Conventions (mandatory rules to follow)
//
// The injection is designed to be ~1500 characters — small enough to not
// dominate the prompt, large enough to provide actionable guidance.
//
// Returns "" if no architecture.md exists, it's stale, or it's a draft.
func buildArchRouterContext() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	doc, err := arch.ReadArchDoc(cwd)
	if err != nil || doc == nil {
		return ""
	}

	if !arch.InjectionWorthy(doc, cwd) {
		return ""
	}

	// Check staleness and add warning if needed.
	// Stale architecture is still injected — it's better than nothing.
	check := arch.CheckStale(cwd, doc)
	isStale := check.IsStale && doc.Meta.Status == "confirmed"

	// Build compact injection.
	injection := arch.BuildInjection(doc, arch.InjectOverview)
	if injection == "" {
		return ""
	}

	// Wrap with routing-relevant framing.
	var b strings.Builder
	b.WriteString("\n# Project Architecture Context\n\n")
	if isStale {
		b.WriteString("⚠️  This document may be outdated. Use with caution, but don't ignore it.\n\n")
	}
	b.WriteString("Use this knowledge to make better routing decisions:\n\n")
	b.WriteString(injection)
	b.WriteString("\n\n")
	b.WriteString("**Routing implications:**\n")
	b.WriteString("- When the user asks to add/register/create a new component, check the Registration Points above. ")
	b.WriteString("If the change requires modifying multiple files, prefer **edit-many** over edit.\n")
	b.WriteString("- When the user asks to modify existing code, use the Layers info to understand which files are in scope.\n")
	b.WriteString("- Respect mandatory conventions when suggesting file edits.\n")
	b.WriteString("- Entry points tell you what the project IS — use this to understand the user's intent.\n")

	return b.String()
}
