// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package planner

import (
	"context"
	"strings"
	"testing"

	"avatars/internal/llm"
)

func TestBuild_CreatesMultiAvatarPlan(t *testing.T) {
	plan := Build("Analyze the current repository and propose a refactoring plan")

	if len(plan.Avatars) < 3 {
		t.Fatalf("expected at least 3 avatars, got %d", len(plan.Avatars))
	}
	if len(plan.Nodes) < 1 {
		t.Fatalf("expected at least 1 work node, got %d", len(plan.Nodes))
	}
	if plan.AvatarIDByRole("Researcher") == "" {
		t.Fatal("expected Researcher avatar")
	}
	assertNoCeremonialDAGNodes(t, plan)
	foundSurvey := false
	for _, node := range plan.Nodes {
		if node.AssignedRole != "Researcher" {
			continue
		}
		foundSurvey = true
		if len(node.DependsOn) != 0 {
			t.Fatalf("survey node must not depend on ceremonial planner, got %+v", node)
		}
	}
	if !foundSurvey {
		t.Fatalf("expected a Researcher work node, got %+v", plan.Nodes)
	}
}

func TestBuildWithContext_PreservesRemediationEnvelope(t *testing.T) {
	plan := BuildWithContext("repair verifier warning", Context{Remediation: &RemediationEnvelope{
		CurrentVerification:                   "PARTIAL via patch",
		LatestNonPassVerification:             "2026-05-05T03:14:49Z | patch | PARTIAL",
		TaskScopedFollowUp:                    "avatars verify --task demo-task",
		ReverifyStatus:                        "pending reverify for latest non-pass verification",
		LatestRemediation:                     "Tool write/file_write completed while the latest non-pass verification still awaits reverify: PARTIAL via patch.",
		VerifierRemediationClosureStatus:      "pending_guarded_remediation",
		VerifierRemediationClosureResume:      "resume-remediate",
		FailedNodeRetryClosureStatus:          "completed",
		FailedNodeRetryClosureReady:           "true",
		FailedNodeRetryClosureVerifierGate:    "verified_pass",
		FailedNodeRetryClosureVerifierVerdict: "PASS",
		FailedNodeRetryClosureReport:          "verifier/retry-a.json",
		RecoverySummaryCategory:               "manual_required",
		RecoverySummaryAction:                 "run_verifier_remediation_repair",
		RecoverySummaryGuardStatus:            "guarded_ready",
		RecoverySummaryGuidance:               "Guarded remediation is pending verifier closure; keep repair and reverify explicit.",
		ExpectedTargets:                       []string{"note.txt", "note.txt", "process_record.md"},
	}})
	if plan.Remediation == nil {
		t.Fatal("expected remediation envelope")
	}
	if plan.Remediation.TaskScopedFollowUp != "avatars verify --task demo-task" {
		t.Fatalf("unexpected task-scoped follow-up: %+v", plan.Remediation)
	}
	if len(plan.Remediation.ExpectedTargets) != 2 {
		t.Fatalf("expected deduplicated expected targets, got %+v", plan.Remediation.ExpectedTargets)
	}
	if plan.Remediation.VerifierRemediationClosureStatus != "pending_guarded_remediation" || plan.Remediation.VerifierRemediationClosureResume != "resume-remediate" {
		t.Fatalf("expected normalized verifier remediation closure fields, got %+v", plan.Remediation)
	}
	if plan.Remediation.FailedNodeRetryClosureStatus != "completed" || plan.Remediation.FailedNodeRetryClosureReady != "true" {
		t.Fatalf("expected normalized failed-node retry closure fields, got %+v", plan.Remediation)
	}
	if plan.Remediation.FailedNodeRetryClosureVerifierGate != "verified_pass" || plan.Remediation.FailedNodeRetryClosureVerifierVerdict != "PASS" || plan.Remediation.FailedNodeRetryClosureReport != "verifier/retry-a.json" {
		t.Fatalf("expected normalized failed-node retry closure verifier fields, got %+v", plan.Remediation)
	}
	if plan.Remediation.RecoverySummaryGuidance != "Guarded remediation is pending verifier closure; keep repair and reverify explicit." || plan.Remediation.RecoverySummaryGuardStatus != "guarded_ready" {
		t.Fatalf("expected normalized recovery summary guidance fields, got %+v", plan.Remediation)
	}
	if len(plan.Remediation.Proposals) != 2 {
		t.Fatalf("expected remediation proposals, got %+v", plan.Remediation.Proposals)
	}
	if plan.Remediation.Proposals[0].ProposalID != "inspect-latest-non-pass" {
		t.Fatalf("unexpected first remediation proposal: %+v", plan.Remediation.Proposals)
	}
	if plan.Remediation.Proposals[1].ProposalID != "remediate-latest-non-pass" {
		t.Fatalf("unexpected second remediation proposal: %+v", plan.Remediation.Proposals)
	}
	if plan.Summary == "" || len(plan.Nodes) == 0 {
		t.Fatalf("expected normal planning shape, got %+v", plan)
	}
}

// === TODO-06 (P2) commander LLM tests ===
//
// TODO-06 adds a complexity assessment step before planning. Trivial
// requests produce a single-avatar plan that the runtime can dispatch
// directly (script --apply / edit --apply) without spinning up the
// 5-avatar survey->build->review chain. Small/medium requests produce
// a sized-down plan; large requests keep the canonical chain.

func TestPlannerBypassForTrivialTasks(t *testing.T) {
	// Heuristic-only path: do not set ComplexityLLMFactory.
	trivials := []string{
		"用 python 写一个九九乘法表",
		"hello",
		"add a print statement to main.go",
		"fix the typo in README",
		"list files",
	}
	for _, input := range trivials {
		assessment := Assess(input)
		if !assessment.IsBypassable() {
			t.Fatalf("expected %q to be bypassable, got level=%s reason=%q", input, assessment.Level, assessment.Reason)
		}
		plan := BuildForComplexity(input, Context{}, assessment)
		if len(plan.Avatars) != 1 {
			t.Fatalf("expected trivial plan for %q to have 1 avatar, got %d (avatars=%+v)", input, len(plan.Avatars), plan.Avatars)
		}
		if plan.Avatars[0].Role != "Direct" {
			t.Fatalf("expected trivial plan avatar role=Direct, got %q", plan.Avatars[0].Role)
		}
		if len(plan.Nodes) != 1 {
			t.Fatalf("expected trivial plan for %q to have 1 node, got %d", input, len(plan.Nodes))
		}
	}
}

func TestAssess_HeuristicOrderingIsMonotonic(t *testing.T) {
	// Directional check: large > medium > small > trivial is monotonic
	// w.r.t. word count + verb presence.
	rank := map[ComplexityLevel]int{
		ComplexityTrivial: 0,
		ComplexitySmall:   1,
		ComplexityMedium:  2,
		ComplexityLarge:   3,
	}
	inputs := []string{
		"",                    // trivial
		"add a print to main", // trivial (8 words, no verb)
		"add a print to main and a debug log to the runner",     // small
		"analyze the codebase and propose a refactoring plan",   // medium (multi-step verb)
		"redesign the database schema and migrate all the data", // large (redesign verb)
	}
	prev := -1
	for _, input := range inputs {
		got := Assess(input)
		if rank[got.Level] < prev {
			t.Fatalf("ordering violated at %q: prev rank=%d, current level=%s rank=%d", input, prev, got.Level, rank[got.Level])
		}
		prev = rank[got.Level]
	}
}

func TestBuildForComplexity_TrivialPlanShape(t *testing.T) {
	assessment := ComplexityAssessment{
		Level:            ComplexityTrivial,
		Reason:           "stub",
		SuggestedAvatars: []string{"Direct"},
		ParallelGroups:   []int{1},
	}
	plan := BuildForComplexity("use python", Context{}, assessment)
	if plan.Title != "use python" {
		t.Fatalf("expected title passthrough, got %q", plan.Title)
	}
	if !strings.Contains(plan.Summary, "Trivial") {
		t.Fatalf("expected trivial plan summary to mention Trivial, got %q", plan.Summary)
	}
	if len(plan.Avatars) != 1 || plan.Avatars[0].Role != "Direct" {
		t.Fatalf("expected 1 Direct avatar, got %+v", plan.Avatars)
	}
	if len(plan.Nodes) != 1 || plan.Nodes[0].ID != "node-direct" {
		t.Fatalf("expected single node-direct, got %+v", plan.Nodes)
	}
}

func TestBuildForComplexity_SmallPlanOmitsResearcherAndCritic(t *testing.T) {
	assessment := ComplexityAssessment{
		Level:            ComplexitySmall,
		SuggestedAvatars: []string{"Planner", "Builder", "Synthesizer"},
		ParallelGroups:   []int{2, 1},
	}
	plan := BuildForComplexity("small task", Context{}, assessment)
	if len(plan.Avatars) != 3 {
		t.Fatalf("expected 3 avatars for small plan, got %d (%+v)", len(plan.Avatars), plan.Avatars)
	}
	for _, role := range []string{"Planner", "Builder", "Synthesizer"} {
		if plan.AvatarIDByRole(role) == "" {
			t.Fatalf("expected small plan to include %s avatar, got %+v", role, plan.Avatars)
		}
	}
	if plan.AvatarIDByRole("Researcher") != "" || plan.AvatarIDByRole("Critic") != "" {
		t.Fatalf("small plan should not include Researcher or Critic, got %+v", plan.Avatars)
	}
}

func TestBuildForComplexity_MediumPlanOmitsCritic(t *testing.T) {
	assessment := ComplexityAssessment{
		Level:            ComplexityMedium,
		SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Synthesizer"},
		ParallelGroups:   []int{2, 2},
	}
	plan := BuildForComplexity("medium task", Context{}, assessment)
	if len(plan.Avatars) != 4 {
		t.Fatalf("expected 4 avatars for medium plan, got %d (%+v)", len(plan.Avatars), plan.Avatars)
	}
	if plan.AvatarIDByRole("Critic") != "" {
		t.Fatalf("medium plan should not include Critic, got %+v", plan.Avatars)
	}
}

func TestBuildForComplexity_MediumMultiPhaseIncludesCritic(t *testing.T) {
	// P9-3: multi-phase assessment suggests Critic with FromLLM=false.
	assessment := ComplexityAssessment{
		Level:            ComplexityMedium,
		Reason:           "multi-phase task (2 phases) — cannot be trivial",
		SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Critic", "Synthesizer"},
		ParallelGroups:   []int{2, 2, 1},
		FromLLM:          false,
	}
	task := "## Phase 1 scaffold\n## Phase 2 auth\n从零搭一个 Python HTTP 服务"
	plan := BuildForComplexity(task, Context{}, assessment)
	if plan.AvatarIDByRole("Critic") == "" {
		t.Fatalf("expected Critic for multi-phase medium, got %+v", plan.Avatars)
	}
}

func TestBuildForComplexity_LargePlanKeepsFullChain(t *testing.T) {
	assessment := ComplexityAssessment{
		Level:            ComplexityLarge,
		SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Critic", "Synthesizer"},
		ParallelGroups:   []int{2, 2, 1},
	}
	plan := BuildForComplexity("redesign the whole system", Context{}, assessment)
	if len(plan.Avatars) != 5 {
		t.Fatalf("expected 5 avatars for large plan, got %d", len(plan.Avatars))
	}
	if len(plan.Nodes) != 3 {
		t.Fatalf("expected 3 work nodes for large plan, got %d (%+v)", len(plan.Nodes), plan.Nodes)
	}
	if plan.AvatarIDByRole("Critic") == "" {
		t.Fatalf("expected large plan to include Critic, got %+v", plan.Avatars)
	}
}

func TestBuildForComplexity_TrivialPlanIgnoresRemediationEnvelope(t *testing.T) {
	// Remediation envelopes are only meaningful for medium/large plans.
	assessment := ComplexityAssessment{Level: ComplexityTrivial}
	plan := BuildForComplexity("trivial", Context{Remediation: &RemediationEnvelope{
		CurrentVerification: "FAIL",
	}}, assessment)
	if plan.Remediation != nil {
		t.Fatalf("expected trivial plan to not carry remediation envelope, got %+v", plan.Remediation)
	}
}

func TestBuildForComplexity_MediumPlanKeepsRemediationEnvelope(t *testing.T) {
	// Medium plans should still carry the remediation envelope so
	// recovery can hand off.
	assessment := ComplexityAssessment{Level: ComplexityMedium}
	plan := BuildForComplexity("repair", Context{Remediation: &RemediationEnvelope{
		CurrentVerification: "PARTIAL",
		LatestRemediation:   "remediation A",
		ReverifyStatus:      "pending",
	}}, assessment)
	if plan.Remediation == nil {
		t.Fatalf("expected medium plan to carry remediation envelope")
	}
	if plan.Remediation.CurrentVerification != "PARTIAL" {
		t.Fatalf("expected envelope to be preserved, got %+v", plan.Remediation)
	}
}

// stubLLMClient is a minimal llm.Client implementation that returns a
// pre-canned response. Used to exercise the LLM path in Assess.
type stubLLMClient struct {
	response string
	fallback bool
	err      error
}

func (s *stubLLMClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	if s.err != nil {
		return llm.Response{}, s.err
	}
	return llm.Response{Text: s.response, Fallback: s.fallback}, nil
}

func (s *stubLLMClient) Provider() string { return "stub" }

func (s *stubLLMClient) Supports(_ llm.Capability) bool { return true }

func TestAssess_LLMPathOverridesHeuristic(t *testing.T) {
	// Stub the LLM factory to return a synthetic client.
	original := ComplexityLLMFactory
	defer func() { ComplexityLLMFactory = original }()
	ComplexityLLMFactory = func() (llm.Client, func(), error) {
		return &stubLLMClient{
			response: `{"level":"large","suggested_avatars":["Planner","Researcher","Builder","Critic","Synthesizer"],"parallel_groups":[2,2,1],"reason":"LLM says large"}`,
		}, func() {}, nil
	}
	// Use a short request that the heuristic would classify as
	// trivial; the LLM should override.
	assessment := Assess("hi")
	if !assessment.FromLLM {
		t.Fatalf("expected assessment.FromLLM=true when LLM path returns, got false")
	}
	if assessment.Level != ComplexityLarge {
		t.Fatalf("expected LLM override to Large, got %s (reason=%q)", assessment.Level, assessment.Reason)
	}
}

func TestAssess_LLMFailureFallsBackToHeuristic(t *testing.T) {
	original := ComplexityLLMFactory
	defer func() { ComplexityLLMFactory = original }()
	// Return a client that errors out — heuristic should win.
	ComplexityLLMFactory = func() (llm.Client, func(), error) {
		return &stubLLMClient{err: context.Canceled}, func() {}, nil
	}
	assessment := Assess("add a print to main.go")
	if assessment.FromLLM {
		t.Fatalf("expected fallback to heuristic when LLM errors out")
	}
	if !assessment.IsTrivial() {
		t.Fatalf("expected heuristic to classify short request as trivial, got %s", assessment.Level)
	}
}

func TestAssess_LLMUnparseableFallsBackToHeuristic(t *testing.T) {
	original := ComplexityLLMFactory
	defer func() { ComplexityLLMFactory = original }()
	// Return a client that emits garbage — parser rejects, heuristic wins.
	ComplexityLLMFactory = func() (llm.Client, func(), error) {
		return &stubLLMClient{response: "not-valid-json"}, func() {}, nil
	}
	assessment := Assess("add a print to main.go")
	if assessment.FromLLM {
		t.Fatalf("expected fallback to heuristic when LLM output is unparseable")
	}
	if !assessment.IsTrivial() {
		t.Fatalf("expected heuristic to classify short request as trivial, got %s", assessment.Level)
	}
}

func TestAssess_NilLLMFactoryFallsBackToHeuristic(t *testing.T) {
	// Default state: no factory set.
	original := ComplexityLLMFactory
	defer func() { ComplexityLLMFactory = original }()
	ComplexityLLMFactory = nil
	assessment := Assess("hello world")
	if assessment.FromLLM {
		t.Fatalf("expected FromLLM=false when factory is nil")
	}
	if !assessment.IsTrivial() {
		t.Fatalf("expected heuristic to classify short request as trivial, got %s", assessment.Level)
	}
}

func TestParseComplexitySpec_AcceptsCodeFenceWrapper(t *testing.T) {
	// Some LLM clients wrap the JSON in ```json ... ```; the parser
	// should still extract the inner JSON object.
	spec, ok := parseComplexitySpec("```json\n{\"level\":\"trivial\",\"suggested_avatars\":[\"Direct\"],\"parallel_groups\":[1],\"reason\":\"x\"}\n```")
	if !ok {
		t.Fatalf("expected parser to accept code-fence wrapper")
	}
	if spec.Level != "trivial" {
		t.Fatalf("expected level=trivial, got %q", spec.Level)
	}
}

func TestParseComplexitySpec_RejectsInvalidJSON(t *testing.T) {
	cases := []string{
		"",
		"not-valid-json",
		`{"level":""}`,            // empty level is invalid
		`{"wrong_field": "true"}`, // missing level
	}
	for _, c := range cases {
		if _, ok := parseComplexitySpec(c); ok {
			t.Fatalf("expected parser to reject %q", c)
		}
	}
}
