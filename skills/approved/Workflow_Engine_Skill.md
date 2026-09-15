---
name: workflow-engine
description: >
  Always-on project workflow engine. Governs the plan→phase→todo pipeline and
  SQLite project memory. Plan and todo are operator-facing markdown; process
  record markdown is an export mirror regenerated from SQLite.
when_to_use: Always active. Injected as context for every avatar run.
context: >
  Project workflow pipeline integrated with avatars multi-avatar system:
  Go bootstrap creates docs/workflow/avatars_plan.md, docs/workflow/avatars_todo.md,
  and scaffolds docs/workflow/process_record.md if missing. Runtime SoT for
  pitfalls/lessons is SQLite (hot-memory.db); process_record.md is export-only.
version: 0.4.2
lifecycle-state: approved
user-invocable: false
disable-model-invocation: true
always-on: true
allowed-tools:
  - read
  - write
---

# Workflow Engine (v0.4.0 — SQLite SoT + Phase Fidelity)

## Purpose

This skill integrates the project workflow into the avatars multi-avatar
pipeline. Bootstrap ensures plan/todo exist. Memory for pitfalls and outcomes
lives in SQLite; `process_record.md` is a human/git export, not the read path.

---

## 1. Document Overview

| Document | Path | Purpose |
|----------|------|---------|
| Plan | `docs/workflow/avatars_plan.md` | Overall project plan with phases table |
| Phase detail | `docs/workflow/phaseN.md` (e.g. `phase1.md`) | Per-phase Scope / Tasks / Success Criteria |
| User requirement | `docs/workflow/user_requirement.md` | Persisted NL fidelity source (routes, env names) |
| Todo | `docs/workflow/avatars_todo.md` | Active workspace — current phase checklist only |
| Record (export) | `docs/workflow/process_record.md` | Regenerated mirror of SQLite warm_lessons + recent outcomes |
| Memory (SoT) | SQLite `hot-memory.db` | Pitfalls, hazards, blockers, evaluation records |

**Path rule:** phase files are `docs/workflow/phaseN.md` — **not**
`docs/workflow/phase/phaseX.md`.

---

## 2. For ALL Avatars: Startup Context

Before processing any task:
1. Read `docs/workflow/avatars_plan.md` — what are we building? which phase is active?
2. Read `docs/workflow/avatars_todo.md` — what is the current active checklist?
3. When present, read `docs/workflow/user_requirement.md` and the active `phaseN.md`.
4. Use injected SQLite pitfalls / warm lessons when present — do **not** treat
   hand-edited `process_record.md` as authoritative (runtime does not read it back).

If plan/todo are empty templates, the project is in planning stage.

---

## 3. Phase Lock vs Active Focus

- **Hard lock** only when the operator says「只做 / 本轮只要 Phase N」.
- Full-course / multi-phase implement runs keep lock=0 so Critic can advance
  and hand off to the next Active Phase Builder in the same run.
- "Active Phase FOCUS" banners are focus hints, not locks.

---

## 4. For the PLANNER Avatar

### 4.1 First Task in a Project
1. Populate `docs/workflow/avatars_plan.md` (Goals, Scope, Phases, Mode).
2. Persist the operator NL into `docs/workflow/user_requirement.md` (runtime may
   also write this — do not invent conflicting routes/env names).
3. If full mode (2+ phases): create `docs/workflow/phaseN.md` with Scope &
   Success Criteria **and** a Tasks checklist using `- [ ]` checkboxes (required).
4. **Tasks ⊇ Scope (≈80%+):** every Scope / Success Criterion must map to at
   least one Task. Thin Tasks that omit auth/DELETE/PATCH while Scope lists them
   are rejected — expand Tasks before Confirm.
5. Copy Phase 1 tasks into `docs/workflow/avatars_todo.md` Active Checklist.
6. Multi-phase / full-course implement → at least medium complexity.
7. Deferred later phases may start as stubs; Active Phase must be fully detailed
   before Builder implements it.

### 4.2 Subsequent Tasks
1. If the current phase is incomplete: add tasks that fit, else propose a new phase.
2. If the current phase is complete: runtime may promote (see Section 6).
3. Prefer the operator's explicit phase count; do not inflate phase count.
4. Nested routes / field names from user NL must survive into phase docs
   (e.g. `POST /rooms/{id}/slots`, not a flattened substitute).

### 4.3 Mode Detection
| Signal | Mode | Phase Files? |
|--------|------|--------------|
| Single-file script, one-step fix | simple | No |
| Multi-step feature, architecture change | full | Yes — `docs/workflow/phaseN.md` |
| Uncertain | simple | Can escalate later |

### 4.4 ENV / auth naming
- Prefer the **exact** env var name from user NL (e.g. `CLIPVAULT_API_KEY`,
  `SLOTBOOK_API_KEY`). Never truncate to bare `KEY` / `API_KEY`.
- Runtime may inject `=== ENV LOCK ===` with those exact names — honor them.

---

## 5. For the SYNTHESIZER / Critic — Memory Honesty

- Do **not** manually rewrite `process_record.md` as if it were SoT.
- Implementation lessons are recorded by runtime into SQLite; end-of-run
  regeneration exports them to `process_record.md`.
- Never mark a phase/project completed when delivery is empty or stub-only.
- Never mark Success Criteria / checklist `[x]` while compile or promised tests are red.
- **`[x]` must not keep `(0/N)` counts** — bump progress counts when marking done.
- **Terminal honesty (D3):** `phase_advance_blocked` /
  `checklist_incomplete` / incomplete Active Checklist ≠ run status `completed`.
  Report `completed_unverified` (or needs remediation) and say what remains.

Also update the phase file (`docs/workflow/phaseN.md`) Completion Notes
when a phase truly finishes with evidence.

---

## 6. Phase Promotion Rules

When Active Checklist essentials are done and there is no hard phase lock:

1. Fill the phase's `## Completion Notes` when appropriate.
2. Runtime SyncTodo expands the next phase into Active Checklist.
3. With lock=0, Critic may immediately dispatch Builder for the next phase.
4. If this was the last phase, mark the project complete in Plan only when
   delivery is substantive.
5. **Evidence gate (cross-language):** Phase complete requires language compile
   (e.g. `go build` / `tsc --noEmit` / `cargo check`) and the phase's test
   command when Success Criteria promise tests. Missing deps must be installed
   first — a red `tsc` from absent `@types/node` is not Phase-done.
6. **Do not advance** while Tasks are thinner than Scope, or while auth/import
   layout failures remain open.

---

## 7. Layout Expectations (cross-language)

Respect the project's declared stack when placing or reviewing files:

**Service / CLI / app**
- Python → `app/`
- Go → `internal/` + `cmd/`
- JS/TS/Rust → `src/`

**Pure public library / reusable module** (library-first, 纯库, no CLI)
- Prefer repo root or `<mod>/`. Do not bury public API under private-looking dirs (`internal/`, `_internal/`, `src/internal/`).
- Go: `internal/` is compiler-enforced — never default-bury the public module under `internal/<mod>/`. Genuine private helpers (`internal/<helper>/` with their own package name) are allowed.
- Python/JS/TS/Rust: named package at root, not a default app tree (`app/` / `src/`) unless the user asked for an application. Do not invent Go `internal/` for those languages.

Root entrypoints (`main.go`, `index.ts`, `lib.rs`) count as delivery.

**Not delivery:** package/runtime basenames written as sources (`sql.js`,
`express.js`, `tsconfig.js`, bare `d.ts` / root `test.ts`), junk `default.py` /
`default/default.py`, or comment-only poison stubs. Prefer `migrations/` when
the task asks for schema SQL.

Do not remap a module file (e.g. `models.py`) into a conflicting package path
(`models/__init__.py`) when the user named the module explicitly.

---

## 8. Anti-Patterns

- **Do NOT** treat `process_record.md` as LLM startup SoT
- **Do NOT** put more than one phase's tasks in the Active Checklist
- **Do NOT** modify the `## User Notes` section in avatars_todo.md
- **Do NOT** invent completed work for empty/stub delivery
- **Do NOT** treat dependency-name files or junk scaffolds as completed work
- **Do NOT** create phase files without Tasks checkboxes
- **Do NOT** write protected docs (`avatars_todo.md` / `avatars_plan.md` / `process_record.md`) from Builder
- **Do NOT** advance Phase while compile/test evidence is still red
- **Do NOT** use path `docs/workflow/phase/phaseX.md` (wrong)
- **Do NOT** ENV-lock bare `KEY` / `API_KEY` when the user named `*_API_KEY`
- **Do NOT** report terminal `completed` when advance is blocked
- **DO** keep the Active Checklist under ~15 items; split the phase if larger
