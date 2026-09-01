package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// Kind is the family of infrastructure a check interrogates. It selects the
// client the runner needs, not the question being asked.
type Kind string

// The kinds of the v1 catalogue.
const (
	KindKubernetes Kind = "kubernetes"
	KindHTTP       Kind = "http"
	KindDNS        Kind = "dns"
	KindIdentity   Kind = "identity"
	KindCommand    Kind = "command"
)

// DefaultTimeout is the per-check deadline when a check does not set one.
const DefaultTimeout = 30 * time.Second

// Check is one assertion lifted out of a runbook: a single testable claim about
// live infrastructure.
type Check struct {
	// ID is the check's `id`, unique within its runbook. The control plane
	// stores it as checks.local_id.
	ID    string `json:"id"`
	Kind  Kind   `json:"kind"`
	Check string `json:"check"`
	// Spec holds the fields specific to this kind+check pair. It marshals to
	// the object stored in checks.spec (jsonb).
	Spec Spec `json:"spec"`
	// Canonical is the resource URI this check references, empty when the check
	// does not name a concrete resource. It is what the reverse index joins on.
	Canonical string `json:"canonical,omitempty"`
	// Line is the 1-indexed line of the check's opening fence, for error
	// messages that an editor can jump to.
	Line int `json:"line"`
	// Enabled defaults to true; `enabled: false` keeps a check in the document
	// as documentation while skipping execution.
	Enabled bool `json:"enabled"`
	// Timeout overrides DefaultTimeout for this check.
	Timeout *Duration `json:"timeout,omitempty"`
	// Verbose opts this check into a fuller observed summary in its result.
	// Off by default: results are minimal unless a human asked otherwise.
	Verbose bool `json:"verbose,omitempty"`
}

// UnmarshalJSON decodes a check, choosing the concrete spec type from the
// kind+check pair in the same object.
//
// Without this the contract would only travel one way. The probe receives its
// work as JSON job payloads carrying exactly this shape, so a Check that
// marshals but cannot be read back is a Check the probe cannot execute.
func (c *Check) UnmarshalJSON(data []byte) error {
	// alias drops this method, so decoding the rest of the object does not
	// recurse into it.
	type alias Check
	var raw struct {
		alias
		// Spec sits at the shallower depth, so it wins over the embedded
		// alias's own spec field and keeps the interface out of the decoder.
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	def, ok := Lookup(raw.Kind, raw.Check)
	if !ok {
		return &UnknownCheckError{Kind: raw.Kind, Check: raw.Check}
	}
	spec := def.New()
	if len(raw.Spec) > 0 {
		if err := json.Unmarshal(raw.Spec, spec); err != nil {
			return fmt.Errorf("decode %s/%s spec: %w", raw.Kind, raw.Check, err)
		}
	}
	*c = Check(raw.alias)
	c.Spec = spec
	return nil
}

// Spec is the kind+check-specific body of an assertion. Implementations live in
// this package so that the parser can validate a block without importing any
// runner, and therefore without linking a Kubernetes client into `sonde parse`.
type Spec interface {
	// Validate reports whether the required fields are present and coherent.
	// It never performs I/O.
	Validate() error
}

// Duration is a time.Duration that marshals as a Go duration string ("30s")
// rather than an integer count of nanoseconds, so the YAML a human writes, the
// JSON the control plane stores, and the zod schema that validates it all agree.
type Duration struct {
	time.Duration
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON implements json.Unmarshaler. sigs.k8s.io/yaml routes YAML
// through JSON, so this covers both encodings.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("timeout must be a duration string such as \"30s\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parse timeout %q: %w", s, err)
	}
	if parsed <= 0 {
		return fmt.Errorf("timeout %q must be positive", s)
	}
	d.Duration = parsed
	return nil
}
