// Package workflow manages persistent project workflow documents:
// plan, phase, todo, and process record. These documents form the
// multi-run project roadmap that persists across avatars sessions.
//
// # Type naming (P1-P2)
//
// ProjectPlanMeta (alias for PlanMeta) — the multi-run project roadmap
// metadata. It describes the overall project structure: goals, phases,
// status, and success criteria. It spans multiple runs.
//
// For a single-run avatar execution plan, see planner.ExecutionPlan.
package workflow
