package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/HamZeus95/sonde/internal/model"
)

// statusLabel is the fixed-width word each result line opens with, painted when
// the destination is a terminal.
//
// Words rather than symbols: they survive a pipe, a CI log viewer and a grep,
// and the colour is added on top of them rather than instead of them. Amber for
// error, not red: "Sonde could not tell" is not "your runbook is wrong", and
// the whole product falls over if those two ever look the same.
func statusLabel(s model.Status, c Colour) string {
	switch s {
	case model.StatusPass:
		return c.status("PASS ", ansiGreen)
	case model.StatusFail:
		return c.status("FAIL ", ansiRed)
	case model.StatusError:
		return c.status("ERROR", ansiYellow)
	case model.StatusSkipped:
		return c.status("SKIP ", ansiDim)
	default:
		return string(s)
	}
}

// HumanRun writes a run for a terminal, grouped by runbook, and closes with the
// line the product exists to print: which runbooks are wrong.
func HumanRun(w io.Writer, run model.Run, colour Colour) error {
	if len(run.Results) == 0 {
		_, err := fmt.Fprintln(w, "No checks to run.")
		return err
	}

	var lastRunbook string
	for _, r := range run.Results {
		if r.RunbookPath != lastRunbook {
			if lastRunbook != "" {
				if _, err := fmt.Fprintln(w); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "%s  %s\n",
				colour.heading(r.RunbookPath), colour.dim(r.RunbookID)); err != nil {
				return err
			}
			lastRunbook = r.RunbookPath
		}
		if _, err := fmt.Fprintf(w, "  %s  %-24s %s\n",
			statusLabel(r.Status, colour), r.CheckID, colour.dim(r.Observed.Summary)); err != nil {
			return err
		}
	}

	s := run.Summary
	parts := []string{fmt.Sprintf("%d passed", s.Passed)}
	if s.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", s.Failed))
	}
	if s.Errored > 0 {
		parts = append(parts, fmt.Sprintf("%d could not be checked", s.Errored))
	}
	if s.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", s.Skipped))
	}
	if _, err := fmt.Fprintf(w, "\n%s: %s\n", plural(s.Total, "check"), strings.Join(parts, ", ")); err != nil {
		return err
	}

	wrong := WrongRunbooks(run)
	if len(wrong) > 0 {
		// The one line this command exists to print, so it is the one line that
		// is allowed to shout.
		if _, err := fmt.Fprintf(w, "\n%s\n",
			colour.status(fmt.Sprintf("%s wrong: %s", plural(len(wrong), "runbook"), strings.Join(wrong, ", ")),
				ansiRed)); err != nil {
			return err
		}
	}
	return nil
}

// WrongRunbooks lists the runbooks with at least one failing assertion, sorted.
//
// A runbook with only errors is not wrong: nothing about it was determined.
func WrongRunbooks(run model.Run) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range run.Results {
		if r.Status != model.StatusFail || seen[r.RunbookID] {
			continue
		}
		seen[r.RunbookID] = true
		out = append(out, r.RunbookID)
	}
	sort.Strings(out)
	return out
}
