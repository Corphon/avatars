package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"avatars/internal/events"
	"avatars/internal/llm"
	"avatars/internal/mcp"
	memstore "avatars/internal/memory"
	"avatars/internal/planner"
	"avatars/internal/registry"
	runhealth "avatars/internal/runtime/health"
	"avatars/internal/skillbuilder"
	"avatars/internal/skills"
	runtelemetry "avatars/internal/telemetry"
	"avatars/internal/transcript"
	"avatars/internal/verification"
)

type RunResult struct {
	Summary               string
	TranscriptPath        string
	ReportPath            string
	ReportContent         string
	SynthesisStatus       string
	GeneratedSkillPath    string
	GeneratedSkillPreview string
	ApprovedSkillCount    int
	InvokedSkillName      string
	CriticInsights        string          // W19: Critic's learned insights for process_record
	ClarifyPending        *ClarifySession // P7: non-nil when ambiguities need user resolution
	ResumeBoundaryType    string
	// Batch4/I: honest phase footer — set in runOnce defer from the same
	// build/docs gate that decides phase_advance_blocked.
	BuildOK             bool
	BuildOKKnown        bool
	PhaseAdvanceBlocked string // "build_failed" | "phase_docs_incomplete" | "checklist_incomplete" | "user_phase_lock" | "confirm_failed" | "construct_failed" | "needs_remediation" | ""
	// End-of-run Cursor-style change summary + next-step suggestions.
	Footer RunFooter
}

type MCPCallResult struct {
	TranscriptPath string
	Summary        string
	Result         []byte
}

type MCPInspectionSection struct {
	Label     string
	Method    string
	Supported bool
	Names     []string
}

type MCPInspectResult struct {
	TranscriptPath string
	Summary        string
	Sections       []MCPInspectionSection
}

type ToolCallResult struct {
	TranscriptPath   string
	Content          string
	ContinuationRun  string
	ContinuationTask string
}

type ToolCallOptions struct {
	SkipVerification bool
}

type VerificationResult struct {
	TranscriptPath string
	ReportPath     string
	Report         verification.Report
}

type MCPClient interface {
	Call(ctx context.Context, spec mcp.ServerSpec, method string, params any) (mcp.CallResponse, error)
	Close() error
}

type SkillStore interface {
	Generate(runID string, taskID string, proposal skillbuilder.Proposal) (string, error)
	PrepareGenerated(runID string, taskID string, proposal skillbuilder.Proposal) (skills.PreparedGeneratedSkill, error)
	FinalizeGenerated(path string, generatedAt time.Time) error
	ReviewGenerated(generatedPath string) (skills.CandidateReview, error)
	Approve(candidatePath string) (string, error)
	ListApproved() ([]skills.Listing, error)
	ListApprovedTolerant() (skills.ListingScan, error)
	LoadApproved(approvedPath string) (skills.Definition, error)
	RegenerateNavigator() error
	CleanupStaleGenerated(maxAgeDays int) (int, error)
	// P1-4b: Record skill usage outcome in governance ledger.
	RecordSkillUse(skillName string, skillPath string, taskID string, success bool) error
	// PARTIAL-9: Check if a skill qualifies for auto-approve based on ledger success rate.
	ShouldAutoApprove(skillName string) bool
	// PHANTOM-4: Read governance ledger for per-skill success rate computation.
	GovernanceLedger() (skills.GovernanceLedger, error)
}

type verifierRunner interface {
	Run(ctx context.Context) verification.Report
}

type verifierFactory func(workingDir string, includeRace bool) verifierRunner

type Engine struct {
	tools         *registry.ToolRegistry
	transcript    transcript.Writer
	store         *events.Store
	memory        *memstore.Store
	projectMemory *memstore.Store
	sessionID     string
	taskID        string
	taskRoot      string
	projectRoot   string   // W11: user project directory (where docs/workflow/ lives)
	changedFiles  []string // W11: files modified during the current run
	runStartedAt  time.Time
	policy        verification.Policy
	llm           llm.Client
	mcp           MCPClient
	mcpServers    []mcp.ServerSpec
	mcpLLMTools   []llm.ToolDefinition
	mcpAttached   bool
	skills        SkillStore
	verifier      verifierFactory
	health        *runhealth.Service
	telemetry     *runtelemetry.Collector
	llmStatus     *llm.ProviderStatus
	llmWarning    string
	// P14-4a: Pre-existing build error baseline captured before task execution.
	// Passed to the verifier so pre-existing errors are filtered from results.
	buildBaseline map[string]string
	// P14-4b: Node-level retry counting to enforce a hard limit of 3 retries.
	// Keyed by nodeID. Prevents infinite retry loops when verification
	// produces the same failure repeatedly.
	nodeRetryCount map[string]int
	// criticBuilderCycles counts Critic→Builder dispatches in this run.
	// NL smoke #8/#17: hard-stop after maxCriticBuilderCycles to prevent
	// unbounded Builder→Critic→Builder loops.
	criticBuilderCycles int
	// R11-3: same quality-gate failure fingerprint → stop Critic↔Builder thrash.
	lastQualityFailFingerprint string
	qualityFailRepeatCount     int
	// criticInsights accumulates Critic review findings for process_record.
	// Populated during executeCriticNodeWork, read when building RunResult.
	criticInsights []string
	// IM-Fix-2: Accumulates hallucinated imports that were stripped from
	// generated files. Injected into criticEscalateToPlanner so the Builder
	// learns which imports to avoid on retry.
	strippedImports []DependencyWarning
	// R1: Last known workflow state snapshot for resume across nodes.
	// Captured during runWorkflowPlan, persisted in transcript on pause.
	lastWorkflowSnapshot *WorkflowStateSnapshot
	// P1-7b: Per-role model routing overrides (from agent.yaml model_routing).
	modelRouting      map[string]string // key=role lowercase, value=model name
	permissionMode    PermissionMode
	sequence          uint64
	attachedFilePath  string
	attachedFileBytes string
	replContext       string
	autoApproveSkills bool
	personality       PersonalityConfig
	// S2.8: from agent.yaml workflow.auto_confirm (default false when unset).
	workflowAutoConfirm bool

	// emitMu protects concurrent event emission during parallel
	// avatar execution (P3-2).
	emitMu sync.Mutex

	// Builder retry state — captures previous attempt output so Builder
	// learns from mistakes instead of cold-starting on retry.
	builderRetryStates map[string]*builderRetryState
	// perFileToolWarnCounts rate-limits builder.per_file_tool_warning (F16).
	perFileToolWarnCounts map[string]int

	// Batch4/K: ConfirmPlan must run at most once per run (split Builder
	// nodes otherwise re-confirm). lockedPhase sticks for the whole run so
	// SyncTodoFromPlan / plan Active Phase cannot flip PHASE LOCK mid-run.
	confirmPlanDone bool
	lockedPhase     int

	// R7-2: original NL task text for tool-loop layout path rewrite.
	layoutTaskHint string

	// D3: Critic/end-of-run phase-advance block reason (checklist_incomplete, …).
	// Read before Synthesizer emits run.completed so status cannot claim success
	// while advance is blocked.
	phaseAdvanceBlocked  string
	phaseAdvanceOnce     phaseAdvanceDecision
	phaseAdvanceResolved bool
	phaseAdvanceApplied  bool

	// F65: Critic hub hard failure kept soft so Synthesizer can still answer the user.
	criticHardFailure string
	// F76: Builder hit max tool turns then converged because disk was green.
	builderTurnCapConverged bool
}

// builderRetryState tracks what Builder generated in previous attempts.
// TR-Fix-12: JSON tags for persistence across engine restarts.
type builderRetryState struct {
	FilesGenerated        []string `json:"files_generated,omitempty"`
	CompileError          string   `json:"compile_error,omitempty"`
	BuildFailed           bool     `json:"build_failed,omitempty"`
	AttemptNumber         int      `json:"attempt_number,omitempty"`
	TruncationCount       int      `json:"truncation_count,omitempty"`         // I35: consecutive truncation recovery attempts
	EscalatedMaxTokens    int      `json:"escalated_max_tokens,omitempty"`     // I35: escalated max_tokens after truncation (0 = use default)
	EscalatedMaxToolTurns int      `json:"escalated_max_tool_turns,omitempty"` // Batch4/H: bump turns after deadline/turn-cap
	ProseNoFilesRetried   bool     `json:"prose_no_files_retried,omitempty"`
	WallClockWaitRetried  bool     `json:"wall_clock_wait_retried,omitempty"`
	EmptyDeliveryRetried  bool     `json:"empty_delivery_retried,omitempty"`
	DocsOnlyRetried       bool     `json:"docs_only_retried,omitempty"`
	HealthFailedRetried   bool     `json:"health_failed_retried,omitempty"`
}

// PersonalityConfig holds operator preferences injected into the LLM
// system prompt. See configs/personality.yaml for the source.
type PersonalityConfig struct {
	Style           string `yaml:"style"`
	Language        string `yaml:"language"`
	Safety          string `yaml:"safety"`
	OperatorContext string `yaml:"operator_context"`
}

func NewEngine(tools *registry.ToolRegistry, transcriptWriter transcript.Writer, store *events.Store, sessionID string, policy verification.Policy, client llm.Client, mcpClient MCPClient, skillStore SkillStore) *Engine {
	// TR-Fix-8: Initialize truncation recovery limit. Can be overridden
	// at runtime via SetMaxTruncationRecoveries before any Builder run.
	SetMaxTruncationRecoveries(3)

	engine := &Engine{
		tools:          tools,
		transcript:     transcriptWriter,
		store:          store,
		sessionID:      sessionID,
		policy:         policy,
		llm:            client,
		mcp:            mcpClient,
		skills:         skillStore,
		health:         runhealth.New(),
		permissionMode: PermissionModeAcceptEdits,
	}
	engine.verifier = func(workingDir string, includeRace bool) verifierRunner {
		var r verification.Runner
		if includeRace {
			r = verification.NewRaceRunner(workingDir)
		} else {
			r = verification.NewDefaultRunner(workingDir)
		}
		if cache := engine.health.Cache(); cache != nil {
			r = r.WithCache(cache)
		}
		return &r
	}
	return engine
}

func (e *Engine) WithMemory(store *memstore.Store) *Engine {
	e.memory = store
	return e
}

func (e *Engine) ensureBuilderRetryState(nodeID string) *builderRetryState {
	if e.builderRetryStates == nil {
		e.builderRetryStates = make(map[string]*builderRetryState)
	}
	state, ok := e.builderRetryStates[nodeID]
	if !ok {
		state = &builderRetryState{}
		e.builderRetryStates[nodeID] = state
	}
	return state
}

func (e *Engine) WithProjectMemory(store *memstore.Store) *Engine {
	e.projectMemory = store
	return e
}

func (e *Engine) WithTaskWorkspace(taskID string, taskRoot string) *Engine {
	e.taskID = taskID
	e.taskRoot = taskRoot
	return e
}

// WithProjectRoot sets the user project directory for workflow document operations.
func (e *Engine) WithProjectRoot(path string) *Engine {
	e.projectRoot = path
	return e
}

func (e *Engine) WithLLMStatus(status llm.ProviderStatus, warning string) *Engine {
	copyStatus := status
	e.llmStatus = &copyStatus
	e.llmWarning = strings.TrimSpace(warning)
	return e
}

func (e *Engine) WithPermissionMode(mode PermissionMode) *Engine {
	e.permissionMode = normalizePermissionMode(mode)
	return e
}

func (e *Engine) WithMCPServers(servers []mcp.ServerSpec) *Engine {
	e.mcpServers = append([]mcp.ServerSpec(nil), servers...)
	return e
}

// CaptureBuildBaseline runs go build/test/vet before task execution and stores
// the resulting errors. Post-task verification filters these pre-existing errors
// so only newly introduced errors cause a FAIL verdict (P14-4a).
func (e *Engine) CaptureBuildBaseline(workingDir string) {
	if e == nil {
		return
	}
	e.buildBaseline = verification.CaptureBuildBaseline(workingDir)
}

// BuildBaseline returns the current pre-existing build error baseline.
func (e *Engine) BuildBaseline() map[string]string {
	if e == nil {
		return nil
	}
	return e.buildBaseline
}

// WithAttachedFile injects the file at `path` (with its full `content`) into
// the prompt bundle as an `attached_file` dynamic section. The `run` command
// uses this when the user passes `--from-file <path>` so the avatar sees the
// same evidence base as the user, instead of re-reading the file or
// declaring "no repository evidence found". The path/content travel together
// (no truncation, no `oneLine` collapse) so a markdown task spec attached
// via `@file` in REPL keeps its headings and code blocks intact.
func (e *Engine) WithAttachedFile(path string, content string) *Engine {
	e.attachedFilePath = strings.TrimSpace(path)
	e.attachedFileBytes = content
	return e
}

// WithREPLContext sets the REPL conversation context (recent turn summaries)
// on the engine so it can be injected into the prompt bundle as a
// `repl_context` dynamic section. Empty context is a no-op.
func (e *Engine) WithREPLContext(context string) *Engine {
	e.replContext = context
	return e
}

// WithAutoApproveSkills enables automatic approval of generated skills
// when they pass validation (ReadyForApproval == true). When disabled
// (default), generated skills require manual `/skills review` approval.
func (e *Engine) WithAutoApproveSkills(autoApprove bool) *Engine {
	e.autoApproveSkills = autoApprove
	return e
}

// WithPersonality sets the operator personality profile on the engine.
// The profile is injected into the LLM system prompt via
// buildLLMSummaryRequest.
// WithModelRouting sets per-role model overrides from agent.yaml model_routing.
// The map keys are lowercase role names (planner, researcher, builder, critic, synthesizer).
func (e *Engine) WithModelRouting(routing map[string]string) *Engine {
	e.modelRouting = routing
	return e
}

// modelForRole returns the model override for a given avatar role, or empty string.
func (e *Engine) modelForRole(role string) string {
	if e == nil || e.modelRouting == nil {
		return ""
	}
	return e.modelRouting[strings.ToLower(strings.TrimSpace(role))]
}

func (e *Engine) WithPersonality(cfg PersonalityConfig) *Engine {
	e.personality = cfg
	return e
}

func (e *Engine) Personality() PersonalityConfig {
	if e == nil {
		return PersonalityConfig{}
	}
	return e.personality
}

func (e *Engine) AutoApproveSkillsEnabled() bool {
	return e != nil && e.autoApproveSkills
}

// WithWorkflowAutoConfirm sets whether empty-plan first runs auto-confirm (S2.8).
func (e *Engine) WithWorkflowAutoConfirm(autoConfirm bool) *Engine {
	e.workflowAutoConfirm = autoConfirm
	return e
}

func (e *Engine) Run(ctx context.Context, input string) (RunResult, error) {
	return e.runOnce(ctx, input, nil)
}

func (e *Engine) RunWithResume(ctx context.Context, input string, transcriptPath string) (RunResult, error) {
	restored, err := transcript.Restore(transcriptPath)
	if err != nil {
		return RunResult{}, err
	}
	if _, err := e.mergeStoredHotMemory(&restored); err != nil {
		return RunResult{}, err
	}
	// TR-Fix-12 / S4.2: Restore builder retry + node failure counts so
	// truncation recovery and Critic escalation survive restarts.
	if wd, wdErr := os.Getwd(); wdErr == nil {
		if states, loadErr := loadBuilderRetryStates(wd); loadErr == nil && states != nil {
			e.builderRetryStates = states
		}
		if counts, loadErr := loadNodeRetryCounts(wd); loadErr == nil && counts != nil {
			e.nodeRetryCount = counts
		}
	}
	return e.runOnce(ctx, input, &restored)
}

func (e *Engine) CallMCP(ctx context.Context, spec mcp.ServerSpec, method string, params any) (MCPCallResult, error) {
	return e.callMCP(ctx, spec, method, params)
}

func (e *Engine) InspectMCP(ctx context.Context, spec mcp.ServerSpec) (MCPInspectResult, error) {
	return e.inspectMCP(ctx, spec)
}

func (e *Engine) CallTool(ctx context.Context, toolName string, operation string, input any) (ToolCallResult, error) {
	return e.callTool(ctx, toolName, operation, input)
}

func (e *Engine) CallToolWithOptions(ctx context.Context, toolName string, operation string, input any, options ToolCallOptions) (ToolCallResult, error) {
	return e.callToolWithOptions(ctx, toolName, operation, input, options)
}

func (e *Engine) ContinueApprovedToolCall(ctx context.Context, request PendingApprovalRequest, options ToolCallOptions) (ToolCallResult, error) {
	return e.continueApprovedToolCall(ctx, request, options)
}

func (e *Engine) Verify(ctx context.Context, taskID string, workingDir string) (VerificationResult, error) {
	return e.verify(ctx, taskID, workingDir)
}

func (e *Engine) VerifyWithRace(ctx context.Context, taskID string, workingDir string) (VerificationResult, error) {
	return e.verifyWithRace(ctx, taskID, workingDir)
}

func (e *Engine) Resume(ctx context.Context, transcriptPath string) (ResumeResult, error) {
	return e.resumeTranscript(ctx, transcriptPath)
}

func (e *Engine) Close() error {
	if e == nil {
		return nil
	}
	var closeErrs []error
	if e.transcript != nil {
		closeErrs = append(closeErrs, e.transcript.Close())
	}
	if e.mcp != nil {
		closeErrs = append(closeErrs, e.mcp.Close())
	}
	if e.memory != nil {
		closeErrs = append(closeErrs, e.memory.Close())
	}
	if e.projectMemory != nil && e.projectMemory != e.memory {
		closeErrs = append(closeErrs, e.projectMemory.Close())
	}
	return errors.Join(closeErrs...)
}

func (e *Engine) mergeStoredHotMemory(restored *transcript.RestoreSnapshot) (memstore.Snapshot, error) {
	snapshot := memstore.Snapshot{AvatarSummaries: map[string]string{}}
	if e == nil || e.memory == nil || restored == nil {
		return snapshot, nil
	}

	loaded, err := e.memory.LoadSnapshot(restored.SessionID)
	if err != nil {
		return memstore.Snapshot{}, err
	}
	if restored.Summary == "" {
		restored.Summary = loaded.StableSummary
	}
	if restored.TaskSummary == "" {
		restored.TaskSummary = loaded.TaskSummary
	}
	if restored.AvatarSummaries == nil {
		restored.AvatarSummaries = map[string]string{}
	}
	for avatarID, summary := range loaded.AvatarSummaries {
		if restored.AvatarSummaries[avatarID] == "" {
			restored.AvatarSummaries[avatarID] = summary
		}
	}
	if len(restored.RecentEvents) == 0 && len(loaded.RecentEvents) > 0 {
		restored.RecentEvents = append([]string(nil), loaded.RecentEvents...)
	}
	memoryTaskID := strings.TrimSpace(e.taskID)
	if memoryTaskID == "" {
		memoryTaskID = strings.TrimSpace(restored.TaskID)
	}
	if memoryTaskID != "" {
		taskLoaded, err := e.memory.LoadLatestTaskSnapshot(memoryTaskID)
		if err != nil {
			return memstore.Snapshot{}, err
		}
		if len(loaded.WarmLessons) == 0 && len(taskLoaded.WarmLessons) > 0 {
			loaded.WarmLessons = append([]memstore.WarmLesson(nil), taskLoaded.WarmLessons...)
		}
		if len(loaded.EvolutionCandidates) == 0 && len(taskLoaded.EvolutionCandidates) > 0 {
			loaded.EvolutionCandidates = append([]memstore.EvolutionCandidate(nil), taskLoaded.EvolutionCandidates...)
		}
		if len(loaded.EvaluationRecords) == 0 && len(taskLoaded.EvaluationRecords) > 0 {
			loaded.EvaluationRecords = append([]memstore.EvaluationRecord(nil), taskLoaded.EvaluationRecords...)
		}
	}
	if e.projectMemory != nil {
		projectLessons, err := e.projectMemory.LoadProjectLessons()
		if err != nil {
			return memstore.Snapshot{}, err
		}
		if len(projectLessons) > 0 {
			loaded.ProjectLessons = append([]memstore.ProjectLesson(nil), projectLessons...)
		}
	}
	return loaded, nil
}

// skillSuccessRates computes per-skill success rates from the governance ledger.
// Returns nil if the skills store is unavailable or has no task_completed entries.
func (e *Engine) skillSuccessRates() map[string]float64 {
	if e == nil || e.skills == nil {
		return nil
	}
	ledger, err := e.skills.GovernanceLedger()
	if err != nil || len(ledger.Entries) == 0 {
		return nil
	}
	type skillStats struct {
		success int
		failure int
	}
	stats := make(map[string]*skillStats)
	for _, entry := range ledger.Entries {
		if entry.Action != "task_completed" {
			continue
		}
		name := strings.TrimSpace(entry.SkillName)
		if name == "" {
			continue
		}
		if stats[name] == nil {
			stats[name] = &skillStats{}
		}
		switch entry.ToState {
		case "success":
			stats[name].success++
		case "failure":
			stats[name].failure++
		}
	}
	if len(stats) == 0 {
		return nil
	}
	rates := make(map[string]float64, len(stats))
	for name, s := range stats {
		total := s.success + s.failure
		if total > 0 {
			rates[name] = float64(s.success) / float64(total)
		}
	}
	return rates
}

// recordNodeRetryFailure increments the failure counter for a node (S4.3).
// Unlike the old Builder-entry increment, this only runs on real failure /
// no-progress rebuilds so Critic escalation stays honest.
func (e *Engine) recordNodeRetryFailure(nodeID string) {
	if e == nil {
		return
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return
	}
	if e.nodeRetryCount == nil {
		e.nodeRetryCount = make(map[string]int)
	}
	e.nodeRetryCount[nodeID]++
	root := strings.TrimSpace(e.projectRoot)
	if root == "" {
		root = "."
	}
	if err := persistNodeRetryCounts(root, e.nodeRetryCount); err != nil {
		_ = e.emit("", "", "", "executing", "builder.retry_persist_failed", "runtime", map[string]any{
			"error":   err.Error(),
			"node_id": nodeID,
			"kind":    "node_retry_counts",
		}, nil)
	}
}

// avatarIDForWorkflowNode maps a runtime node to a concrete avatar ID (S4.5).
// Parallel survey nodes use suffix matching (docs/src/cfg); otherwise first role.
func avatarIDForWorkflowNode(plan planner.Plan, node WorkflowNodeRuntime) string {
	role := strings.TrimSpace(node.AssignedRole)
	nodeID := strings.ToLower(strings.TrimSpace(node.ID))
	ids := plan.AvatarIDsByRole(role)
	if len(ids) == 0 {
		return plan.AvatarIDByRole(role)
	}
	if len(ids) == 1 {
		return ids[0]
	}
	for _, id := range ids {
		idL := strings.ToLower(id)
		switch {
		case strings.Contains(nodeID, "docs") && strings.Contains(idL, "docs"):
			return id
		case strings.Contains(nodeID, "src") && (strings.Contains(idL, "src") || strings.Contains(idL, "source")):
			return id
		case strings.Contains(nodeID, "cfg") && (strings.Contains(idL, "cfg") || strings.Contains(idL, "config")):
			return id
		}
	}
	// Split builder nodes: round-robin by numeric suffix when multiple builders exist.
	if role == "Builder" && strings.Contains(nodeID, "-") {
		parts := strings.Split(nodeID, "-")
		last := parts[len(parts)-1]
		var n int
		if _, err := fmt.Sscanf(last, "%d", &n); err == nil && n > 0 {
			return ids[(n-1)%len(ids)]
		}
	}
	return ids[0]
}

// recordInvokedSkillUse writes a task_completed ledger entry for the skill
// that actually ran. S3.6: closes the feedback loop for ShouldAutoApprove.
func (e *Engine) recordInvokedSkillUse(taskID string, invokedName string, active *skills.Definition, success bool) {
	if e == nil || e.skills == nil {
		return
	}
	name := strings.TrimSpace(invokedName)
	path := ""
	if active != nil {
		if name == "" {
			name = strings.TrimSpace(active.Name)
		}
		path = strings.TrimSpace(active.Path)
	}
	if name == "" {
		return
	}
	_ = e.skills.RecordSkillUse(name, path, strings.TrimSpace(taskID), success)
}

// topPastExperienceSkills returns up to k approved skills for role, ranked by
// governance ledger success rate (S3.10). Falls back to name/role match order
// when the ledger has no task_completed data yet.
func (e *Engine) topPastExperienceSkills(role string, k int) []skills.Definition {
	if e == nil || e.skills == nil || k <= 0 {
		return nil
	}
	scan, err := e.skills.ListApprovedTolerant()
	if err != nil {
		return nil
	}
	role = strings.ToLower(strings.TrimSpace(role))
	type ranked struct {
		def  skills.Definition
		rate float64
		hits int
	}
	rates := e.skillSuccessRates()
	candidates := make([]ranked, 0)
	for _, listing := range scan.Listings {
		def, loadErr := e.skills.LoadApproved(listing.Path)
		if loadErr != nil || strings.TrimSpace(def.Body) == "" {
			continue
		}
		skillRole := strings.ToLower(strings.TrimSpace(def.Role))
		nameL := strings.ToLower(def.Name)
		descL := strings.ToLower(def.Description)
		if skillRole != role && !strings.Contains(nameL, role) && !strings.Contains(descL, role) {
			continue
		}
		rate := 0.0
		hits := 0
		if rates != nil {
			if r, ok := rates[def.Name]; ok {
				rate = r
				hits = 1
			}
		}
		candidates = append(candidates, ranked{def: def, rate: rate, hits: hits})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].hits != candidates[j].hits {
			return candidates[i].hits > candidates[j].hits
		}
		if candidates[i].rate != candidates[j].rate {
			return candidates[i].rate > candidates[j].rate
		}
		return candidates[i].def.Name < candidates[j].def.Name
	})
	if len(candidates) > k {
		candidates = candidates[:k]
	}
	out := make([]skills.Definition, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.def)
	}
	return out
}
