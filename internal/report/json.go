package report

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/HamZeus95/sonde/internal/model"
)

// JSONDocument writes the parse document as indented JSON.
//
// Indented rather than compact because the first thing anyone does with it is
// read it, and the second is diff it in a pull request.
func JSONDocument(w io.Writer, doc model.Document) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

// JSONRun writes a run as indented JSON.
func JSONRun(w io.Writer, run model.Run) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(run); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}
