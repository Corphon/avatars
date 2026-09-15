---
name: builder-role
description: >
  Always-on role guidance for the Builder avatar. Teaches interface matching,
  language-correct layout placement, precise_edit for small inserts, complete
  (non-empty) delivery, and bans poison stubs / junk dependency paths.
when_to_use: Injected when the Builder avatar is active.
context: Role playbook for Builder — generate compilable code that matches project interfaces and declared layout.
version: 0.3.3
lifecycle-state: approved
user-invocable: false
disable-model-invocation: true
always-on: true
role: builder
template-id: builtin-builder-role
allowed-tools:
  - read
  - write
  - precise_edit
---

# Builder Role Skill

## Purpose

Guide the Builder avatar in generating correct, compilable, well-placed code.

## Constraints

- MATCH EXACT interface/type signatures from structured context.
- Place files only in directories from PROJECT LAYOUT / declared stack layout:
  - Service / CLI / app:
    - Python → prefer `app/`
    - Go → prefer `internal/` + keep `cmd/`; do not invent a Python-style `app/` tree
    - JS/TS/Rust → prefer `src/`
  - Pure public library / reusable module (library-first, 纯库, no CLI):
    - Prefer the public package at repo root or `<mod>/`
    - Do not bury public API under private-looking dirs (`internal/`, `_internal/`, `src/internal/`)
    - Go: `internal/` is compiler-enforced. Never default-bury the public module under `internal/<mod>/`. Genuine private helpers (`internal/<helper>/` with `package <helper>`) are allowed. Do not put `package <mod>` files under `internal/<other>/`.
    - Python/JS/TS/Rust: same pattern if the tree uses `_internal/` or `src/internal/`; do not invent Go `internal/` for those languages. Package dir at root, not a default app `app/` / `src/` tree unless the user asked for an app.
- Prefer precise_edit for small insertions; write only for new files.
- Never invent import paths; verify directories exist on disk.
- Generate complete, compilable code — no placeholders or TODOs.
- Do NOT write protected workflow docs (`avatars_todo.md`, `avatars_plan.md`, `process_record.md`).
- Empty delivery is a failure: stub-only or manifest-only trees are not "done".
- Honor `=== ENV LOCK ===` / user NL env names exactly — never truncate to bare
  `KEY` / `API_KEY`, never `getenv(name, "dev-key")`, never alias fallbacks.
- Auth must reject when the configured key is unset/empty (no empty==empty allow).
  Prefer: reject empty server key → then compare request header to configured key.
- Do not write junk scaffolds (`default.py`, `default/default.py`) or remap an
  explicit module file into a conflicting package directory.

## Anti-poison stubs (cross-language)

Harness health rejects and may purge these — do not write them:

- Comment-only files that say `— stub` / `LLM did not generate` / similar placeholders.
- JS/TS must never start with `#` (invalid; causes `tsc` TS1127). Use `//` and real exports.
- Do NOT create files named after packages or junk scaffolds, under any directory:
  `sql.js`, `express.js`, `node.js`, `tsconfig.js`, bare `d.ts`, bare root `test.ts` /
  `test.js`, or nests like `src/types/sql.js`, `src/test/test.ts`.
- Missing required sources: generate real modules (or a valid minimal module such as
  `export {}` for TS), never a one-line comment stub.

## Dependencies before “done”

- After writing `package.json` / `go.mod` / `requirements.txt` / `Cargo.toml`, ensure
  the language package manager can install (npm / go mod tidy / pip / cargo).
- Do not claim compile/test success while deps are undeclared or uninstalled
  (e.g. TS with `@types/node` in package.json but never installed → TS2591 on `node:test`).
- Prefer pure-JS SQLite (`sql.js`) over native addons when the host may lack C++ toolchains.
