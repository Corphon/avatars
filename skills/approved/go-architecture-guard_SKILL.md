---
name: go-architecture-guard
description: >
  Enforces Go project architecture rules: no external dependencies, package structure
  consistency, type definition locations, CLI patterns. Prevents the Builder from
  introducing frameworks (cobra/urfave), creating duplicate types, or adding new
  top-level packages without explicit instruction.
when_to_use: Always active for Go project code generation tasks.
context: The Builder must preserve the project's existing architecture. This skill defines hard constraints that override the Builder's default tendency to reorganize or introduce dependencies.
version: 0.1.0
template-id: go-architecture-guard
lifecycle-state: approved
user-invocable: false
disable-model-invocation: false
always-on: true
allowed-tools:
  - write_file
  - edit_file
  - read_file
  - shell_done
---

# Go Architecture Guard

## HARD CONSTRAINTS — DO NOT VIOLATE

### 1. Dependency Lock
- **NO new external dependencies.** The project's `go.mod` defines the allowed dependency set.
- If a feature requires a library not in `go.mod`, use the standard library instead.
- NEVER add `require` lines to `go.mod`. NEVER run `go get`.
- Standard library packages (`fmt`, `os`, `io`, `strings`, `sync`, `time`, `encoding/json`, `net/http`, `context`, `errors`, `sort`, `strconv`, `path/filepath`, `bufio`, `bytes`, `log`, `flag`) are always allowed.

### 2. Package Structure — Modify Existing, Don't Create New
- **DO NOT create new top-level directories or packages** unless the task EXPLICITLY says "create a new package" or "create a new project".
- **DO NOT create utility packages** (`stringutil/`, `strutil/`, `parse.go` at root, `util/`, `common/`, `helpers/`). If a utility function is needed, put it in the package that uses it, or in an existing `internal/` sub-package.
- When the task says "modify X" or "add feature to X", modify X's existing package. Do NOT create a parallel package alongside it.

### 3. Type Definitions — One Source of Truth
- **DO NOT redefine types that already exist.** If `Note` is defined in `internal/model/`, use that type. Do not create a new `Note` in a different package.
- **DO NOT create duplicate Store/Service/Repository types.** If a Store already exists in `internal/store/`, extend it — don't create a second Store elsewhere.
- Before adding a new type, verify it doesn't already exist in the project.

### 4. CLI Pattern — Preserve Existing Style
- **Follow the project's existing CLI pattern.** If it uses `switch-case` in a handler, do NOT introduce `cobra`, `urfave-cli`, `kingpin`, or any CLI framework.
- **Follow the existing help text format.** Match the style, indentation, and wording of `PrintHelp()` or equivalent.

### 5. Cross-File Consistency
- When changing a function signature, **update ALL call sites in the same output batch.**
- When adding a new command, **update the help text** in the same output batch.
- **Write implementation BEFORE tests.** Tests reference the implementation — if you write tests first, you may hallucinate APIs that don't exist.

### 6. Keep It Simple
- **Prefer extending existing files over creating new ones.**
- **Prefer adding a function to an existing file over creating a new file.**
- **Prefer standard library solutions over clever patterns.**
- If the task can be done in 20 lines, don't write 200. If it can be done in one file, don't create three.

### Before Outputting Code, Verify:
1. Am I modifying an existing package? (Good) Or creating a new top-level directory? (Bad — unless explicitly requested)
2. Am I using types that already exist? (Good) Or redefining them? (Bad)
3. Am I adding external dependencies? (Bad — use stdlib)
4. Am I following the existing CLI pattern? (Good) Or introducing a framework? (Bad)
5. Are my function signatures consistent across all modified files? (Must be yes)
