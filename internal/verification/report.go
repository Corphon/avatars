package verification

type Verdict string

const (
	VerdictPass    Verdict = "PASS"
	VerdictFail    Verdict = "FAIL"
	VerdictPartial Verdict = "PARTIAL"
)

type CheckEvidence struct {
	Name           string  `json:"name"`
	CommandRun     string  `json:"command_run"`
	OutputObserved string  `json:"output_observed"`
	Expected       string  `json:"expected"`
	Actual         string  `json:"actual"`
	Result         Verdict `json:"result"`
}

type Report struct {
	Verdict  Verdict         `json:"verdict"`
	Checks   []CheckEvidence `json:"checks"`
	Summary  string          `json:"summary"`
	Warnings []string        `json:"warnings,omitempty"`
}
