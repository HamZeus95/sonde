// Package model holds the types shared by the parser, the check runners, the
// reporters and the control plane's JSON contract.
//
// It deliberately imports nothing else from internal/: everything depends on
// model, model depends on nothing. Keep it that way.
//
// The JSON tags on these types are the cross-repo contract. sonde-cloud's
// packages/contracts zod schemas mirror them field for field; changing a tag
// here without changing them there breaks the control plane silently.
package model

// SchemaVersion is the version of the JSON document the CLI emits. Bump it when
// the shape of that document changes in a way a consumer must notice, and bump
// the mirroring zod schemas in the same session.
const SchemaVersion = 1

// ContractVersion is the runbook format version, the `sonde.version` key in a
// runbook's frontmatter. A runbook declaring anything else is a parse error:
// forward compatibility is handled per-field (unknown fields warn), not by
// silently accepting a format we do not understand.
const ContractVersion = 1

// Criticality drives check cadence in the control plane: high hourly, medium
// every six hours, low daily.
type Criticality string

// The three criticalities, in descending order of how often a runbook of that
// criticality is re-checked.
const (
	CriticalityHigh   Criticality = "high"
	CriticalityMedium Criticality = "medium"
	CriticalityLow    Criticality = "low"
)

// Criticalities lists every valid value, in descending severity, for error
// messages and validation.
var Criticalities = []Criticality{CriticalityHigh, CriticalityMedium, CriticalityLow}

// Valid reports whether c is a known criticality.
func (c Criticality) Valid() bool {
	for _, known := range Criticalities {
		if c == known {
			return true
		}
	}
	return false
}

// Document is the top-level JSON emitted by `sonde parse`. A single object
// rather than a stream of runbooks so that schema_version travels with the data.
type Document struct {
	SchemaVersion int       `json:"schema_version"`
	Runbooks      []Runbook `json:"runbooks"`
}

// Runbook is one parsed Markdown document containing sonde blocks.
type Runbook struct {
	// Path is as given on the command line, slash-separated, so that golden
	// files and control-plane records are identical on every platform.
	Path string `json:"path"`
	// ContentHash is the SHA-256 of the file's bytes, hex encoded. The control
	// plane uses it to skip re-indexing unchanged runbooks.
	ContentHash string    `json:"content_hash"`
	Meta        Meta      `json:"meta"`
	Checks      []Check   `json:"checks"`
	Warnings    []Warning `json:"warnings,omitempty"`
}

// Meta is the `sonde:` block of a runbook's YAML frontmatter.
type Meta struct {
	Version     int         `json:"version"`
	ID          string      `json:"id"`
	Owner       string      `json:"owner,omitempty"`
	Environment string      `json:"environment,omitempty"`
	Criticality Criticality `json:"criticality,omitempty"`
}

// Warning is a non-fatal parse observation: an unknown field, a deprecated
// spelling. Warnings never change the exit code. Anything that should fail a
// build is an error, not a warning.
type Warning struct {
	Line    int    `json:"line"`
	CheckID string `json:"check_id,omitempty"`
	Message string `json:"message"`
}
