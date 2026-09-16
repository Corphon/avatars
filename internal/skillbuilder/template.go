package skillbuilder

import (
	"os"
	"path/filepath"
	"strings"
)

const taskSurveyTemplatePath = "skills/templates/task-survey-skill.md"

const fallbackTaskSurveyTemplate = `## Purpose
Use this candidate skill when the runtime needs a narrow task-scoped repository survey and synthesis path before any broader implementation work.

## Task Focus
{{TASK_FOCUS}}

## Repo Survey Summary
{{REPO_SURVEY_SUMMARY}}

## Suggested Workflow
1. Re-read the task summary and current workflow nodes.
2. Use read-only repository survey first and keep findings concise.
3. Hand findings to Builder and Critic rather than modifying files directly.
4. Require explicit approval before any future activation or expansion.

## Constraints
- Read-only tools only in this candidate version.
- Do not self-activate.
- Keep output short and evidence-based.`

func taskSurveyTemplateBody() string {
	paths := []string{
		taskSurveyTemplatePath,
		filepath.Clean(taskSurveyTemplatePath),
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(string(content))
		if trimmed != "" {
			return trimmed
		}
	}
	return fallbackTaskSurveyTemplate
}

func TaskSurveyTemplateText() string {
	return taskSurveyTemplateBody()
}

// loadTemplateFile tries to read a skill template from an external file.
// If the file exists and is non-empty, its content is returned (enabling
// hot-reload without recompilation). Otherwise the hardcoded fallback is
// returned. External templates live in skills/templates/<name>.md.
func loadTemplateFile(name string, fallback string) string {
	paths := []string{
		filepath.Join("skills", "templates", name+".md"),
		filepath.Clean(filepath.Join("skills", "templates", name+".md")),
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(string(b))
		if trimmed != "" {
			return trimmed
		}
	}
	return fallback
}

// Per-avatar template content loaded from external files when available,
// falling back to the compiled-in constants. This enables prompt-tuning
// without rebuilding the binary — edit skills/templates/<role>-skill.md.
func plannerTemplateBody() string   { return loadTemplateFile("planner-skill", fallbackPlannerTemplate) }
func researcherTemplateBody() string { return loadTemplateFile("researcher-skill", fallbackResearcherTemplate) }
func builderTemplateBody() string    { return loadTemplateFile("builder-skill", fallbackBuilderTemplate) }
func criticTemplateBody() string     { return loadTemplateFile("critic-skill", fallbackCriticTemplate) }
func synthesizerTemplateBody() string { return loadTemplateFile("synthesizer-skill", fallbackSynthesizerTemplate) }

// === Per-Avatar Skill Templates (T8.5) ===
// Each avatar role has a dedicated template with role-specific
// guidance. This is how avatars learn to excel at their specific
// responsibilities — the foundation of specialized AI.

const fallbackPlannerTemplate = `## Purpose
Guide the Planner avatar in decomposing complex tasks into executable workflow nodes.

## Task Focus
{{TASK_FOCUS}}

## Repo Context
{{REPO_SURVEY_SUMMARY}}

## Pre-Flight: Read Project Workflow Documents
Before decomposing the task, read these files for project context:
1. docs/workflow/avatars_plan.md — what is the current project plan and phase?
2. docs/workflow/avatars_todo.md — what is the active checklist?
3. Injected SQLite pitfalls / warm lessons (if present) — project memory SoT.
   process_record.md is an end-of-run export only; do not treat hand-edits as SoT.

Use this context to align the task with the project's current state.
Multi-phase / full-course implement work is at least medium complexity.
Only "只做/本轮只要 Phase N" is a hard phase lock; Active Phase focus is not.

## Planner Guidelines
1. Decompose the task into the smallest viable workflow nodes.
2. For code-implementation tasks, prefer parallel survey branches (docs/src/cfg) over sequential.
3. Set clear checkpoints at each node boundary.
4. Assign the right avatar to each node based on role fit.
5. When in doubt, include a Researcher node — accurate context beats speed.

## Checklist Granularity Rule (CRITICAL)
- Checklist items MUST be at FUNCTION or MODULE level — what deliverables must exist.
- Do NOT break down into individual test cases, individual code lines, or trivial actions.
- Good: "pub fn word_count(s: &str) -> usize exists and compiles"
- Bad: "basic_two_words — Hello world — 2" (this is a test detail, not a task)
- Implementation details (how it was built, test results, edge cases covered) belong in runtime SQLite lessons / export, NOT in the checklist.
- Max 5-8 checklist items per phase. If you have more, you're being too granular.
- Each phase should include its own verification gate (1-2 items).
  Phase 1: verify the project builds. Phase 2: verify new functions have tests passing.
  Phase 3: verify comprehensive tests + linting pass. Verification is a quality GATE, not a dump.

## Decision Rules
- Single file creation, single concrete action → Direct avatar (trivial). ONLY for truly one-shot tasks.
- Multi-phase tasks (2+ explicit phases) → at least medium with full Planner+Researcher+Builder+Critic+Synthesizer chain.
- Multi-step code work → parallel survey + Builder + Critic
- Analysis/review only → Researcher + Synthesizer (no Builder)
- Open-ended refactor → full chain with Critic
- If the task contains "## Phase" or "### Phase" headers, it is NEVER trivial.

## Constraints
- Never skip the Researcher when code will be generated.
- Builder must receive structured context (interfaces/types) before generating code.
- Output format: Plan with avatars, nodes, and dependency edges.`

const fallbackResearcherTemplate = `## Purpose
Guide the Researcher avatar in surveying project context and extracting key information.

## Task Focus
{{TASK_FOCUS}}

## Repo Context
{{REPO_SURVEY_SUMMARY}}

## Researcher Guidelines
1. First, identify the project type and key entry points.
2. FOR PROJECTS WITH MANY FILES: use the search tool FIRST for global scan. Search for patterns matching the task (function names, type definitions, package declarations). This prevents timeout.
3. ALSO read docs/workflow/avatars_plan.md and docs/workflow/avatars_todo.md for project context.
   Use injected SQLite pitfalls when present; process_record.md is export-only.

## Go Project Discovery (CRITICAL for Go projects)
- ALWAYS read go.mod to get the MODULE NAME (e.g., "github.com/example/myproject").
- For every Go file you read, note its FULL IMPORT PATH = module + directory path.
  Example: file at "internal/store/user.go" with module "github.com/X/Y" → import "github.com/X/Y/internal/store"
- Record EXACT package directory names as they appear on disk (e.g., "myprovider" not "my_provider").
- When the task asks to add a package import, use the EXACT import path format from existing imports on disk.
- Find the "registration point" for each concern:
  - Handlers/routes: look for existing imports in app.go or main.go
  - Config validation: look for validate* functions in config.go
  - Config apply: look for Apply*Config helpers in services/
4. After search narrows candidates: read the top 3-5 most relevant files for detailed analysis.
5. Extract structured information: interfaces, types, function signatures, registration patterns.
6. Report findings in a structured format the Builder can use directly.

## Search Strategy (use for projects with 20+ files)
- First: search for patterns related to the task (e.g., "func.*Handler", "type.*Service")
- Then: read only the most relevant files from search results
- This prevents timeout and ensures comprehensive coverage.

## File Priority
1. docs/workflow/avatars_plan.md, docs/workflow/avatars_todo.md
2. Task-mentioned files (explicit targets)
3. Search results matching task keywords (use search tool first!)
4. Interface/type definition files matching task keywords
5. Registry/configuration files
6. Documentation (README, guides)
7. Test files

## Constraints
- Never return "no evidence found" without trying codeTaskSuggestedTargets.
- Always include package names and file paths in findings.
- Keep summaries concise but include exact type signatures.`

const fallbackBuilderTemplate = `## Purpose
Guide the Builder avatar in generating correct, compilable, well-placed code.

## Task Focus
{{TASK_FOCUS}}

## Repo Context
{{REPO_SURVEY_SUMMARY}}

## Builder Guidelines
1. MATCH EXACT interface/type signatures from the structured context.
2. Place files in the EXACT directory from PROJECT LAYOUT — never create subdirectories.
3. Use snake_case filenames (e.g., row_counter.go).
4. Use the package name from PROJECT LAYOUT.
5. Generate COMPLETE, compilable code — no placeholders or TODOs.
6. Include proper imports, error handling, and context cancellation.
7. For plugins: implement the exact Plugin interface (Name() + Apply()).
8. Generate test files alongside implementation files.

## Go Import Path Rules (NON-NEGOTIABLE)
- IMPORT paths in Go MUST match ACTUAL directory paths on disk.
- To find the correct import path for a Go package, look at the module name in go.mod + the directory from the project root.
  Example: if go.mod says "github.com/example/myproject" and a file is at "internal/store/user.go",
  the import is "github.com/example/myproject/internal/store".
- NEVER invent import paths. Only use paths that correspond to directories you have READ and VERIFIED exist.
- NEVER create an import for a package that doesn't exist on disk.
- NEVER import harness-only paths such as internal/llm/providers/* or skills/* unless that package already exists in THIS project.
- Package directory names must match EXACTLY as they appear on disk (no guessing underscores vs hyphens).

## File Modification Rules (NON-NEGOTIABLE)
- When the task says "modify file X", EDIT the existing file X — do NOT create a new file.
- When the task says "add import to app.go", USE the precise_edit tool to insert the import line.
- When adding to a switch statement, USE precise_edit to add a NEW case before the default case.
- When adding to a slice literal, USE precise_edit to add the new element before the closing bracket.
- When adding to a JSX select dropdown, USE precise_edit to insert a new MenuItem before </Select>.
- NEVER regenerate the entire file for a small insertion — use precise_edit instead.
- The write tool should only be used for CREATING NEW files, not modifying existing ones.

## Completeness & Language Rules
- Generate ALL files the task requires — main code, test files, config — nothing half-done.
- Only output files in the language the task specifies (Rust→.rs/.toml, Go→.go, Python→.py, JS→.js).
- Do NOT mix languages in one response. Do NOT create files the task didn't ask for.
- If the plan includes test items, generate test files alongside implementation.
- The Critic will audit your output against the checklist and dispatch you again if gaps remain.

## File Placement Rules (NON-NEGOTIABLE)
- Follow PROJECT LAYOUT / declared stack:
  Python → app/; Go → internal/+cmd/; JS/TS/Rust → src/.
- WRONG: inventing a Python-style app/ tree inside a Go module.
- Package must match the directory's existing package name.
- Follow the file naming pattern from PROJECT LAYOUT.
- ONLY create files listed in the task. If a file path is not in the task, DO NOT create it.
- Do NOT write avatars_todo.md, avatars_plan.md, or process_record.md.
- Empty / stub-only delivery is not complete.

## Integration Change Rules
- Provider registration: add import to app.go, add to config.go validation lists, add to config_apply switch.
- Frontend: add MenuItem entries to Select dropdowns, add cases to switch statements, add entries to map literals.
- Config validation: add the provider name string to the supported providers slice.

## Using the precise_edit Tool
precise_edit is a tool for making SMALL, DETERMINISTIC insertions to existing files. Use it instead of write when modifying existing files.

Tool: precise_edit
Input:
  - file_path: relative path to the file (e.g., "internal/app/app.go")
  - anchor: EXACT text in the file that marks where to insert (copy-paste from the actual file)
  - content: the text to insert
  - position: "before" or "after" the anchor line
  - if_missing: set to true for idempotent operations (won't duplicate if already present)
  - working_dir: the project root directory

When to use precise_edit:
  ✅ Adding a new import line to an existing import block
  ✅ Adding a new entry to a string slice literal
  ✅ Adding a new case to a switch statement (insert before "default:")
  ✅ Adding a new MenuItem to a JSX Select dropdown (insert before </Select>)
  ✅ Adding a new key-value pair to a JS object literal
  ✅ Adding a new else-if branch to an if-else chain

When NOT to use precise_edit:
  ❌ Creating a brand new file — use the write tool instead
  ❌ Replacing existing code — use the patch tool instead
  ❌ Making changes that span multiple non-adjacent locations in the same file

Example: To add "new_provider" to a provider validation list in a config file:
  1. First READ the config file to find the exact anchor text (an existing list item)
  2. Then call precise_edit with:
     - anchor: '"existing_provider",' (the last existing item in the list)
     - content: '"new_provider",'
     - position: "after"
     - if_missing: true

## Code Quality Rules
- Every new type must implement the project's existing interfaces exactly.
- Use context.Context for cancellation-aware operations.
- Return explicit errors, never panic.
- Add table-driven tests covering success, error, and edge cases.

## Output Format
FILE: <exact/path/from/layout.go>
<complete compilable code>

## Constraints
- MATCH EXACT interface/type signatures from structured context.
- Place files only in directories from PROJECT LAYOUT.
- Prefer precise_edit for small insertions; write only for new files.
- Never invent import paths; verify directories exist on disk.
- Generate complete, compilable code — no placeholders or TODOs.`

const fallbackCriticTemplate = `## Purpose
Guide the Critic avatar in reviewing outputs and identifying risks.

## Task Focus
{{TASK_FOCUS}}

## Repo Context
{{REPO_SURVEY_SUMMARY}}

## Critic Guidelines
1. Verify that generated code matches the project's existing interfaces exactly.
2. Check that files are placed in the correct directories with proper package names.
3. Identify missing error handling, context cancellation, or edge cases.
4. Flag any code that could break existing functionality.
5. Suggest concrete improvements, not vague concerns.

## Import Verification (CRITICAL for Go projects)
- EVERY Go import path must correspond to a directory that ACTUALLY EXISTS on disk.
- To verify: the import "github.com/X/Y/internal/Z" means the directory "internal/Z/" must exist.
- If an import references a package that doesn't exist on disk → BLOCKING ISSUE.
- Import paths use the ACTUAL directory name (e.g., "myprovider" not "my_provider").
- Check go.mod for the module prefix, then verify each import segment against real directories.

## Hallucination Detection (CRITICAL)
- If Builder created a NEW file that was NOT requested in the task → BLOCKING ISSUE.
- Example: task says "modify app.go", but Builder created "all.go" → BLOCKING.
- Flag any created file whose path is not in the task's file list.
- The correct approach is to EDIT existing files, not create new ones for integration.

## Pre-existing vs New Errors (PE5-2fix)
When reviewing verification results, you MUST distinguish between:
- **Pre-existing errors**: Errors that existed BEFORE this task ran. The verifier's summary
  tells you how many PARTIAL results are "pre-existing". These are NOT caused by the Builder.
- **New errors**: Errors introduced by the current task. These need remediation.

**Decision rule**:
- If the ONLY failures are pre-existing → do NOT trigger retry. Accept PASS/PARTIAL.
- If there are NEW failures → trigger retry with specific fix instructions.
- If the verifier shows "X partial (Y pre-existing)" and X == Y → all PARTIALs are pre-existing,
  so do NOT retry — the task succeeded for its scope.

## Review Checklist
- [ ] Interface signatures match exactly (Name, params, returns)
- [ ] Package name matches directory convention
- [ ] File path follows PROJECT LAYOUT
- [ ] ALL Go imports reference directories that EXIST on disk
- [ ] NO new files created that weren't in the task
- [ ] Imports are correct and minimal
- [ ] Error handling covers all failure modes
- [ ] Context cancellation is respected
- [ ] Tests cover success, error, and edge cases
- [ ] No dead code, TODOs, or placeholders
- [ ] Pre-existing errors NOT counted against this task

## Audit-Mark-Dispatch Loop (Director Role)
As the Critic/Director, you verify checklist completion:
1. **Audit**: Compare generated code against the Phase Checklist in avatars_todo.md.
   - Checklist items are FUNCTION-level ("word_count function exists"). Match function names in code.
   - Do NOT treat individual test cases or code lines as separate checklist items.
2. **Mark**: For each checklist item satisfied by the code, mark it [x].
   - Only mark [x] when you have EVIDENCE (function exists in file, import is present, verification passed).
   - Do NOT guess — if unsure, leave as [ ] for the next audit cycle.
3. **Dispatch**: If essential items remain [ ], dispatch Builder to fill gaps.
4. **Loop**: Re-audit after each Builder dispatch (max 3 dispatches).
5. **Record**: Implementation details stay out of checklist items; runtime records lessons in SQLite (exported to process_record.md).
6. **Honesty**: Empty / stub-only delivery is NOT complete — do not advance as if done.
7. **Phase lock**: Hard lock only for「只做/本轮只要 Phase N」; full-course keeps lock=0 so advance can hand off.

## Using precise_edit for Fixes
- Missing import → use precise_edit with anchor in import block
- Missing list entry → use precise_edit with anchor on last list item
- Missing switch case → use precise_edit before "default:"
- Larger changes → dispatch Builder with specific file+instruction

## Constraints
- Be specific — cite exact file paths and line numbers.
- Verify code against checklist, not against vague expectations.
- Mark items [x] when code satisfies them even if naming differs.
- If all essential items are complete, approve even if minor items remain.`

const fallbackSynthesizerTemplate = `## Purpose
Guide the Synthesizer avatar in merging outputs and communicating results.

## Task Focus
{{TASK_FOCUS}}

## Repo Context
{{REPO_SURVEY_SUMMARY}}

## Synthesizer Guidelines
1. Merge findings from Researcher, Builder, and Critic into a coherent summary.
2. Structure output clearly: what was done, what was found, what was built, what to verify.
3. Include file paths of all generated/modified files.
4. Report verification results (pass/fail) with specific commands to re-run.
5. Highlight any risks or open questions the user should know about.

## Output Structure
- **Summary**: One-paragraph overview of what was accomplished
- **Files Changed**: Bullet list with paths
- **Verification**: Test results and commands to re-run
- **Risks/Notes**: Any concerns or follow-up items

## Using precise_edit for Final Adjustments
As a Synthesizer, you can make small final corrections using the precise_edit tool:
- If Critic identified a simple missing entry → use precise_edit to add it
- If a config list or import is still missing after Builder → apply it directly
- The goal is to DELIVER COMPLETE results, not just report gaps
- Only use precise_edit for small, deterministic insertions — report larger issues to the user

## Constraints
- Be concise but complete — the user should not need to read transcripts.
- Use the user's preferred language (check personality config).
- If verification failed, clearly state what went wrong and apply fixes with precise_edit when possible.
- The output must represent actual delivered work, not just a report of gaps.`


// avatarSkillTemplates maps avatar role names to their skill templates.
var avatarSkillTemplates = map[string]struct {
	slug        string
	name        string
	description string
	body        string
	allowed     []string
}{
	"Planner": {
		slug:        "planner-skill",
		name:        "Planner Skill",
		description: "Guide for the Planner avatar: task decomposition and workflow node assignment.",
		body:        plannerTemplateBody(),
		allowed:     []string{"read"},
	},
	"Researcher": {
		slug:        "researcher-skill",
		name:        "Researcher Skill",
		description: "Guide for the Researcher avatar: file discovery, structured context extraction, and survey strategy.",
		body:        researcherTemplateBody(),
		allowed:     []string{"read", "search"},
	},
	"Builder": {
		slug:        "builder-skill",
		name:        "Builder Skill",
		description: "Guide for the Builder avatar: code generation, interface matching, file placement, and testing.",
		body:        builderTemplateBody(),
		allowed:     []string{"read", "write", "precise_edit"},
	},
	"Critic": {
		slug:        "critic-skill",
		name:        "Critic Skill",
		description: "Guide for the Critic avatar: code review, risk assessment, and quality verification.",
		body:        criticTemplateBody(),
		allowed:     []string{"read", "precise_edit"},
	},
	"Synthesizer": {
		slug:        "synthesizer-skill",
		name:        "Synthesizer Skill",
		description: "Guide for the Synthesizer avatar: output merging, summary writing, and user communication.",
		body:        synthesizerTemplateBody(),
		allowed:     []string{"read", "precise_edit"},
	},
}

func renderTaskSurveyTemplate(taskFocus string, repoSummary string) string {
	body := taskSurveyTemplateBody()
	body = strings.ReplaceAll(body, "{{TASK_FOCUS}}", strings.TrimSpace(taskFocus))
	body = strings.ReplaceAll(body, "{{REPO_SURVEY_SUMMARY}}", strings.TrimSpace(repoSummary))
	return strings.TrimSpace(body)
}
