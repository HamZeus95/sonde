package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/HamZeus95/sonde/internal/model"
)

// TAPRun writes a run as TAP version 13.
//
// TAP has no way to say "could not be evaluated": a point is ok or it is not.
// Errors are therefore emitted as not ok with severity: error in the YAML
// diagnostic, and a consumer that needs the distinction should read the JUnit
// or JSON output, or the exit code, which keeps it.
func TAPRun(w io.Writer, run model.Run) error {
	if _, err := fmt.Fprintf(w, "TAP version 13\n1..%d\n", len(run.Results)); err != nil {
		return err
	}
	for i, r := range run.Results {
		name := fmt.Sprintf("%s/%s", r.RunbookID, r.CheckID)
		switch r.Status {
		case model.StatusPass:
			if _, err := fmt.Fprintf(w, "ok %d - %s\n", i+1, name); err != nil {
				return err
			}
			continue
		case model.StatusSkipped:
			if _, err := fmt.Fprintf(w, "ok %d - %s # SKIP %s\n", i+1, name, r.Observed.Summary); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "not ok %d - %s\n", i+1, name); err != nil {
			return err
		}
		severity := "fail"
		if r.Status == model.StatusError {
			severity = "error"
		}
		diagnostic := strings.Join([]string{
			"  ---",
			"  severity: " + severity,
			"  message: " + tapScalar(r.Observed.Summary),
			"  check: " + string(r.Kind) + "/" + r.Check,
			"  at: " + fmt.Sprintf("%s:%d", r.RunbookPath, r.Line),
			"  ...",
			"",
		}, "\n")
		if _, err := io.WriteString(w, diagnostic); err != nil {
			return err
		}
	}
	return nil
}

// tapScalar quotes a message so that a colon in it cannot break the YAML block.
func tapScalar(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
