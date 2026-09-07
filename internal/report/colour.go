package report

import (
	"io"
	"os"
)

// Colour decides whether status words are written with ANSI escapes.
//
// The four statuses are the product's whole vocabulary, and the difference
// between them is the difference between "your runbook is wrong" and "Sonde
// could not tell". On a terminal that distinction should be visible before it
// is read.
//
// Everywhere else it must not be. A file, a pipe, a CI log and a grep get the
// same plain words they always did — the labels are words rather than symbols
// for exactly that reason, and colour is added on top of them, never instead.
type Colour struct{ enabled bool }

// ANSI codes, deliberately the basic eight. A 256-colour palette looks better
// on the terminal it was tuned against and worse on every other one, and this
// has to be legible on a light background as well as a dark one.
const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiDim    = "\033[2m"
	ansiBold   = "\033[1m"
)

// ColourMode is the --color flag's value.
type ColourMode string

const (
	// ColourAuto writes colour when the destination is a terminal.
	ColourAuto ColourMode = "auto"
	// ColourAlways writes it regardless — for a pager, or for a recording.
	ColourAlways ColourMode = "always"
	// ColourNever writes none.
	ColourNever ColourMode = "never"
)

// ColourModes are the accepted values of --color.
var ColourModes = []ColourMode{ColourAuto, ColourAlways, ColourNever}

// ValidColourMode reports whether m is one of them.
func ValidColourMode(m ColourMode) bool {
	for _, candidate := range ColourModes {
		if m == candidate {
			return true
		}
	}
	return false
}

// NewColour resolves the mode against the destination and the environment.
//
// NO_COLOR is honoured for any non-empty value, per no-color.org: a user who
// has asked every tool on their machine to stop has asked this one too, and
// "always" is the explicit override that says otherwise.
func NewColour(w io.Writer, mode ColourMode) Colour {
	switch mode {
	case ColourAlways:
		return Colour{enabled: true}
	case ColourNever:
		return Colour{enabled: false}
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return Colour{enabled: false}
	}
	return Colour{enabled: isTerminal(w)}
}

// isTerminal reports whether w is a character device.
//
// A writer, not a file descriptor, because that is what the report writers are
// given — and anything that is not an *os.File (a buffer in a test, a pipe) is
// not a terminal by definition.
func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// status paints one of the four status words.
func (c Colour) status(s string, code string) string {
	if !c.enabled {
		return s
	}
	return code + s + ansiReset
}

// heading paints a runbook path, so the eye finds the boundaries between them.
func (c Colour) heading(s string) string {
	if !c.enabled {
		return s
	}
	return ansiBold + s + ansiReset
}

// dim paints supporting detail that should not compete with the status.
func (c Colour) dim(s string) string {
	if !c.enabled {
		return s
	}
	return ansiDim + s + ansiReset
}
