package model

import "time"

// Status is the outcome of executing one check.
//
// The distinction between Fail and Error is the most load-bearing one in the
// product. Fail means the assertion did not hold: the runbook is wrong. Error
// means Sonde could not determine the answer: the probe is offline, the API
// timed out, RBAC denied the read. Reporting an Error as a Fail tells a user
// their documentation is broken when in fact Sonde is broken, which is the
// fastest way to lose them. Errors never count toward drift.
type Status string

// The four outcomes. Nothing else is a status; in particular there is no
// "unknown", because every unknown is one of Error or Skipped and conflating
// them loses the distinction between a broken probe and a disabled check.
const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusError   Status = "error"
	StatusSkipped Status = "skipped"
)

// Observed is what the check saw, stored as check_runs.observed.
//
// Summary is a short line meant to be read in a terminal. Detail is populated
// only when the check set verbose: true, and never carries secret values,
// full manifests or response bodies.
type Observed struct {
	Summary string         `json:"summary"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// Result is one executed check.
type Result struct {
	RunbookPath string    `json:"runbook_path"`
	RunbookID   string    `json:"runbook_id"`
	CheckID     string    `json:"check_id"`
	Kind        Kind      `json:"kind"`
	Check       string    `json:"check"`
	Canonical   string    `json:"canonical,omitempty"`
	Status      Status    `json:"status"`
	Observed    Observed  `json:"observed"`
	LatencyMS   int64     `json:"latency_ms"`
	RanAt       time.Time `json:"ran_at"`
	// Line is the check's line in its runbook, so a failing result points at
	// the assertion that failed.
	Line int `json:"line"`
}

// Run is the top-level JSON emitted by `sonde check`.
type Run struct {
	SchemaVersion int       `json:"schema_version"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	Results       []Result  `json:"results"`
	Summary       Summary   `json:"summary"`
}

// Summary counts a run's results by status.
type Summary struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errored int `json:"errored"`
	Skipped int `json:"skipped"`
}

// Summarise counts results by status.
func Summarise(results []Result) Summary {
	s := Summary{Total: len(results)}
	for _, r := range results {
		switch r.Status {
		case StatusPass:
			s.Passed++
		case StatusFail:
			s.Failed++
		case StatusError:
			s.Errored++
		case StatusSkipped:
			s.Skipped++
		}
	}
	return s
}
