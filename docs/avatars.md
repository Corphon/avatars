# avatars Architecture & Planning Entrypoint

> Detailed planning, architecture decisions, and module relationship diagrams.

## Memory Architecture (M6)

avatars uses a **SQLite-first** memory model with markdown export for git versioning.

```
┌─────────────────────────────────────────────────────────┐
│                    WRITE PATH                            │
│                                                          │
│  RecordTaskCompletion()                                  │
│    ├─ SQLite evaluation_records (SoT)                    │
│    └─ SQLite warm_lessons (pitfalls/hazards/blockers)   │
│                                                          │
│  Run end: RegenerateRecordMarkdownFromSQLite()           │
│    └─ process_record.md (git-friendly export, READ ONLY) │
└─────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────┐
│                    READ PATH                             │
│                                                          │
│  LoadMemoryContext() → SQLite LoadSnapshot("workflow")   │
│  ReadPitfalls()      → SQLite warm_lessons               │
│  LoadWorkflowContext() → plan.md + todo.md + SQLite      │
│                                                          │
│  process_record.md is NEVER read by runtime code.        │
│  It exists purely for git diff / human review.           │
└─────────────────────────────────────────────────────────┘
```

### Tables

| Table | Purpose | Retention |
|-------|---------|-----------|
| `evaluation_records` | Task completion verdicts (PASS/FAIL/PARTIAL) | Jaccard ≥0.7 dedup |
| `warm_lessons` | Cross-session pitfalls and hazards | Per-task_id, confidence-based expiry |
| `evolution_candidates` | Skill晋升候选 | Time-based staleness |

### Dedup Strategy

Both SQLite writes and markdown export use **Jaccard similarity ≥0.7** for deduplication (M3 unification).

### Config

- `agent.yaml :: llm_defaults.timeout_ms` — base LLM timeout
- `agent.yaml :: llm_defaults.max_tool_turns_by_role` — per-role tool calling turns
- `agent.yaml :: llm_defaults.max_parallel_avatars` — scheduler concurrency cap

## Key Design Decisions

- **Single Source of Truth**: SQLite for all mutable state; YAML files for static config; markdown exports for git.
- **Deterministic Guards**: import validation, type conflict detection, and syntax checks all run WITHOUT LLM involvement.
- **Adversarial Verification**: Critic tries to BREAK the code, not confirm it works (inspired by Claude Code's verificationAgent).
- **Streaming + Tools**: Builder output streams in real-time via SSE with tool_call delta accumulation.
