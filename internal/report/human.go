package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/HamZeus95/sonde/internal/model"
)

// HumanDocument writes the parse document for a terminal: one block per
// runbook, its checks listed with the resource each one is indexed under, and
// warnings attached to the line that caused them.
func HumanDocument(w io.Writer, doc model.Document) error {
	if len(doc.Runbooks) == 0 {
		_, err := fmt.Fprintln(w, "No runbooks found. A runbook declares itself with a sonde: key in its frontmatter, or with a sonde-runbook block.")
		return err
	}

	var checks, warnings int
	for i, rb := range doc.Runbooks {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s  %s%s\n", rb.Path, rb.Meta.ID, describeMeta(rb.Meta)); err != nil {
			return err
		}
		idWidth, kindWidth := columnWidths(rb.Checks)
		for _, c := range rb.Checks {
			checks++
			state := ""
			if !c.Enabled {
				state = "  (disabled)"
			}
			indexed := c.Canonical
			if indexed == "" {
				indexed = "not indexed"
			}
			if _, err := fmt.Fprintf(w, "  %4d  %-*s  %-*s  %s%s\n",
				c.Line, idWidth, c.ID, kindWidth, string(c.Kind)+"/"+c.Check, indexed, state); err != nil {
				return err
			}
		}
		for _, warning := range rb.Warnings {
			warnings++
			if _, err := fmt.Fprintf(w, "  warning line %d: %s\n", warning.Line, warning.Message); err != nil {
				return err
			}
		}
	}

	_, err := fmt.Fprintf(w, "\n%s, %s, %s\n",
		plural(len(doc.Runbooks), "runbook"), plural(checks, "check"), plural(warnings, "warning"))
	return err
}

// columnWidths sizes the id and kind columns to the widest entry, so that a
// long check name shifts the column rather than breaking the alignment of every
// row after it.
func columnWidths(checks []model.Check) (id, kind int) {
	for _, c := range checks {
		if len(c.ID) > id {
			id = len(c.ID)
		}
		if n := len(c.Kind) + 1 + len(c.Check); n > kind {
			kind = n
		}
	}
	return id, kind
}

func describeMeta(m model.Meta) string {
	var parts []string
	if m.Environment != "" {
		parts = append(parts, m.Environment)
	}
	if m.Criticality != "" {
		parts = append(parts, string(m.Criticality))
	}
	if m.Owner != "" {
		parts = append(parts, m.Owner)
	}
	if len(parts) == 0 {
		return ""
	}
	return "  (" + strings.Join(parts, ", ") + ")"
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
