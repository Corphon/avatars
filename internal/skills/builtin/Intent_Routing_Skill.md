---
name: intent-routing
description: >
  Always-on routing guidance for avatars intent classification. Describes which
  user intents should be dispatched to script, edit, bootstrap, or plan mode,
  and provides Chinese/English intent keyword mappings so the LLM can make
  accurate routing decisions even when the deterministic token matcher misses.
when_to_use: Always active. Injected as context for every avatar run and intent analysis.
context: Governs how avatars routes user requests to the correct execution path: script, edit, bootstrap, or plan mode.
version: 0.1.0
lifecycle-state: approved
user-invocable: false
disable-model-invocation: true
always-on: true
allowed-tools:
  - read
---

# Intent Routing Skill

## Purpose

This skill teaches the avatar LLM how user intents should be routed. It augments
the deterministic token matcher with semantic understanding of creation requests.

## Routing Rules

### 1. Script Path (`script --apply`)

Route here when the user asks to **create, write, generate, or build a standalone
program, game, app, tool, or utility** — anything that produces a single-file
deliverable.

**English triggers**: "write a script", "create a game", "make a tool", "build a
calculator", "generate a utility", "simple script", "write program".

**Chinese triggers**: 写脚本, 写一个脚本, 生成脚本, 创建脚本, 脚本, 写游戏,
写一个游戏, 做游戏, 写程序, 做一个程序, 写应用, 写工具, 写一个小,
搞一个, 弄一个, 做一个小, 游戏, 程序, 应用, 工具, 小游戏.

**Language detection**: "用 py"/"用 python" → .py, "用 js"/"用 javascript" → .js,
"用powershell" → .ps1, "shell"/"bash" → .sh.

### 2. Edit Path (`edit --apply`)

Route here when the user asks to **modify, update, fix, or change an existing
file** and the target file path is identifiable.

**English triggers**: "modify", "update", "change", "fix", "edit", "refactor".

**Chinese triggers**: 修改, 更新, 修复, 改代码, 把…改成, 将…替换为.

### 3. Bootstrap Path (`bootstrap --apply`)

Route here when the user asks to **scaffold a new project** with multiple files
and a defined structure (e.g., "create a web app", "scaffold a Go service").

### 4. Plan Mode Path (`run --new-task --permission-mode plan`)

Route here only when the request is **genuinely multi-step, ambiguous, or
requires analysis before action**. Do NOT route simple creation requests here —
they belong on the script or edit path.

## Anti-Patterns

- **Do NOT** route "write a game" or "write a script" to plan mode. These are
  script-path requests.
- **Do NOT** treat "帮我用 py 写一个游戏" (help me write a game in Python) as
  a complex multi-step task. Route to script path.
- **Do NOT** ask for clarification when the user clearly wants to create
  something. Route to script path with inferred filename and language.
- **DO** ask for clarification only when the intent is genuinely ambiguous
  (e.g., "do something with this project" without specifying what).

## Clarification Exit

If the intent analyzer cannot determine script vs edit vs plan, it should
return `action=clarify` with a specific question, not fall through to a generic
command.