# Avatars CLI Guide

This guide records the currently shipped command-line surface for `avatars`.

## Configuration

### Bundle the runtime assets under `avatars/`

Recommended target-project layout:

```text
target-project/
  avatars/
    bin/
    configs/
    skills/
    web/
    CLI_guide.md
  .avatars/
```

Run commands from the target-project root, for example:

```powershell
.\avatars\bin\avatars.exe "Analyze this project, explain what it does, find issues, do not modify files, summarize in ana.md"
```

The runtime resolves bundled assets from `AVATARS_HOME` first, then `.\avatars\`, then legacy root paths. `configs/agent.yaml`, `skills/`, and `web/stage` may live inside the `avatars/` bundle. Project execution state still stays in the target root `.avatars/` directory so transcripts, memory, approvals, verifier reports, and task workspaces remain project-local.

If you need an explicit location, set:

```powershell
$env:AVATARS_HOME = "D:\path\to\target-project\avatars"
```

Default repository survey ignores the bundled `avatars/` directory so tool files such as `avatars/configs/agent.yaml` are not mistaken for target-project source evidence.

### Local plugin skill bundles

`avatars` can also read local plugin bundles from `.agents/plugins/marketplace.json` and expose any standardized `skills/*/SKILL.md` inside those bundles as runtime-visible skills.

Inspect the local plugin registry:

```powershell
avatars plugins list
```

List approved skills plus plugin-provided skills together:

```powershell
avatars skills list
```

### LLM action map

Use this compact command map when choosing bounded actions:

The machine-readable companion is `configs/cli_actions.yaml`. The intent router may include that YAML-derived action map in the LLM route prompt, while this Markdown guide remains the human-facing source of full usage detail.

- `avatars intent --from-file <plan.md>`
- `avatars run --from-file <plan.md>`
- `avatars repl [--task <task-id>] [--new-task] [--display compact|verbose]`
- `avatars intent --choose <n> --confirm "<intent>"`
- `avatars run --task <task-id> "<task>"`
- `avatars run --new-task --permission-mode plan "<task>"`
- `avatars run --resume <transcript-path> "<task>"`
- `avatars resume <transcript-path>`
- `avatars bootstrap [--apply] [--name <name>] [--module <module>] [--stack go-cli|python-cli|node-cli|rust-cli]`
- `avatars stage [--from-file <path>] [--prompt <direction>] [--serve] "<description>"`
- `avatars serve [--task <task-id>] [--resume <transcript-path>] [--permission-mode <mode>] [task]`
- `avatars verify [--race] [--task <task-id>]`
- `avatars feedback import-diagnostics <task-id> <json-file>`
- `avatars governance status [--task <task-id>]`
- `avatars memory status [--task <task-id>]`
- `avatars memory maintain --dry-run [--task <task-id>]`
- `avatars memory maintain --apply --confirm-archive [--task <task-id>]`
- `avatars memory archive-status [--task <task-id>]`
- `avatars memory archive-list [--task <task-id>]`
- `avatars memory archive-restore --dry-run --tombstone <tombstone-id> [--task <task-id>]`
- `avatars rollback inspect --artifact <path>`
- `avatars rollback apply --artifact <path> --confirm`
- `avatars tasks list`
- `avatars tasks show <task-id> [--view planner|synthesizer|verifier|governance|avatar:<avatar-id>]`
- `avatars tasks approve <task-id> [--approval-key <key>] [--replay]`
- `avatars tasks deny <task-id> [--approval-key <key>]`
- `avatars tasks retry-node <task-id> --origin-run <run-id> --pause-point <pause-point-id> --node <node-id> --pause-digest <digest> --dry-run`
- `avatars tasks delete <task-id>`
- `avatars llm providers`
- `avatars llm show <provider>`
- `avatars actions list [--path <cli-actions.yaml>]`
- `avatars actions show <action-id> [--path <cli-actions.yaml>]`
- `avatars route [--task <task-id>] [--new-task] "<intent>"`
- `avatars mcp list`
- `avatars mcp show <server-name>`
- `avatars mcp inspect [--task <task-id>] <server-name-or-url>`
- `avatars mcp call [--task <task-id>] <server-name-or-url> <method> [params-json]`
- `avatars shell [--permission-mode <mode>] <cmd> [args...]`
- `avatars write [--overwrite] [--permission-mode <mode>] <path> <content>`
- `avatars patch [--replace-all] [--permission-mode <mode>] <path> <old> <new>`
- `avatars git <subcommand> [args...]`
- `avatars skills status`
- `avatars skills list`
- `avatars skills pending`
- `avatars skills review <generated-file>`
- `avatars skills approve <generated-file>`
- `avatars skills archive <approved-file>`
- `avatars skills disable <approved-file>`
- `avatars skills restore <archived-or-disabled-file>`
- `avatars skills restore-missing <recorded-file-or-name> <source-markdown>`
- `avatars skills restore-guide <recorded-file-or-name>`
- `avatars skills resolve-missing <recorded-file-or-name>`
- `avatars skills repair-metadata <current-file>`
- `avatars skills repair-history <current-file>`
- `avatars skills repair-invariants <current-file>`
- `avatars skills timeline`
- `avatars skills ledger`
- `avatars skills reconcile`
- `avatars skills sync`
- `avatars skills promotion [--task <task-id>]`

Prefer these commands over ad hoc shell actions. They are the bounded control surface the runtime expects.

`avatars intent --from-file <plan.md>` prints a short confirmation line that the markdown file was loaded before routing begins.

## REPL

Start the interactive loop:

```powershell
avatars
avatars repl
```

Bare `avatars` now starts the same REPL loop.

Or start it explicitly:

```powershell
avatars repl
```

By default, normal input uses the stable task context `repl-session`.

Bind normal REPL input to a stable task workspace:

```powershell
avatars repl --task repo-analysis
```

Choose display mode at startup:

```powershell
avatars repl --display compact
avatars repl --display verbose
```

Compact is the default. It suppresses workflow progress and turn-status metadata in natural-language safe runs, then prints the readable `Answer`, `Full answer`, and necessary result location. Verbose shows the underlying run progress, task metadata, transcript path, verifier status, and suggested follow-up commands.

Inside the loop:

- `/help` shows REPL commands.
- `/history` shows recent REPL commands.
- `/load <plan.md>` feeds a markdown plan into the existing intent router, equivalent to `avatars intent --from-file <plan.md>`.
- `@<plan.md>` is a shorter alias for `/load <plan.md>`.
- `/verbose on` switches the current REPL session to verbose display.
- `/verbose off` switches the current REPL session back to compact display.
- `/intent ...` or `avatars intent ...` lets you run the explicit CLI syntax from inside REPL.
- `!!` replays the latest history item, and `!<number>` replays one item from `/history`.
- `/exit` or `/quit` leaves the loop.
- Normal natural-language input first goes through the shared natural-language router.
- The router asks LLM to classify ordinary text into `direct_answer`, `memory_answer`, `safe_run`, `clarify`, or `guarded`, then harness applies the chosen bounded path.
- Deterministic local questions may still be answered directly through bounded read-only handlers.
- Follow-up project opinion/status questions first try task memory and print `Memory recall:` when existing task memory is enough.
- Lightweight project opinion questions such as "what do you think of this repository?" or "这个仓库怎么样？" receive a bounded local answer instead of launching a full task.
- Other safe read-only questions route through a safe plan-mode task run in the active task workspace.
- Explicit read-only work requests such as analyze, inspect, find issues, write a report, or run tests route through `avatars run --permission-mode plan` unless a more specific command is used.
- Explicit mutating coding requests such as writing scripts, generating code, fixing code, or modifying files route through `avatars run --permission-mode default` so guarded write approval can engage instead of silently producing only a plan.

In compact mode, direct local answers do not print task status. Safe plan-mode runs print the answer preview and result location without flooding the terminal with workflow internals. In verbose mode, each routed task turn can print task status, latest transcript path, verifier summary, and suggested follow-up commands when the task workspace exists. If the default `repl-session` workspace does not exist yet, direct local answers may still complete without treating the missing task manifest as a command failure.

The REPL reuses existing bounded commands. It is not a free-form shell. Normal input now routes through the shared natural-language classifier first; the `safe_run` branch becomes `avatars run --task <active-task> --permission-mode plan "<input>"` for read-only work, `--permission-mode default` for explicit coding writes, or `avatars bootstrap ...` for empty-project scaffolding. Use explicit `/intent ...` or `avatars intent ...` when you want the CLI intent router instead. `/load <plan.md>` preserves the file-loaded cue and injects the active task marker when the plan does not already name a task. History is stored at `.avatars/repl/history.txt`.

If you use `avatars intent ...` with a natural-language question and the explicit CLI router has no candidate, it now uses the same shared natural-language router as REPL. Deterministic local questions can get direct answers; other safe questions fall back to plan-mode task execution. Risky or destructive intents still stay guarded.

## Bootstrap

Preview a new empty-project scaffold:

```powershell
avatars bootstrap --name demo-app
```

Apply the scaffold:

```powershell
avatars bootstrap --apply --name demo-app --module example.com/demo-app
```

Current bootstrap stacks:

- `go-cli`.
- `python-cli`.
- `node-cli`.
- `rust-cli`.
- `go-cli` writes `README.md`, `docs/architecture.md`, `coding_plan.md`, `process_record.md`, `go.mod`, `cmd/<name>/main.go`, and `cmd/<name>/main_test.go`.
- `python-cli` writes `README.md`, `docs/architecture.md`, `coding_plan.md`, `process_record.md`, `pyproject.toml`, `main.py`, and `test_main.py`.
- `node-cli` writes `README.md`, `docs/architecture.md`, `coding_plan.md`, `process_record.md`, `package.json`, `src/index.js`, and `test/index.test.js`.
- `rust-cli` writes `README.md`, `docs/architecture.md`, `coding_plan.md`, `process_record.md`, `Cargo.toml`, and `src/main.rs`.
- `--apply` blocks non-empty project roots, except `.git`, `.avatars`, and the bundled `avatars/` tool directory, then prints blocking entries and safe remediation options instead of ending with only an error.
- `--apply` writes `bootstrap_summary.md` in the project root for human follow-up.
- After writing, bootstrap runs the stack verifier and prints `Verifier: PASS`, `Verifier: FAIL`, or `Verifier: UNAVAILABLE`.
- In an empty project root, natural-language requests such as "搭一个空项目并生成代码" route to this bootstrap mode instead of repository analysis fallback.
- Natural-language bootstrap can extract common project names and module paths, for example `帮我搭一个叫 ledger-cli 的空项目` or `create project ledger-cli module example.com/ledger-cli`.
- Language follow-up such as `用python`, `用 node`, or `用 rust` can continue a bootstrap context and route to the requested stack.

## Script

Preview a small script scaffold:

```powershell
avatars script hello.py "write a simple script"
```

Write the script and run the built-in verifier:

```powershell
avatars script --apply hello.py "write a simple script"
```

Current script behavior:

- Supported script extensions: `.py`, `.js`, `.mjs`, `.ps1`, `.sh`.
- `--apply` refuses to overwrite an existing file.
- Python scripts run `python -m py_compile <path>` when Python is available.
- Node scripts run `node --check <path>` when Node is available.
- REPL natural-language requests such as `写一个简单脚本 hello.py` route to `avatars script --apply hello.py ...` instead of a multi-avatar plan-only run.
- Script intent is LLM-first when an LLM is configured: the model decides whether the request is a concrete script, the language, the topic, and a safe filename.
- Topic-specific scripts run through `avatars script --apply --llm ...` and use the configured LLM to generate code. If no LLM is available, Avatars refuses to write a generic fake template for that request.
- Deterministic fallback is only for generic placeholder scripts such as `写一个简单脚本 hello.py`, or for structural safety fallback when the LLM is unavailable.
- Location follow-up such as `你写的脚本在哪呢？` lists detected script files instead of launching a task.

### Approval Help

When a previous action is awaiting approval, ask `approval cli 指令是什么？` or `approval 指令是什么？`.

The REPL answers with the current pending commands, usually:

```powershell
avatars tasks show repl-session --view verifier
avatars tasks approve repl-session --replay
avatars tasks approve repl-session
avatars tasks deny repl-session
```

## Edit (single file)

Edit one file with a natural-language instruction:

```powershell
# Preview the edit (dry-run)
avatars edit README.md "把标题改成简体中文"

# Apply the edit
avatars edit --apply README.md "把标题改成简体中文"
```

`edit` is a focused single-file path: the LLM receives the file content plus instruction, produces a replacement, and the verifier checks the result. It does not launch a multi-avatar planning run.

- The instruction must reference an existing file path.
- `--apply` overwrites the target file after verification passes.
- The verifier runs both syntax checks and execution checks for the detected extension.

## Edit-Many (multi-file)

Edit multiple files with a single instruction:

```powershell
# Preview the edits (dry-run)
avatars edit-many "rename all functions from foo_ to bar_ in every Go file"

# Apply the edits
avatars edit-many --apply "rename all functions from foo_ to bar_ in every Go file"
```

`edit-many` asks the LLM to produce edits for multiple files in one pass:

- Supports at most **8 explicit file paths** in the instruction.
- The LLM returns a JSON map `{path: content}` — each path must exist on disk before the edit.
- The verifier runs on every changed file.
- `--apply` writes all files after verification.
- If no LLM is available, `edit-many` refuses instead of producing empty content.
- Duplicate paths and empty content from the LLM are rejected.

## Explicit Patches

REPL natural-language requests with an explicit file, old text, and new text can route directly to `avatars patch`:

```text
把 README.md 里的 old text 改成 new text
replace old text with new text in README.md
```

This route applies a bounded string replacement with `acceptEdits` and prints the patch result directly. It does not launch a multi-avatar planning run.

### Select the active LLM provider profile

Edit `configs/agent.yaml` (copy from `configs/agent.yaml.example`) and change the active provider name. Resolve secrets via `api_key_env` only — never commit plaintext `api_key` values.

```yaml
llm:
	active_provider: deepseek
```

### Define provider-specific LLM profiles

The `llm.providers` map can now keep multiple provider presets in one file. Each profile can override:

- `provider`
- `model`
- `enable_web_search`
- `think_mode`
- `api_key_env` (required for cloud providers)
- `api_key` (optional local override; gitignored `agent.yaml` only — do not commit)
- `base_url`

If `active_provider` is set, the selected profile is merged on top of the shared top-level `llm` defaults.

The default `configs/agent.yaml` now includes an `openrouter` profile that points at `https://openrouter.ai/api/v1`, uses `OPENROUTER_API_KEY`, and keeps a provider-qualified model name such as `openai/gpt-4.1-mini`.

You can also set `llm.timeout_ms` in `configs/agent.yaml` to raise or lower the live provider request timeout. The runtime defaults to 60000ms when the field is omitted.

When `openrouter` web search is enabled, the compatibility client sends the OpenRouter `web` plugin automatically; no extra CLI flag is required beyond the existing runtime web-search request path.

Provider profile loading now fails early when a selected provider is missing static required fields such as an explicit `model` or `base_url`. Missing API keys do not fail config loading because the runtime still supports deterministic fallback when credentials are absent.

### Inspect provider capabilities from the CLI

```powershell
avatars llm providers
```

This prints the shipped provider catalog with family, web-search support, explicit override requirements, and local-vs-remote status.

```powershell
avatars llm show openrouter
```

This prints one provider's detailed defaults, including model, base URL, API key environment variable, and whether explicit overrides are required.

### Provider quick reference

Use `avatars llm providers` and `avatars llm show <provider>` as the current source of truth for shipped provider defaults and requirements.

## Run Tasks

### Run from a bare user intent

```powershell
.\bin\avatars.exe "Inspect configs/agent.yaml and CLI_guide.md for runtime readiness drift."
```

Bare intent mode asks the intent router to select one existing CLI action. When a free-form task intent has no explicit `task=<task-id>` marker, the router now runs it as `avatars run --new-task ...` so stale task transcripts cannot be auto-resumed accidentally. When you want to reuse one stable workspace, include `task=<task-id>` in the intent or use explicit `avatars run --task <task-id> ...`.

`avatars run` now prints live progress lines to stderr while the run is active. Typical lines include `Running: ...`, `Researcher round ...`, `Reading: ...`, `LLM thinking: ...`, and verification status updates. Set `AVATARS_PROGRESS=0` to disable that progress output.

The progress stream also now surfaces avatar roles and workflow node activation, so you can see which avatar is active without opening the transcript.

Repository analysis uses a deterministic exploration chain: explicit file targets first, then ranked repository survey targets such as docs, manifests, entrypoints, domain logic, config, and CI files. Empty or minimal project roots produce a bootstrap report that says no repository evidence was found and points to the next setup files instead of pretending concrete issues were proven.

When multiple actions fit, it prints numbered candidates and waits for an explicit follow-up:

```powershell
.\bin\avatars.exe intent --choose 1 --confirm "inspect governance task=demo-task"
```

This mode does not create a free-form shell executor. It only routes to existing bounded CLI commands such as `run`, `verify`, `memory status`, `memory maintain --dry-run`, `governance status`, `tasks show`, `skills status`, and `skills reconcile`. Ambiguous or higher-risk choices require confirmation.

When a live LLM provider is configured, the intent router can use it to choose or ask for clarification from the bounded candidate list. If the provider is unavailable or falls back deterministically, local rules still provide the candidate list.

## Governance

Inspect harness governance status:

```powershell
avatars governance status
avatars governance status --task <task-id>
avatars tasks show <task-id> --view governance
```

These commands are read-only. They report runtime truth readiness, memory lifecycle readiness, skill governance readiness, verifier readiness, workflow node execution evidence, retry closure readiness when present, retry closure verifier report path when present, a bounded workflow node next-action cue when evidence supports one, summary counts, blockers, and the guardrail that no skills, memory, runtime truth, verifier records, or tombstones are mutated. Use the task view before risky task-level actions such as memory restore apply, skill repair/sync, or retry expansion.

For task-scoped governance, `Runtime truth: task_status=...` is a derived finality status from task-local memory. It can report:

- `awaiting_approval`: pending guarded approval blocks continuation.
- `awaiting_verification`: latest non-pass verifier result still needs reverify coverage.
- `awaiting_retry_closure`: failed-node retry closure evidence is incomplete.

When a derived finality blocker is present, `Governance readiness` reports `runtime_truth=review` instead of `runtime_truth=ok`. `Governance blockers` includes approval and retry-closure finality blockers. `awaiting_verification` is not double-counted when `verifier=pending` already reports the verifier blocker.

## Memory Lifecycle

Inspect project memory DB:

```powershell
avatars memory status
```

Inspect one task memory DB:

```powershell
avatars memory status --task <task-id>
```

This command is read-only. It reports DB path, DB size, table row counts with timestamp ranges, current mechanical pruning limits, advisory retirement candidates, and protected truth counts. `Memory maintenance mode: dry_run_with_guarded_archive_apply` means status inspection itself does not mutate data, while advisory archive apply exists only behind explicit `memory maintain --apply --confirm-archive`.

Preview memory maintenance:

```powershell
avatars memory maintain --dry-run
avatars memory maintain --dry-run --task <task-id>
```

This command is read-only. It reports retirement candidates, reusable memory kept, protected truth skipped, and the guardrail text. It previews the same advisory candidate classes used by guarded archive apply, but it does not delete, archive, retire, or vacuum records. Reusable memory is not protected by a permanent tag. The current policy keeps it because it still has useful evidence, while future maintenance may reclassify it after confidence, support, age, contradiction, or supersession changes.

Inspect archive/tombstone readiness:

```powershell
avatars memory archive-status
avatars memory archive-status --task <task-id>
```

This reports whether the archive/tombstone schema is ready, tombstone counts, advisory tables eligible for archive, and protected truth classes that stay outside archive/delete scope. This is read-only; no records are moved.

List archive tombstones:

```powershell
avatars memory archive-list
avatars memory archive-list --task <task-id>
```

This command is read-only. It lists tombstones with table, key, reason, state, archived time, source, and summary. It does not restore records and does not mutate hot recall.

Inspect one restore candidate:

```powershell
avatars memory archive-restore --dry-run --tombstone <tombstone-id>
avatars memory archive-restore --dry-run --tombstone <tombstone-id> --task <task-id>
```

This command is read-only. It reports the tombstone target, whether a matching hot advisory record already exists, restore support status, and the protected-truth guardrail. Missing `--dry-run` or missing `--tombstone` is rejected. It does not restore records.

Apply archive candidates:

```powershell
avatars memory maintain --apply --confirm-archive [--task <task-id>]
```

This is the only apply grammar. `--apply` without `--confirm-archive` is rejected, and `--confirm-archive` without `--apply` is rejected. Apply writes deterministic tombstones before advisory records leave hot recall. Scope is limited to `warm_lessons`, `evolution_candidates`, and `project_lessons`. Protected runtime/verifier truth stays untouched. No vacuum path is run.

### Build a local MVP binary

```powershell
go build -o .\bin\avatars.exe ./cmd/avatars
```

### Run MVP smoke checks with logs

```powershell
$logDir = "logs/mvp-shakedown-2026-05-08"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
.\bin\avatars.exe llm providers *> "$logDir\02-llm-providers.log"
.\bin\avatars.exe tasks list *> "$logDir\03-tasks-list.log"
.\bin\avatars.exe verify *> "$logDir\04-verify.log"
```

### Run one MVP shakedown task

```powershell
.\bin\avatars.exe run --task mvp-shakedown --permission-mode plan "Read configs/agent.yaml and CLI_guide.md. Summarize current runtime readiness gaps." *> "logs\mvp-shakedown-2026-05-08\05-run-plan-task.log"
.\bin\avatars.exe tasks show mvp-shakedown *> "logs\mvp-shakedown-2026-05-08\06-tasks-show-mvp-shakedown.log"
.\bin\avatars.exe tasks show mvp-shakedown --view verifier *> "logs\mvp-shakedown-2026-05-08\07-tasks-show-verifier.log"
```

In `--permission-mode plan`, mutating write actions are expected to be denied. Treat that as a successful safety-boundary smoke signal when `tasks show` still records the transcript, telemetry, permission denial, and suggested follow-up.

### Run REPL routing smoke check

```powershell
.\bin\avatars.exe smoke repl-routing --new-task
.\bin\avatars.exe smoke coding-gate
```

This is read-only. It checks the local REPL router against fixed inputs and prints the expected routing kind for direct answers, memory answers, safe runs, guarded requests, and clarify cases. It does not call the LLM or launch a task.

`smoke coding-gate` is also read-only. It checks the minimum gate before coding work: coding-like input routes to plan mode, UX repair questions answer directly, report proof rejects unanchored findings, and unrelated vague questions clarify instead of launching work.

### Inspect CLI action map

```powershell
avatars actions list
avatars actions show analyze_readonly
avatars actions show route_preview
avatars actions show memory_recall_check
avatars actions show smoke_repl_routing
avatars actions show smoke_coding_gate
avatars actions show git_diff
avatars actions show mcp_list
```

This is read-only. It prints the structured action map already used by the intent router prompt, so you can see which bounded commands are available without reading YAML by hand.

The action map includes common read-only diagnostics for routing smoke checks, git status/diff/log, LLM provider profiles, MCP inventory, memory recall, verifier evidence, and target-project shakedown. These entries point to existing bounded CLI commands; they are not shell templates.

### Preview one route

```powershell
avatars route "分析项目，找问题，总结写入 outcome.md"
avatars route "这个项目是干什么的"
```

This is read-only. It previews the route kind, chosen command or answer path, and whether the runtime would execute a task or stay local. No command is run.

### Target-project routing shakedown

Run this from a target project after copying the `avatars/` bundle:

```powershell
.\avatars\bin\avatars.exe actions list
.\avatars\bin\avatars.exe actions show target_project_shakedown
.\avatars\bin\avatars.exe intent "smoke repl routing"
.\avatars\bin\avatars.exe intent "git diff"
.\avatars\bin\avatars.exe route "这个项目是干啥的，你能找到哪些问题，分析总结写入 outcome.md"
.\avatars\bin\avatars.exe smoke repl-routing --new-task
.\avatars\bin\avatars.exe smoke coding-gate
```

Expected shape:

- `actions list` includes `route_preview`, `target_project_shakedown`, `report_evidence_check`, and `memory_recall_check`.
- `intent "smoke repl routing"` and `intent "git diff"` route to bounded read-only commands.
- `route` shows whether the request would stay local, use memory, or run a plan-mode task before anything executes.
- `smoke repl-routing` stays read-only and does not call the LLM.
- `smoke coding-gate` stays read-only and blocks coding experiments until routing, UX repair, report proof, and plan-mode boundaries pass.

After one real analysis task exists, inspect recall before repeating repository survey:

```powershell
.\avatars\bin\avatars.exe memory status --task repl-session
.\avatars\bin\avatars.exe route --task repl-session "你认为这个项目构建的如何？"
.\avatars\bin\avatars.exe tasks show repl-session --view verifier
```

For follow-up opinion or status questions, prefer memory-backed answers. Run a fresh survey only when the user asks for a new analysis, memory lacks support, or project files changed.

### Start a new ad-hoc run

```powershell
avatars run "Analyze the current repository and propose a refactoring plan"
```

For practical read-only issue hunting, prefer:

```powershell
avatars run --new-task --permission-mode plan "Analyze this repository, explain what it does, and report concrete issues. Do not modify files."
```

If the intent explicitly says read-only or do not modify, the router now adds `--permission-mode plan` automatically.

Repository analysis and issue-hunting intents expand the read-only survey beyond one bootstrap file. When available, the runtime reads multiple project-facing files such as README/docs, module manifests, command entrypoints, and config files before synthesis.

If the intent asks for a markdown report such as `ana.md`, the run now emits that file at the requested path with the final synthesized analysis content instead of leaving only the transcript JSONL.

### Bind a run to a stable task workspace

```powershell
avatars run --task repo-refactor "Analyze the current repository and propose a refactoring plan"
```

### Force a fresh task workspace

```powershell
avatars run --new-task "Analyze the current repository and propose a refactoring plan"
```

### Continue from an explicit transcript

```powershell
avatars run --resume .avatars/tasks/repo-refactor/sessions/20260428-081900.489765200.jsonl "Continue the repository analysis"
```

### Run with an explicit permission mode

```powershell
avatars run --permission-mode plan "Inspect the repository and propose a remediation plan"
```

Current permission modes are `default`, `acceptEdits`, `dontAsk`, `plan`, and `bypassPermissions`.

`avatars run` now prints the resolved permission mode in its task summary so the operator can distinguish a planning-only run from one that can execute guarded edits.

When `avatars run` has a writable skill store available, candidate skill generation now routes its markdown file write through the guarded `write` tool instead of bypassing the tool pipeline. In practice that means explicit `--permission-mode default` runs can now stop in a real approval-required state while trying to persist the generated skill candidate, rather than silently writing the file and only treating approval as an activation concern.

Skill candidate signals now also enter task-local evolution memory. Plan-mode `skill.candidate_prepared` events create a medium-priority `skill_promotion` backlog item, while generated skill files create a high-priority `skill_promotion` backlog item. The backlog is advisory only: it does not auto-approve, auto-activate, or mutate skill templates. Inspect it with `avatars tasks show <task-id> --view planner` or any view that prints evolution candidates.

For a direct operator queue, use `avatars skills promotion [--task <task-id>]`. It prints task-local `skill_promotion` items, with priority counts and a read-only queue view. Without `--task`, it scans known task workspaces and prints queues per task.

When a run stops that way, the blocked run now still returns a partial transcript result internally and the CLI records that transcript back into the stable task workspace before surfacing the approval-required error. In practice, `avatars tasks show <task-id>` can now keep pointing at the blocked run's latest transcript and summary instead of losing linkage whenever the run stops at an approval gate.

Blocked approval-required runs now also emit a terminal run status of `awaiting_approval` and write a matching stable run boundary. In practice, stage run history and operator snapshots can distinguish an approval-blocked stop from a generic failed run while still preserving the blocked transcript summary.

### Runtime status meanings

- `stable`: task workspace has no newer lifecycle or pending-approval evidence overriding the manifest status.
- `awaiting_approval`: the origin run stopped at a guarded action boundary and has a pending approval request.
- `continued`: an approved guarded request was replayed and the origin run lifecycle was updated with continuation lineage.
- `completed`: the run reached a normal stable boundary.
- `completed_unverified`: the run completed with a partial verifier verdict or optional verifier policy instead of a full PASS.
- `needs_remediation`: verifier FAIL blocked normal completion after a mutating action.

## Resume Inspection

Three resume surfaces — they are not interchangeable:

| Command | Mode | What it does |
|---|---|---|
| `avatars resume <transcript>` | **restore-only** | Reloads summary/memory and prints `BoundaryType`. Does **not** continue the DAG. |
| `avatars run --resume <transcript> "<task>"` | **soft-replan** | `RunWithResume` → `runOnce` with restored memory (usually re-plans). |
| `avatars run --continue-from-pause <transcript>` | **continue-from-pause** | Re-enters the run with restored memory. Approval DAG continuation still uses `approval --replay` / `ContinueApprovedToolCall`. |

### Restore summary state from a transcript

```powershell
avatars resume .avatars/tasks/repo-refactor/sessions/20260428-081900.489765200.jsonl
```

The CLI prints `Resume mode: restore-only` and `Boundary type: …`. To actually continue work, use `run --resume` or `run --continue-from-pause`.

Expected output can include:

- restored stable summary
- task summary
- avatar summary count
- recent key event count
- latest run LLM status from task-local hot memory, so operator inspection can compare the last persisted run configuration against the current config file
- current configured LLM provider state, including provider family, resolved model, think mode, and any config warning when the current `agent.yaml` is invalid
- latest telemetry snapshot with duration, execution counters, provider/model, and usage availability
- evaluation record count
- warm lesson count
- evolution candidate count
- project lesson count
- top project lesson support count when repo-level evidence is available
- structured memory path
- resume transcript path

## Task Workspaces

### List task workspaces

```powershell
avatars tasks list
```

Output columns:

- task id
- task status
- run count
- task title

### Show one task workspace

```powershell
avatars tasks show repo-refactor
```

### Inspect one task workspace through a query policy

```powershell
avatars tasks show repo-refactor --view verifier
```

```powershell
avatars tasks show repo-refactor --view avatar:avatar-builder
```

### Approve the latest pending guarded action for a task

```powershell
avatars tasks approve repo-refactor
```

### Approve one specific pending guarded action by approval key

```powershell
avatars tasks approve repo-refactor --approval-key approval-key-demo
```

### Approve and replay the latest pending guarded action for a task

```powershell
avatars tasks approve repo-refactor --replay
```

### Approve and replay one specific pending guarded action by approval key

```powershell
avatars tasks approve repo-refactor --approval-key approval-key-demo --replay
```

### Deny the latest pending guarded action for a task

```powershell
avatars tasks deny repo-refactor
```

### Deny one specific pending guarded action by approval key

```powershell
avatars tasks deny repo-refactor --approval-key approval-key-demo
```

Expected output can include:

- task id and title
- derived task status from task-local hot memory, so unresolved approval gates can surface as `awaiting_approval` instead of leaving operators with the stale manifest-only `stable` or `new` status
- task root, sessions directory, memory directory
- latest transcript path
- latest run summary
- stable memory summary from SQLite hot memory
- task memory summary from SQLite hot memory
- avatar summary count from SQLite hot memory
- recent key events from task-local SQLite hot memory
- bounded execution chain derived from planning-related recent events, so operators can scan decomposition, assignment, and handoff steps without replaying raw transcripts
- latest avatar report, ask, challenge, and summary derived from typed collaboration messages already stored in task recent events, with bounded route cues preserved in the summary text so operators can distinguish directed asks from broadcast status signals without replaying raw transcripts
- latest avatar follow-up derived from the same bounded collaboration stream, so operators can see when the latest directed ask has already received a deterministic response without replaying raw transcripts
- latest avatar report, ask, challenge, and summary route cues surfaced as separate inspection lines when available, so operators can see the latest directed target or broadcast scope without rereading the full collaboration sentence
- latest avatar follow-up route cue surfaced as a separate inspection line when available, so operators can see who received the bounded follow-up without rereading the whole message
- latest avatar ask status surfaced as a separate inspection line when available, so operators can tell at a glance whether the latest directed ask is still pending or already followed-up
- latest run LLM status from task-local hot memory, including persisted provider/model/think-mode state and any warning captured for that run
- current configured LLM provider state from the active `agent.yaml`, so operator inspection can compare current config with historical telemetry
- latest telemetry snapshot from task-local SQLite hot memory, including duration, LLM/tool/verifier counters, and either concrete usage totals or an explicit `usage: unavailable` line when the provider does not surface token/cost data
- latest verifier report path when verifier evidence has been recorded
- latest verifier follow-up command when the latest verifier verdict is non-pass, so operators can rerun verification deterministically from `tasks show` with the current task id already bound into the command
- latest non-pass verifier summary and follow-up, so operators can still see the most recent failing or partial verification trend even after a later PASS has become the latest snapshot
- reverify status, so operators can tell whether the latest non-pass verifier result is still awaiting another rerun or has already been covered by a later PASS
- latest reverify attempt summary when a write, patch, or guarded mutating shell command has completed after the latest non-pass verifier signal but before a covering rerun, so operators can see explicit remediation progress without scanning raw evaluation records first
- latest reverify attempt target list when the remediation evidence records explicit expected targets, so operators can see which file paths the fix was trying to touch without opening the raw evaluation record details
- latest reverify remediation summary when the loop is already closed by a later PASS, so operators can still see the most recent remediation step that preceded closure instead of losing provenance at the moment the rerun passes
- latest reverify remediation target list when the covered remediation evidence recorded explicit expected targets, so operators can still see the intended fix scope after the later PASS closes the loop
- latest reverify closure summary when the loop is already closed by a later PASS, so operators can see explicit closure evidence without scanning raw evaluation records first
- recovery summary with compact category, action, guard, closure, authority, source, pause/resume coordinates, targets, and non-authoritative guidance when recovery evidence exists, so operators can distinguish inspection facts from executable commands
- latest approval-required summary when the latest guarded action is blocked pending approval rather than refused outright, so operators can distinguish approval-needed work from sandbox or write-policy denials without opening raw evaluation details
- latest approval-required source when that approval-needed record names a concrete decision source such as `runtime_permission_mode`, so operators can tell whether the block came from the selected mode instead of a narrower sandbox guard
- latest approval-required key and permission mode when those details are present on the pending request, so operators can target one specific approval-needed action without reopening raw evaluation record details
- latest approval replay summary, source, key, and replay transcript path after `avatars tasks approve ... --replay` succeeds or fails, so operators can audit the replayed guarded request separately from both the original pending-approval record and the task's generic latest transcript line
- generated skill replay finalization when the replayed guarded write targets `skills/generated/...`, so approval replay does not stop at the markdown file and instead also restores the generated skill history sidecar and governance finalize step
- bounded pending approval queue details, including per-request approval keys, tool/operation labels, mode, and decision source, so operators can distinguish one unresolved guarded action from another inside the same task
- suggested approval commands when the latest guarded action is still pending approval, so operators can resolve the current `default`-mode block with `avatars tasks approve <task-id>` or `avatars tasks deny <task-id>` and, when replay metadata is available, use `--replay` to execute the original guarded request immediately after approval; when more than one request is pending, deterministic `--approval-key <key>` selectors keep that resolution bounded to one request
- latest permission denial summary when the latest guarded action was refused by the sandbox or a concrete write guard, so operators can distinguish a denied action from a generic execution failure without opening raw evaluation details
- latest permission denial source when that denial records a concrete decision source such as `tool_sandbox`, so operators can tell whether the refusal came from the runtime class policy or a narrower sandbox guard
- a bounded remediation proposal list derived from the current verifier state, reverify status, and the latest remediation evidence, so operators can inspect proposal ids, inspect-vs-remediate summaries, intended targets, and deterministic follow-up commands without reconstructing planner context manually
- a bounded suggested command list that deduplicates the current deterministic verifier-view and rerun follow-ups, so users do not need to memorize which `avatars ...` inspection or rerun command should come next
- bounded verifier history count for the current task
- evaluation record count from task-local memory
- warm lesson count from task-local memory
- evolution candidate count from task-local memory
- project lesson count from repo-level memory
- top project lesson support count from repo-level memory
- role-scoped query views for `planner`, `synthesizer`, `verifier`, or `avatar:<avatar-id>`
- verifier views can include the latest verifier verdict, summary, report path, checks, warnings, and bounded verifier history from task-local hot memory
- verifier views now also include a deterministic task-scoped follow-up command for non-pass latest or historical verifier entries, so operators can rerun verification from the inspection surface instead of reconstructing the next step manually
- verifier views now also include the latest non-pass verifier summary and follow-up, so the operator can spot the current failure trend without scanning the full bounded history line by line
- verifier views now also include a bounded reverify status line, so the operator can tell whether the latest non-pass verifier signal is still pending or has already been superseded by a later PASS
- verifier views now also include the latest reverify attempt summary when remediation work has started but the loop is still pending, so progress stays explicit before the next rerun lands
- verifier views now also include the latest reverify attempt target list when the remediation evidence records explicit expected targets, so operators can inspect intended fix scope without opening raw evaluation details
- verifier views now also include the latest reverify remediation summary when the loop is already closed, so the operator can keep the final remediation provenance visible alongside closure evidence
- verifier views now also include the latest reverify remediation target list when the covered remediation evidence records explicit expected targets, so intended fix scope remains visible after closure
- verifier views now also include the latest reverify closure summary when the current verifier state already covers the last non-pass signal, so loop-closure evidence stays explicit instead of being inferred from record ordering
- verifier views now also include the same recovery summary and non-authoritative guidance as full task inspection, so recovery facts remain visible beside verifier evidence without implying automatic retry
- verifier views now also include the latest approval-required summary and decision source when the task recently hit a mode-gated mutating action that still needs approval, so operators can inspect approval-needed guarded actions without replaying raw tool transcripts
- verifier views now also include the latest permission denial summary and decision source when the task recently hit a sandbox or write-policy refusal, so operators can inspect denied guarded actions without replaying raw tool transcripts
- verifier views now also include the same bounded remediation proposal list as the full task view, so the operator can inspect proposal ids, inspect-vs-remediate summaries, intended targets, and deterministic follow-up commands directly beside the verifier evidence
- verifier and non-verifier query views now also include a bounded suggested command list, so users can see the next deterministic `avatars ...` command without remembering whether they should open the verifier view or rerun verification first
- planner, synthesizer, verifier, and avatar query views can now also print the same bounded execution chain derived from recent planning summaries, so task-scoped inspection stays aligned between summary and role-specific views
- planner, synthesizer, verifier, and avatar query views can now also print the latest avatar report, ask, challenge, and summary derived from typed avatar collaboration messages, with route cues preserved in the summary text so role-scoped inspection can still tell whether the newest signal was directed or broadcast without opening transcripts
- planner, synthesizer, verifier, and avatar query views can now also print the latest avatar follow-up derived from the bounded collaboration stream, so role-scoped inspection can tell when the current directed ask already has a visible response
- planner, synthesizer, verifier, and avatar query views can now also print the latest avatar route cues as separate lines, so role-scoped inspection can spot `to Planner` or `broadcast task` directly instead of reparsing the collaboration summary mentally
- planner, synthesizer, verifier, and avatar query views can now also print the latest avatar follow-up route cue as a separate line, so role-scoped inspection can spot `to Researcher` directly when the bounded follow-up path is present
- planner, synthesizer, verifier, and avatar query views can now also print the latest avatar ask status as a separate line, so role-scoped inspection can see `pending via ...` or `followed-up via ...` directly instead of inferring it from message order
- planner, synthesizer, and verifier views can include the latest telemetry snapshot for the current task, so operator inspection can see duration and execution counters without opening raw transcripts
- planner, synthesizer, and verifier views can include structured evaluation records derived from verifier verdicts, verifier warnings, and failing or partial verifier check output
- when a later verifier PASS covers the latest non-pass verifier signal for the same task, task-local evaluation records now also include a structured reverify-closure evidence row instead of leaving that loop closure implicit in history ordering alone
- when a write, patch, or guarded mutating shell command completes for a task whose latest verifier signal is still non-pass, task-local evaluation records now also include a structured reverify-attempt evidence row so remediation progress is visible before the next rerun
- when the latest non-pass verifier signal is later covered by a PASS, the existing reverify-attempt evidence is now still surfaced as remediation provenance instead of disappearing from inspection as soon as the closure record arrives
- structured evaluation records now retain the originating tool context, and the current rule-based experience miner can turn those records into evolution candidates without mutating runtime or templates automatically
- planner and synthesizer views can include bounded warm lessons from task-local memory
- planner and synthesizer views can include bounded evolution candidates from task-local memory
- planner and synthesizer views can include promoted project lessons from repo-level memory, ranked by cross-task support count, confidence, source priority, and recency

### Delete one task workspace

```powershell
avatars tasks delete repo-refactor
```

This removes the entire `.avatars/tasks/<task-id>/` folder.

## Feedback

### Import diagnostics into a stable task workspace

```powershell
avatars feedback import-diagnostics repo-refactor diagnostics.json
```

The diagnostics file should be a JSON array. Each entry can include:

- `tool`
- `severity`
- `summary` or `message`
- `path`
- `line`
- `column`
- `code`
- `source`

Imported diagnostics are written into task-local evaluation memory and immediately used to generate deterministic evolution candidates.
Imported diagnostics now also persist one deterministic warm lesson so planner and synthesizer retrieval can see the strongest imported diagnostic without waiting for another run.
When the imported diagnostics yield high-confidence warm lessons or high-priority evolution candidates, they are now also promoted into repo-level project lessons immediately.

## Verification

### Run verifier checks

```powershell
avatars verify
```

This prints the structured verifier report.

By default, `avatars verify` runs the CGO-free checks: `go test ./...` and `go vet ./...`.

### Run optional race verification

```powershell
avatars verify --race
avatars verify --race --task repo-refactor
```

Use `--race` when you want the optional deep check that includes `go test -race ./...`. That path still needs CGO.

### Run verifier checks and persist them to a stable task workspace

```powershell
avatars verify --task repo-refactor
```

This still runs verifier commands against the current repository root, but it persists the resulting verifier report, verdict, and evaluation feedback into `.avatars/tasks/<task-id>/memory/` so `avatars tasks show <task-id>` and the stage operator panel can reflect the reverify result.

## Creative Stage

Stage is a **visual sketchbook**, not project source. Use it to see an idea, a repo map, or a file as a self-contained HTML card. Implementing features still goes through `run` / `script` / `edit`.

`avatars serve` (below) is the **operator dashboard** on port 5000. The creative gallery is `avatars stage --serve` on port **5100**.

```powershell
# Idea → one HTML card (same idea overwrites the same card; --new forces another)
avatars stage "一个深邃的星空，流星划过，星座闪烁"

# Project map (plan + file tree; architecture.md is auxiliary)
avatars stage --project

# Multi-file change patterns from architecture.md
avatars stage --reg-points

# From a file + direction
avatars stage --from-file story.txt --prompt "做成交互叙事"

# Revise the same card
avatars stage --edit latest "做成像素风"

# Gallery (Create / Revise / Delete in the browser)
avatars stage --serve
avatars stage --list
avatars stage --delete latest
```

Cards live in `stage/` (`stage-xxxxxxxxxxxx.html` + `.json`). Open the HTML file directly, or browse `http://127.0.0.1:5100`.

### Start the read-only operator dashboard

```powershell
avatars serve "Analyze the current repository and propose a refactoring plan"
```

### Start the stage against one stable task workspace

```powershell
avatars serve --task repo-refactor "Continue repository inspection"
```

This loads archived runs from `.avatars/tasks/<task-id>/sessions/` into the stage timeline before the new live run starts.

### Start the stage with one explicit archived transcript

```powershell
avatars serve --resume .avatars/tasks/repo-refactor/sessions/20260428-081900.489765200.jsonl "Review this archived run"
```

### Start the stage with an explicit permission mode

```powershell
avatars serve --permission-mode plan "Inspect the repository in read-only planning mode"
```

This lets the stage timeline include one explicit transcript even when you are not following the full task workspace.

The stage server listens on `http://127.0.0.1:5000`.

`avatars serve` is still a local CLI command that starts a long-lived HTTP process. It is not a cloud service by itself. If you want a server deployment, run the binary under a service manager or reverse proxy, then call the CLI against that process.

The stage now includes an operator panel that shows:

- archived runs loaded from a stable task workspace or one explicit transcript when `avatars serve` is started with `--task` or `--resume`
- configured LLM state from the current `agent.yaml`
- a run timeline that summarizes each in-memory run with provider, model, counts, timestamps, and summary, and lets the operator jump directly into one run view
- a run selector that can follow the latest run or pin one historical run from the current event history
- the selected run LLM state from runtime events
- the selected run telemetry snapshot from runtime events
- task-scoped feedback from task-local memory, including the latest verifier verdict plus evaluation, warm lesson, evolution, and project lesson counts when `avatars serve` is attached to a stable task workspace
- task-scoped feedback now includes read-only finality metadata: `task_status`, `finality_readiness`, and `finality_blocker_count`, using the same shared contract as CLI governance
- task-scoped feedback in the stage now also includes the latest verifier follow-up command when the latest verdict is non-pass, and that command is now task-scoped (`avatars verify --task <task-id>`), so the read-only operator panel can point back to the exact reverify path without inventing a write control
- task-scoped feedback in the stage now also includes the latest non-pass verifier summary and follow-up, so the operator panel preserves recent verification drift context even after the latest snapshot flips back to PASS
- task-scoped feedback in the stage now also includes a reverify status line, so the operator panel can distinguish between an outstanding latest non-pass verifier signal and one that has already been covered by a later PASS
- task-scoped feedback in the stage now also includes the latest reverify attempt summary when remediation work has started but the latest non-pass signal still awaits rerun coverage
- task-scoped feedback in the stage now also includes the latest reverify remediation summary when the loop is already closed, so remediation provenance stays visible next to closure evidence after the rerun passes
- task-scoped feedback in the stage now also includes the latest reverify closure summary when the loop is already closed, so the operator panel can show explicit loop-closure evidence without falling back to the generic latest-evaluation line
- task-scoped feedback in the stage now also includes the latest approval-required summary plus decision source when a guarded action is blocked pending approval by the selected permission mode, so the operator panel can distinguish approval-needed actions from hard denials
- task-scoped feedback in the stage now also includes the latest approval-required key, permission mode, and bounded pending approval queue metadata, so the operator panel can tell whether one or several unresolved guarded actions still need review before the operator returns to CLI
- task-scoped feedback in the stage now also includes the latest permission denial summary plus decision source when a guarded action is refused by the sandbox, so the operator panel can distinguish deny provenance from generic tool failures
- task-scoped feedback in the stage now also includes a bounded remediation proposal list, so the read-only operator panel can surface current inspect-vs-remediate proposal ids, intended targets, and deterministic follow-up commands without inventing write controls
- task-scoped feedback in the stage now also includes the shared workflow node next-action cue and retry closure verifier report path from runtime/verifier evidence, exposed as read-only inspection metadata rather than a retry, approval, or verifier control
- repository-scoped skills governance facts, including tracked lifecycle counts plus the current top alert and hotspot when governed skills exist in the workspace
- suggested CLI follow-up commands for the skills governance block so the operator panel can point directly to `reconcile`, `show`, `repair-history`, or `repair-invariants` without adding write controls to the stage
- bounded alert and hotspot lists in the skills governance block so the operator panel can expose more than one governed-skill issue without becoming a full drill-down surface
- a bounded blocked-invariant summary in the skills governance block so non-repairable ledger anomalies can stay visible even when they are outside the currently surfaced alert selection
- bounded per-skill detail previews in the skills governance block so surfaced governed skills also show alert excerpts and recent transitions before the operator switches back to CLI inspection
- a governance item selector that can pin either one surfaced governed skill detail preview or one bounded blocked invariant item in the operator panel, so operators can drill into a bounded governance view without leaving the read-only stage
- selected blocked invariant items now include bounded recent transitions, so the stage can show the immediate governance chain evidence behind a ledger blocker without switching back to CLI first
- selected blocked invariant items now also include bounded same-path history and metadata drift summaries, so the stage can surface adjacent governance anomalies without reopening `avatars skills reconcile`
- selected blocked invariant items now also include bounded same-path state, path, and content drift summaries, so the stage can surface more of the local reconciliation context without becoming a full ledger or reconcile browser
- selected blocked invariant items now also include bounded same-skill `missing-current` and `untracked-current` summaries, so the stage can surface nearby repo/ledger gaps without leaving the read-only operator panel
- selected blocked invariant items now also include bounded recent governance ledger entries, so the stage can show the latest same-skill ordering context and source task/run evidence without leaving the read-only operator panel
- the skills governance block now also includes bounded current gap counts and a capped current gap list, so operators can see standalone `missing-current` and `untracked-current` findings even when no blocked invariant is selected
- the governance item selector can now also pin one bounded current gap item by `skill_path`, so the stage can show recorded state/action or current digest plus a deterministic follow-up command without expanding into a full reconcile browser
- the skills governance block now also includes bounded drift hotspot counts and a capped drift hotspot list, so operators can see standalone path-scoped reconciliation anomalies even when they are not attached to a blocked invariant item
- the governance item selector can now also pin one bounded drift hotspot by `skill_path`, so the stage can show grouped history/metadata/state/path/content drift evidence plus a deterministic follow-up command without expanding into a full reconcile browser
- the governance item selector now uses stable per-item selection keys internally, so operators can pin bounded detail, blocked invariant, current gap, and drift hotspot items independently even when multiple governance item types share the same current path
- legacy `skill_path` stage links still resolve to the highest-priority bounded item on that path, but once loaded the selector can walk the full bounded item list without collapsing same-path entries
- the governance item selector now also exposes bounded previous and next traversal controls, so operators can walk the surfaced governance item list without reopening the dropdown for every step
- the governance item selector now also groups bounded items into explicit sections such as detail previews, blocked invariants, current gaps, and drift hotspots, so traversal order stays legible as the surfaced list grows
- the governance item selector now also exposes bounded previous-section and next-section controls plus a lightweight section summary, so operators can jump across grouped sections without losing the same bounded traversal order
- the governance toolbar now also shows clickable section overview cards with per-section counts and first-item summaries, so operators can assess the surfaced governance distribution before interacting with the selector itself
- the governance section cards now also highlight the active section and show each section's share of the bounded governance view, so operators can judge focus and density at a glance before drilling in
- the governance section cards and compact section summary now also surface a representative next-step hint for each section, so operators can see the strongest bounded follow-up before switching back to CLI inspection
- the main governance text block now also repeats a focused section summary and a per-section next-step rollup, so the text view and the grouped section cards stay aligned even when the operator is scanning without using the toolbar controls
- dense governance sections now also aggregate a dominant section-level follow-up hint, so cards and text rollups can distinguish one representative item from the most common next CLI step inside the same bounded section
- the main governance text block now also uses a compact text mode for section rollups, shortening repeated command prefixes and dense section labels so text-only scanning stays readable even as the bounded governance surface grows
- bounded governance section cues now also surface a severity-aware priority heuristic, so the operator can see whether a section currently reads as `urgent`, `review`, or `inspect` before deciding which bounded cue to follow first
- bounded governance selection items now also carry a minimal priority contract from `/api/operator`, so cards and text rollups can consume repo-scoped urgency metadata without re-deriving it from detail, invariant, gap, and drift evidence on the client
- governance section cards now also compress their priority and lead-next line into the same compact cue syntax used by the text rollups, so card scanning and text-only scanning no longer force the operator to parse two different cue grammars
- invariant repairability diagnostics on surfaced lifecycle alerts, so the operator panel now shows `repairable`, `blocked-by`, and the deterministic follow-up command for invariant issues before the operator switches back to CLI inspection
- a structured drift explorer that compares configured versus currently viewed run state per field, with a changed-only or all-fields filter
- a run-scoped event log that switches to the same run as the operator panel

## Shell Tool

### Run a guarded shell command

```powershell
avatars shell go test ./internal/runtime
```

### Run a guarded shell command in planning mode

```powershell
avatars shell --permission-mode plan go test ./internal/runtime
```

Readonly shell commands continue to run in `plan`, `default`, and `dontAsk`; guarded mutating shell commands are denied in `plan` and `dontAsk`, while `default` now emits an approval-required block before execution.

## Write Tool

### Write a file inside the allowed workspace

```powershell
avatars write .avatars/tmp.txt "hello from avatars"
```

### Deny mutating writes without prompting

```powershell
avatars write --permission-mode dontAsk .avatars/tmp.txt "hello from avatars"
```

When `dontAsk` or `plan` is selected, mutating writes are denied up front and emitted as `tool.denied`; when `default` is selected, mutating writes now emit `tool.awaiting_approval` so the operator can distinguish approval-needed work from hard refusals.

When a task run hits that `default`-mode approval gate, the current pending request can now be resolved with `avatars tasks approve <task-id>` or `avatars tasks deny <task-id>`. When more than one request is pending, use `--approval-key <key>` to target the intended guarded action deterministically.

```powershell
avatars tasks approve <task-id> --approval-key <key> --replay
```

Add `--replay` when you want the task-scoped CLI flow to approve and then immediately re-execute the original guarded request from its persisted approval artifact. The persisted artifact includes a suspended action frame, and replay validates the guarded tool, operation, and input digest before execution. When the approval came from the main workflow, the artifact can also include a workflow checkpoint with the blocked node, completed nodes, and remaining nodes. Replay emits `workflow.resume_checkpoint`; when checkpoint metadata is present and the replay succeeds, the runtime now marks the blocked workflow node complete, resumes the remaining workflow nodes serially, and closes the origin run lifecycle as `completed`. That checkpoint payload now carries canonical `node_id`, `node_role`, and `node_title` for the blocked node when known, while preserving blocked-node compatibility fields. Standalone replay without workflow checkpoint metadata remains a one-tool continuation and leaves the origin lifecycle at `continued`. Repeating replay after the approval artifact already has `continuation_status=completed` is idempotent: the runtime emits `tool.approval_replay_skipped`, reuses the stored continuation lineage, and does not execute the guarded tool again. While an unresolved approval is still pending, `avatars tasks list`, `avatars tasks show <task-id>`, and stage task feedback now elevate the task status to `awaiting_approval` so the operator can spot blocked work without scanning the full approval details. After replay, `avatars tasks show <task-id>` plus `avatars tasks show <task-id> --view verifier` expose the latest approval replay summary, replay transcript path, continuation run ID, continuation task ID, and verifier evidence when verification ran.

### Recovery summary command boundary

`avatars tasks show <task-id>` and `avatars tasks show <task-id> --view verifier` may print a recovery summary plus guidance. This is an inspection surface, not a recovery command grammar.

Verifier view can print recovery summary from task evaluation records even when there is no verifier snapshot yet. For example, a failed workflow node pause point may appear as `category=retryable`, `action=plan_failed_node_retry`, `guard=manual_review_required`, and `authority=workflow_scheduler`. This remains inspection-only.

When failed-node evidence has enough coordinates, `avatars tasks show <task-id>` and `avatars tasks show <task-id> --view verifier` may also print a read-only `Failed node retry candidate` block. The block can include node id, pause point id/kind/digest, origin run/task, retryable flag, expected targets, `contract_ready`, and missing future-command fields. This does not add a retry command and does not run the scheduler.

When failed-node retry attempt evidence exists, those same inspection commands may also print read-only `Failed node retry attempt` and `Failed node retry attempt closure` blocks. The attempt block can include attempt id, status, closure status, open/closed/failed flags, pause/node/origin coordinates, policy reason, verifier gate expectation, and targets. The closure block can include readiness, missing evidence, manual-review guard, node-work verifier evidence, scheduler resume evidence, and lifecycle evidence. Closure evidence is bound by `retry_attempt_id` only.

When failed-node retry candidate and attempt evidence are sufficient to pass the read-only preflight boundary, those same inspection commands may also print a `Failed node retry artifact` block. The artifact block can include deterministic artifact and attempt ids, terminal state, pause/node/origin coordinates, pause digest, scheduler/manual-review policy, verifier gate expectation, and expected targets. This artifact is inspection metadata only; it is not a retry authorization, not an execution artifact, and not a scheduler command.

When failed-node pause evidence includes current-state readiness fields, those same commands may also print `Failed node retry current state`. The block can include whether the current-state evidence is complete, current node status, dependency evidence, scheduler continuation readiness, dependency ids, missing evidence, and the scheduler/manual-review policy. This remains inspection-only; it does not create retry grammar.

Recovery authority is intentionally not unified yet. Approval replay, verifier remediation repair, and failed-node retry use different authority models. Failed-node retry now has read-only candidate, attempt, closure, preflight, artifact, and current-state readiness inspection, but still lacks an executable retry command, so it remains guidance-only.

Current explicit recovery-related commands are:

- `avatars tasks show <task-id>` for full task inspection.
- `avatars tasks show <task-id> --view verifier` for verifier-focused inspection.
- `avatars tasks retry-node <task-id> --origin-run <run-id> --pause-point <pause-point-id> --node <node-id> --pause-digest <digest> --dry-run` for failed-node retry preflight inspection only.
- `avatars verify --task <task-id>` for explicit verifier reruns and reverify closure.
- `avatars tasks approve <task-id>` / `avatars tasks deny <task-id>` for pending guarded actions.
- `avatars tasks approve <task-id> --approval-key <key>` / `avatars tasks deny <task-id> --approval-key <key>` for selecting one pending guarded action.
- `avatars tasks approve <task-id> --replay` and keyed `--replay` variants for explicit approval replay.

Failed-node retry has a dry-run inspection command only:

```powershell
avatars tasks retry-node <task-id> --origin-run <run-id> --pause-point <pause-point-id> --node <node-id> --pause-digest <digest> --dry-run
```

The command loads recorded task truth, evaluates the failed-node retry preflight boundary, renders allowed/denied reasons, candidate coordinates, current-state readiness, a derived inspection artifact when allowed, and the latest attempt/closure evidence. It writes no retry attempt, does not resume scheduler work, and does not mutate task memory. Current dry-run denial reasons include incomplete current-state readiness, in-flight retry attempt, terminal retry scope, failed attempt without retry-after-failure policy, and denied attempt without later validation policy.

The executable form remains unavailable. Passing `--confirm-recorded-truth` returns an unavailable error, and omitting `--dry-run` is rejected. The future executable shape is still reserved by contract only.

Guidance-only recovery situations currently include:

- failed workflow node retry planning,
- verifier remediation repair planning beyond explicit `avatars verify --task <task-id>`,
- blocked recovery-policy decisions,
- advisory recovery action request/boundary metadata that has no concrete approval or verifier command yet.

Do not treat `Recovery summary guidance` as a command. It explains the current evidence and points the operator toward inspection or existing explicit commands. It does not start retry, approve work, replay tools, or close verifier remediation.

### Overwrite an existing file

```powershell
avatars write --overwrite .avatars/tmp.txt "updated text"
```

## Patch Tool

### Replace one exact string

```powershell
avatars patch .avatars/tmp.txt hello hi
```

### Run patch with an explicit permission mode

```powershell
avatars patch --permission-mode acceptEdits .avatars/tmp.txt hello hi
```

### Replace all matches

```powershell
avatars patch --replace-all .avatars/tmp.txt hello hi
```

## Git Tool

### Inspect git state

```powershell
avatars git status
```

### Inspect git diff

```powershell
avatars git diff
```

### Inspect git log

```powershell
avatars git log --oneline -5
```

## Rollback Tool

Rollback uses a captured before-image artifact from a guarded `write` or `patch`. Inspect is read-only. Apply requires `--confirm`.

New rollback artifacts also record the post-mutation `after_sha256`. Before applying rollback, the CLI checks that the current target file still matches that captured after-state. If the file was edited again after the artifact was captured, rollback is refused so user follow-up edits are not silently overwritten.

### Inspect rollback artifact

```powershell
avatars rollback inspect --artifact .avatars/tasks/<task-id>/memory/rollback/<artifact>.json
```

### Apply rollback artifact

```powershell
avatars rollback apply --artifact .avatars/tasks/<task-id>/memory/rollback/<artifact>.json --confirm
```

If apply fails with `rollback target changed since capture`, inspect the current file and artifact again before choosing a new recovery path.

## MCP Tool

### List configured MCP servers

```powershell
avatars mcp list
```

This reads `mcp.servers` from `configs/agent.yaml` and prints the configured server names, URLs, and optional descriptions.

### Show one configured MCP server

```powershell
avatars mcp show repo-inspector
```

This prints the configured URL and optional description for one named MCP server.

### Inspect MCP capabilities through bounded standard list probes

```powershell
avatars mcp inspect repo-inspector
```

This runs a bounded read-only capability probe against `tools/list`, `resources/list`, and `prompts/list`. Sections that return JSON-RPC `method not found` are reported as `unsupported`; other transport or RPC failures still fail the command.

### Bind MCP inspection to a stable task workspace

```powershell
avatars mcp inspect --task repo-mcp repo-inspector
```

When `--task <task-id>` is provided, the MCP inspection run reuses that stable task workspace and updates `avatars tasks show <task-id>` with the latest transcript, summary, and recent MCP events.

### Call an HTTP JSON-RPC MCP server

```powershell
avatars mcp call http://127.0.0.1:5000 tools/list
```

### Call a configured MCP server by name

```powershell
avatars mcp call repo-inspector tools/list
```

When the first argument matches a configured `mcp.servers` entry in `configs/agent.yaml`, the CLI resolves the server name to its configured URL before issuing the runtime MCP call.

### Bind an MCP call to a stable task workspace

```powershell
avatars mcp call --task repo-mcp repo-inspector tools/list
```

This keeps the MCP call inside the selected task workspace so the transcript path, latest run summary, stable summary, and recent key events remain visible through the existing task inspection surface.

### Call with JSON params

```powershell
avatars mcp call http://127.0.0.1:5000 tools/call '{"name":"read_file","arguments":{"path":"process_record.md"}}'
```

## Skills

### Inspect the skills lifecycle directories

```powershell
avatars skills status
```

This prints the current role of each `skills/` directory:

- `approved`: active runtime skills that can be listed, loaded, and invoked
- `generated`: runtime-generated candidate skills waiting for explicit approval
- `archive`: active governance area for retired or disabled skills that stay inspectable but excluded from the runtime-loaded registry
- `templates`: reserved template area that is not used by the current runtime flow

The command also prints the current file count and file names in each directory, so you can tell whether existing files are active runtime artifacts or just pending candidates.

### Approve a generated skill

```powershell
avatars skills approve skills/generated/<candidate-file>.md
```

Approval now writes explicit lifecycle metadata into the skill file, including `approved-at`, so later inspection does not depend on filesystem timestamps.

### Archive an approved skill

```powershell
avatars skills archive skills/approved/<skill-file>.md
```

Archiving writes `archived-at`, flips the lifecycle state to `archived`, and moves the file into `skills/archive/` without returning it to the runtime-loaded registry.

### Disable an approved skill

```powershell
avatars skills disable skills/approved/<skill-file>.md
```

Disabling writes `disabled-at`, flips the lifecycle state to `disabled`, and moves the file into `skills/archive/` so the skill is paused without remaining in the runtime-loaded registry.

### Restore an archived skill

```powershell
avatars skills restore skills/archive/<skill-file>.md
```

Restore moves the skill back into `skills/approved/`, resets the lifecycle state to `approved`, clears `archived-at` or `disabled-at`, and preserves the earlier `approved-at` timestamp so the approved registry becomes active again without inventing filesystem-derived history.

### List pending generated skills

```powershell
avatars skills pending
```

This prints each generated candidate with:

- generated timestamp from skill frontmatter
- source task id
- source run id
- full skill path

### Review a generated skill before approval

```powershell
avatars skills review skills/generated/<candidate-file>.md
```

The review surface compares one generated candidate against the closest governed reference, preferring a template-id match and falling back to the skill name when needed.

The output includes:

- matched reference path when one exists
- matched reference lifecycle state
- match reason such as `template-id: ...`
- changed field count and per-field candidate/reference values
- body changed yes/no summary with candidate and reference line counts
- unified body diff hunks with `@@` headers, nearby unchanged context lines, `-` reference-only lines, and `+` candidate-only lines when the body changed

### List approved skills

```powershell
avatars skills list
```

Approved listings now include the persisted approval timestamp.

### List archived skills

```powershell
avatars skills archived
```

Archived listings include both the archive timestamp and the prior approval timestamp for audit review.

### List disabled skills

```powershell
avatars skills disabled
```

Disabled listings include the disabled timestamp and the prior approval timestamp, while `avatars skills archived` stays filtered to true archived entries only.

### Show repository-wide skills timeline

```powershell
avatars skills timeline
```

The timeline view aggregates all current generated, approved, archived, and disabled skills, then prints:

- current tracked skill counts by lifecycle state
- total recorded lifecycle transitions across those current skills
- churn hotspot lines ranked by transition count
- governance alert lines for deterministic lifecycle issues such as high churn, state mismatch, and impossible lifecycle jumps like `generate -> archive` without an intervening `approve`
- latest-first transition history lines with action, from/to state, skill name, current state, and path

### Show repository-wide governance ledger

```powershell
avatars skills ledger
```

The ledger view reads an append-only repository ledger that survives current skill file removal and prints:

- total recorded governance events
- distinct governed skills seen in the ledger
- latest-first ledger event lines with action, from/to state, skill name, and recorded path

### Reconcile current skills against the ledger

```powershell
avatars skills reconcile
```

The reconciliation view compares current governed skill files against the latest repository ledger entries and prints:

- current and ledger-tracked skill counts
- missing current entries where the ledger still has a latest skill state but no matching current file remains
- untracked current entries where a current skill file has no ledger history
- state drift entries where the current lifecycle state differs from the latest ledger state
- path drift entries where the current file path differs from the latest ledger path for the same governed skill
- content drift entries where the current governed skill definition no longer matches the latest ledger fingerprint even though lifecycle state and path may still match
- metadata drift entries where required lifecycle timestamps or source identifiers have been stripped or no longer match the latest ledger evidence
- history drift entries where the governed `.history.json` sidecar is missing or no longer matches the lifecycle transition sequence that can be reconstructed from the append-only governance ledger
- invariant drift entries where the current parsed lifecycle transition chain is structurally invalid, such as a broken `generate -> approve -> archive` sequence or another impossible governed promotion jump

### Resolve one missing-current finding

```powershell
avatars skills resolve-missing <recorded-file-or-name>
```

This resolution view appends a ledger-only `missing` terminal state for one missing-current finding and prints:

- the governed skill name that was resolved
- the resolution action and the previous lifecycle state
- the terminal `missing` resolution state
- the recorded historical file path that remains in the ledger

Use this only when the governed file is intentionally gone and you want reconciliation to stop flagging it as an unresolved missing-current finding.

### Inspect restore guidance for one missing-current finding

```powershell
avatars skills restore-guide <recorded-file-or-name>
```

This read-only guidance view prints the latest ledger facts needed to recover a missing governed skill file from version control or another artifact source before choosing ledger-only resolution:

- the governed skill name
- the recorded historical path
- the latest recorded action and lifecycle state
- source task id, source run id, and template id when available
- the recommended follow-up reconciliation check after the file has been restored

### Restore one missing-current file from an explicit markdown artifact

```powershell
avatars skills restore-missing <recorded-file-or-name> <source-markdown>
```

This workflow restores a missing governed skill file into the recorded ledger path by using an explicit markdown artifact supplied by the operator. The command then appends an auditable restore event and prints:

- the restored governed path
- the source artifact path used for recovery
- the lifecycle state restored at that path
- the appended ledger action
- the recommended follow-up reconciliation check

Use this when you have already recovered the skill markdown from version control or another artifact source and want to put the real file back under governance before considering `avatars skills resolve-missing`.

### Repair current skill metadata from governance history

```powershell
avatars skills repair-metadata <current-file>
```

This workflow repairs lifecycle timestamps and source identifiers for a governed current skill by using existing history plus the latest ledger entry. It does not change lifecycle state, file path, or body content. The command prints:

- the repaired governed path
- the current lifecycle state
- the list of metadata fields that were rehydrated or cleared
- the appended ledger action
- the recommended follow-up reconciliation check

### Repair current skill history sidecar from governance ledger

```powershell
avatars skills repair-history <current-file>
```

This workflow rebuilds the governed `.history.json` sidecar for an existing current skill from the append-only governance ledger. It restores transition history only; ledger-only maintenance events such as sync or metadata repair are not written into the sidecar. The command prints:

- the repaired governed path
- the current lifecycle state
- the number of rebuilt transition entries
- the appended ledger action
- the recommended follow-up reconciliation check

If a governed `.history.json` sidecar is malformed instead of merely missing, inspection commands no longer fail fast. `avatars skills show` now reports `HistoryStatus: corrupted`, and `avatars skills reconcile` surfaces a `corrupted-history` history drift so operators can still run `avatars skills repair-history <current-file>`.

### Repair current skill lifecycle invariants from governance ledger

```powershell
avatars skills repair-invariants <current-file>
```

This workflow repairs impossible or broken lifecycle chains for an existing governed current skill by rebuilding transition history from the append-only governance ledger, but only when the current sidecar contains repairable invariant violations such as `broken-transition-chain`, `invalid-transition`, or `invalid-start-transition`. The command prints:

- the repaired governed path
- the current lifecycle state
- the number of rebuilt transition entries
- the appended ledger action
- the recommended follow-up reconciliation check

When the current invariant drift is not repairable from the current governance ledger, the command now fails explicitly instead of silently falling back to a broader repair path. Use `avatars skills reconcile` to inspect the invariant entry's `repairable`, `blocked-by`, and `follow-up` fields before retrying.

### Sync the governance ledger to current skills

```powershell
avatars skills sync
```

The sync view appends governance ledger entries for current-file drift without mutating the skill files themselves and prints:

- adopted current entries for governed skill files that have no ledger history yet
- synced current entries for governed skill files whose current lifecycle state, path, or content fingerprint differs from the latest ledger record
- unresolved missing-current entries that still require manual recovery because no current skill file remains to sync from

After `avatars skills sync`, rerun `avatars skills reconcile` to confirm that untracked current files plus state, path, or content drift have been cleared and that only unresolved missing-current findings remain.

`avatars skills reconcile` now also classifies invariant drifts as repairable or non-repairable. Each invariant entry includes:

- whether the drift is repairable from the current governance ledger
- a `blocked-by` reason when repair is unsafe or impossible from current ledger evidence
- a deterministic `follow-up` command such as `avatars skills repair-invariants <current-file>` or `avatars skills ledger`

### Show one current governed skill

```powershell
avatars skills show skills/generated/<skill-file>.md
```

```powershell
avatars skills show skills/approved/<skill-file>.md
```

```powershell
avatars skills show skills/archive/<skill-file>.md
```

The detailed view accepts any current governed skill file under `skills/generated`, `skills/approved`, or `skills/archive`, including disabled files that still live in `skills/archive`. The view includes lifecycle state, source task id, source run id, generated timestamp, approved timestamp, archived timestamp, disabled timestamp, a `HistoryStatus` summary, per-skill `GovernanceAlerts`, a `SuggestedFollowUp` hint, and ordered transition history entries for generate/approve/archive/disable/restore when present. `SuggestedFollowUp` now narrows corrupted or missing sidecars to `avatars skills repair-history <current-file>` and lifecycle chain violations to `avatars skills repair-invariants <current-file>` instead of always falling back to repository-wide reconciliation. If the sidecar is malformed, the view prints `HistoryStatus: corrupted` plus the history decode error and a `corrupted-history` governance alert instead of failing the command.

`skills/approved/avatars-operating-contract_SKILL.md` is an always-on approved skill. It is loaded as a runtime operating contract for natural-language behavior, not as a user-invocable command skill.

## Notes

- Stable task workspaces live under `.avatars/tasks/<task-id>/`.
- Session transcripts are append-only JSONL files.
- Task-local SQLite hot memory lives under `.avatars/tasks/<task-id>/memory/hot-memory.db`.
- Latest structured verifier artifacts are written under `.avatars/tasks/<task-id>/memory/verifier/` when a task-scoped verifier run is recorded.
- Task-local verifier retrieval currently keeps a bounded latest-first history window for recent verifier runs.
- Task-local evaluation retrieval now keeps a bounded latest-first record window for verifier verdicts and passive feedback signals, including the originating tool for each record.
- Task-local evaluation retrieval now also captures runtime tool failures that happen before verifier execution, such as unavailable tools or sandboxed tool call errors.
- File-based diagnostics imports can also write passive feedback into task-local evaluation memory without requiring a live IDE bridge.
- Task-local warm retrieval currently keeps a bounded latest-first lesson window for planner and synthesizer readers.
- Task-local telemetry retrieval currently keeps the latest run snapshot per session mode; token and cost values remain explicitly unavailable unless the active provider surfaces usage data.
- When no verifier verdict is available, task-local warm retrieval can now also capture the latest runtime tool failure as a deterministic warm lesson.
- Task-local evolution retrieval now keeps a bounded latest-first candidate window for planner and synthesizer inspection.
- Repo-level memory now keeps a bounded ranked project lesson window with unique supporting task evidence so planner and synthesizer retrieval can prefer lessons reinforced by multiple tasks.
- Task-local hot memory keeps a bounded recent-key-event window for inspection and continuation prompts.
- `avatars tasks show <task-id> --view ...` exposes the current hot-memory query policy without replaying transcripts.
- The current SQLite dependency chain requires the module `go` directive in `go.mod` to stay at `1.24.0`.
