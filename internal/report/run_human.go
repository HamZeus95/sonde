package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/HamZeus95/sonde/internal/model"
)

// statusLabel is the fixed-width word each result line opens with. Words rather
// than symbols: they survive a pipe, a CI log viewer and a grep.
func statusLabel(s model.Status) string {
	switch s {
	case model.StatusPass:
		return "PASS "
	case model.StatusFail:
		return "FAIL "
	case model.StatusError:
		return "ERROR"
	case model.StatusSkipped:
		return "SKIP "
	default:
		return string(s)
	}
}

// HumanRun writes a run for a terminal, grouped by runbook, and closes with the
// line the product exists to print: which runbooks are wrong.
func HumanRun(w io.Writer, run model.Run) error {
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
			if _, err := fmt.Fprintf(w, "%s  %s\n", r.RunbookPath, r.RunbookID); err != nil {
				return err
			}
			lastRunbook = r.RunbookPath
		}
		if _, err := fmt.Fprintf(w, "  %s  %-24s %s\n", statusLabel(r.Status), r.CheckID, r.Observed.Summary); err != nil {
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
		if _, err := fmt.Fprintf(w, "\n%s wrong: %s\n", plural(len(wrong), "runbook"), strings.Join(wrong, ", ")); err != nil {
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
