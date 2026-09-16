// Package planner builds single-run execution plans: avatar assignments,
// workflow DAG nodes, and remediation context.
//
// # Type naming (P1-P2)
//
// ExecutionPlan (alias for Plan) — a single-run plan that assigns avatars
// to workflow nodes. Each run produces one ExecutionPlan.
//
// For the multi-run project roadmap, see workflow.ProjectPlanMeta.
package planner
