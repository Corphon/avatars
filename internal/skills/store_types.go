package skills

import (
	"time"
)

type Listing struct {
	Name           string
	Description    string
	WhenToUse      string
	AllowedTools   []string
	Context        string
	AlwaysOn       bool
	Version        string
	LifecycleState string
	Role           string // K1: avatar role this skill belongs to (planner/builder/etc.)
	SourceRunID    string
	SourceTaskID   string
	GeneratedAt    time.Time
	ApprovedAt     time.Time
	ArchivedAt     time.Time
	DisabledAt     time.Time
	Path           string
}

type Definition struct {
	Listing
	UserInvocable          bool
	DisableModelInvocation bool
	TemplateID             string
	Body                   string
	History                []SkillHistoryEntry
	HistoryError           string
	GovernanceAlerts       []GovernanceAlert
}

type ListingWarning struct {
	Path  string
	Error string
}

type ListingScan struct {
	Listings []Listing
	Warnings []ListingWarning
}

type PreparedGeneratedSkill struct {
	Path        string
	Content     string
	GeneratedAt time.Time
}

type SkillHistoryEntry struct {
	At        time.Time `json:"at"`
	Action    string    `json:"action"`
	FromState string    `json:"from_state,omitempty"`
	ToState   string    `json:"to_state"`
	Path      string    `json:"path"`
}

type GovernanceLedgerEntry struct {
	At            time.Time `json:"at"`
	Action        string    `json:"action"`
	FromState     string    `json:"from_state,omitempty"`
	ToState       string    `json:"to_state"`
	SkillName     string    `json:"skill_name"`
	SkillPath     string    `json:"skill_path"`
	TemplateID    string    `json:"template_id,omitempty"`
	SourceRunID   string    `json:"source_run_id,omitempty"`
	SourceTaskID  string    `json:"source_task_id,omitempty"`
	ContentDigest string    `json:"content_digest,omitempty"`
}

type GovernanceLedger struct {
	EventCount int
	SkillsSeen int
	Entries    []GovernanceLedgerEntry
}

type GovernanceCurrentRecord struct {
	SkillName     string
	CurrentState  string
	Path          string
	ContentDigest string
}

type GovernanceStateDrift struct {
	SkillName    string
	CurrentState string
	LedgerState  string
	CurrentPath  string
	LedgerPath   string
}

type GovernancePathDrift struct {
	SkillName   string
	CurrentPath string
	LedgerPath  string
}

type GovernanceContentDrift struct {
	SkillName     string
	CurrentPath   string
	CurrentDigest string
	LedgerDigest  string
	CurrentState  string
	LedgerState   string
}

type GovernanceMetadataDrift struct {
	SkillName    string
	CurrentPath  string
	CurrentState string
	Field        string
	Expected     string
	Actual       string
}

type GovernanceHistoryDrift struct {
	SkillName            string
	CurrentPath          string
	CurrentState         string
	Reason               string
	ExpectedTransitions  int
	ActualTransitions    int
	ExpectedLatestAction string
	ActualLatestAction   string
}

type GovernanceInvariantDrift struct {
	SkillName         string
	CurrentPath       string
	CurrentState      string
	Reason            string
	Repairable        bool
	BlockedBy         string
	SuggestedFollowUp string
}

type GovernanceReconciliation struct {
	CurrentSkills    int
	LedgerSkills     int
	MissingCurrent   []GovernanceLedgerEntry
	UntrackedCurrent []GovernanceCurrentRecord
	StateDrifts      []GovernanceStateDrift
	PathDrifts       []GovernancePathDrift
	ContentDrifts    []GovernanceContentDrift
	MetadataDrifts   []GovernanceMetadataDrift
	HistoryDrifts    []GovernanceHistoryDrift
	InvariantDrifts  []GovernanceInvariantDrift
}

type GovernanceLedgerSyncEntry struct {
	Entry          GovernanceLedgerEntry
	PreviousPath   string
	StateChanged   bool
	PathChanged    bool
	ContentChanged bool
}

type GovernanceLedgerSync struct {
	AdoptedCurrent    []GovernanceLedgerEntry
	SyncedCurrent     []GovernanceLedgerSyncEntry
	UnresolvedMissing []GovernanceLedgerEntry
}

type GovernanceMissingRestoreGuide struct {
	SkillName               string
	RecordedPath            string
	LatestAction            string
	LatestLifecycleState    string
	SourceRunID             string
	SourceTaskID            string
	TemplateID              string
	SuggestedReconcileCheck string
}

type GovernanceMissingRestoreResult struct {
	RestoredPath            string
	SourcePath              string
	LifecycleState          string
	LedgerEntry             GovernanceLedgerEntry
	SuggestedReconcileCheck string
}

type GovernanceMetadataRepairResult struct {
	RepairedPath            string
	LifecycleState          string
	UpdatedFields           []string
	LedgerEntry             GovernanceLedgerEntry
	SuggestedReconcileCheck string
}

type GovernanceHistoryRepairResult struct {
	RepairedPath            string
	LifecycleState          string
	RebuiltTransitions      int
	LedgerEntry             GovernanceLedgerEntry
	SuggestedReconcileCheck string
}

type GovernanceInvariantRepairResult struct {
	RepairedPath            string
	LifecycleState          string
	RebuiltTransitions      int
	LedgerEntry             GovernanceLedgerEntry
	SuggestedReconcileCheck string
}

type governanceInvariantRepairAssessment struct {
	Repairable        bool
	BlockedBy         string
	SuggestedFollowUp string
	ExpectedHistory   []SkillHistoryEntry
}

type GovernanceTransition struct {
	SkillName    string
	CurrentState string
	SkillPath    string
	SkillHistoryEntry
}

type GovernanceSummary struct {
	TrackedSkills  int
	GeneratedCount int
	ApprovedCount  int
	ArchivedCount  int
	DisabledCount  int
	// PHANTOM-4: Skill performance tracking from task_completed ledger entries.
	SuccessCount    int
	FailureCount    int
	SuccessRate     float64 // 0.0-1.0, computed from SuccessCount/(SuccessCount+FailureCount)
	TransitionCount int
	Hotspots        []GovernanceHotspot
	Alerts          []GovernanceAlert
	Transitions     []GovernanceTransition
}

type GovernanceHotspot struct {
	SkillName        string
	CurrentState     string
	Path             string
	TransitionCount  int
	LastTransitionAt time.Time
}

type GovernanceAlert struct {
	Severity  string
	SkillName string
	Reason    string
	Path      string
}

type ReviewFieldDiff struct {
	Field          string
	CandidateValue string
	ReferenceValue string
}

type CandidateReview struct {
	Candidate          Definition
	HasReference       bool
	Reference          Definition
	MatchReason        string
	ChangedFields      []ReviewFieldDiff
	BodyChanged        bool
	BodyDiffLines      []string
	CandidateBodyLines int
	ReferenceBodyLines int
	ValidationFindings []string
	ReadyForApproval   bool
}

type bodyDiffOp struct {
	kind          byte
	line          string
	referenceLine int
	candidateLine int
}

type DirectoryStatus struct {
	Name      string
	Path      string
	Purpose   string
	Active    bool
	Files     []string
	FileCount int
}
