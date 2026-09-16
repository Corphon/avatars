---
name: skills-navigator
description: >
  Always-on index of all approved skills. Lists every skill's name, path,
  function summary, and trigger conditions so the avatar LLM knows which
  skills are available and when to use them. Updated automatically when
  new skills are approved.
when_to_use: Always active. Consult this index before proposing new skills or when the user asks what avatars can do.
context: Master registry of all approved skills. Read-only reference for skill discovery and routing.
version: 0.1.0
lifecycle-state: approved
user-invocable: false
disable-model-invocation: true
always-on: true
allowed-tools:
  - read
---

# Skills Navigator

## Approved Skills Index

| # | Name | Path | Always-On | Summary |
|---|------|------|-----------|---------|
| 1 | builder-role | \avatars\skills\approved\Builder_Role_Skill.md | yes | Compilable delivery; ENV LOCK fidelity; no auth fail-open; bans poison stubs |
| 2 | critic-role | \avatars\skills\approved\Critic_Role_Skill.md | yes | Evidence review; Tasks⊇Scope; checklist_incomplete ≠ done; auth fail-open P0 |
| 3 | intent-routing | \avatars\skills\approved\Intent_Routing_Skill.md | yes | > |
| 4 | plan-mode | \avatars\skills\approved\Plan_Mode_Skill.md | yes | > |
| 5 | planner-role | \avatars\skills\approved\Planner_Role_Skill.md | yes | Tasks⊇Scope; phaseN.md paths; preserve user env/route fidelity |
| 6 | researcher-role | \avatars\skills\approved\Researcher_Role_Skill.md | yes | > |
| 7 | synthesizer-role | \avatars\skills\approved\Synthesizer_Role_Skill.md | yes | Honest status; advance-blocked ≠ completed |
| 8 | workflow-engine | \avatars\skills\approved\Workflow_Engine_Skill.md | yes | phaseN.md; user_requirement; ENV LOCK; D3 terminal honesty |
| 9 | skills-navigator | \avatars\skills\approved\_Navigator_Skill.md | yes | > |
| 10 | avatars-operating-contract | \avatars\skills\approved\avatars-operating-contract_SKILL.md | yes | > |
| 11 | caveman | \avatars\skills\approved\cave_man_SKILL.md | no | > |
| 12 | caveman-commit | \avatars\skills\approved\caveman-commit_SKILL.md | no | > |
| 13 | caveman-help | \avatars\skills\approved\caveman-help_SKILL.md | no | > |
| 14 | caveman-review | \avatars\skills\approved\caveman-review_SKILL.md | no | > |
| 15 | go-architecture-guard | \avatars\skills\approved\go-architecture-guard_SKILL.md | yes | > |
| 16 | SVG + Canvas + CSS + JS 混合动画开发 | \avatars\skills\approved\svg-canvas-js-hybrid-animation_SKILL.md | no | 通用型 Web 动画开发技能，结合 SVG 矢量场景、Canvas 动态渲染与 CSS 过渡/排版，适用于物理模拟、数据可视化、�... |

## How to Use This Index

1. **Before proposing a new skill**: Check this index to avoid duplicating an existing skill.
2. **When the user asks what avatars can do**: Reference this index to list available capabilities.
3. **When routing a request**: Check always-on skills (intent-routing, plan-mode) for routing guidance.
4. **When a new skill is approved**: This index is automatically updated by the `Approve` workflow.
