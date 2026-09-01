// Package report writes parse and run output in the formats a human reads and
// the formats a CI system reads.
//
// Writers here never decide anything. They render what they are given: no
// filtering, no severity judgements, no re-counting. A reporter that decides
// what counts as a failure is a second source of truth about drift.
package report

import "fmt"

// Format selects an output writer.
type Format string

const (
	// FormatHuman is the default: terse, aligned, meant for a terminal.
	FormatHuman Format = "human"
	// FormatJSON is the machine contract, mirrored by sonde-cloud's zod schemas.
	FormatJSON Format = "json"
	// FormatJUnit is consumed by CI systems that render test reports.
	FormatJUnit Format = "junit"
	// FormatTAP is the Test Anything Protocol, for everything else.
	FormatTAP Format = "tap"
)

// ParseFormats are the formats `sonde parse` supports. Parsing produces a
// document, not test results, so the two test-result formats are not offered:
// reporting a parsed check as a passing test would claim it had been executed.
var ParseFormats = []Format{FormatHuman, FormatJSON}

// RunFormats are the formats `sonde check` supports.
var RunFormats = []Format{FormatHuman, FormatJSON, FormatJUnit, FormatTAP}

// Valid reports whether f is in allowed.
func (f Format) Valid(allowed []Format) bool {
	for _, a := range allowed {
		if f == a {
			return true
		}
	}
	return false
}

// Describe renders a format list for a usage message.
func Describe(formats []Format) string {
	out := ""
	for i, f := range formats {
		if i > 0 {
			out += "|"
		}
		out += string(f)
	}
	return out
}

// ErrUnknownFormat is returned for a format outside the allowed set. The CLI
// turns it into exit code 2: a bad flag is a usage error, not a check failure.
func ErrUnknownFormat(f Format, allowed []Format) error {
	return fmt.Errorf("unknown format %q: valid formats are %s", f, Describe(allowed))
}
