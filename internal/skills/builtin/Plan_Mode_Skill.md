---
name: plan-mode
description: >
  Always-on skill describing the plan mode execution boundary. Plan mode is
  read-only analysis and planning — it does NOT write code or create files.
  This skill teaches the avatar LLM when to use plan mode vs fast paths
  (script, edit, bootstrap).
when_to_use: Always active. Injected as context so every avatar understands plan mode boundaries.
context: Plan mode is one of four execution paths in avatars. It runs read-only: inspect, analyze, plan, report. It must NOT be used as a fallback for simple creation requests that belong on the script or edit path.
version: 0.1.0
lifecycle-state: approved
user-invocable: false
disable-model-invocation: true
always-on: true
allowed-tools:
  - read
---

# Plan Mode Skill

## What Plan Mode Does

Plan mode (`--permission-mode plan`) runs the avatar pipeline in **read-only**
mode. The avatar can:

- Read and analyze project files
- Generate analysis reports
- Create task plans and recommendations
- Write findings to `.md` reports (explicit user request only)

## What Plan Mode Does NOT Do

Plan mode **must not**:

- Write, modify, or create source code files
- Execute scripts or run programs
- Apply patches or edits
- Generate new scripts, games, apps, or tools

If the user wants to **create** something (write a script, build a game, make
a tool), route to the **script path**, not plan mode.

## When to Use Plan Mode

- User asks to **analyze** a project without changes: "analyze this repo",
  "what does this code do", "review the architecture", "find issues"
- User asks to **plan** before acting: "plan a refactoring", "suggest an
  approach", "propose improvements"
- User explicitly says "plan" or "分析" or "规划" or "只看不改"
- Read-only status queries: "how many files", "what's the project structure"

## When NOT to Use Plan Mode

- User wants to **create** a script/game/tool/app → use `script --apply`
- User wants to **modify** an existing file → use `edit --apply`
- User wants to **scaffold** a new project → use `bootstrap --apply`
- User says "写一个" / "帮我写" / "写脚本" / "搞一个" → this is creation,
  NOT planning

## Anti-Patterns

- **Do NOT** route "帮我写一个游戏" (help me write a game) to plan mode.
  This is a creation request → script path.
- **Do NOT** route "写一个计算器" (write a calculator) to plan mode.
  This is a creation request → script path.
- **Do NOT** use plan mode as a fallback when intent is unclear. If the user
  clearly wants to create something, use the script/edit/fast path.
- **DO** use plan mode only when the user wants read-only analysis or when the
  request is genuinely ambiguous and needs disambiguation.