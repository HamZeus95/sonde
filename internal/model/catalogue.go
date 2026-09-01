package model

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// CheckDef is one entry in the v1 check catalogue: a kind+check pair, the spec
// it decodes into, and one line of what it answers.
//
// The catalogue lives here rather than beside the runners so that `sonde parse`
// can reject an unknown kind+check pair without importing a Kubernetes client:
// parsing must stay safe to run on untrusted input, and linking a cluster
// client into it works against that.
//
// internal/checks/registry.go maps the same pairs to the Runners that execute
// them. This table is the authority on which pairs exist; the registry is the
// authority on which of them this build can run.
type CheckDef struct {
	Kind    Kind
	Check   string
	Summary string
	// New returns a zero-valued spec of this check's type, ready to decode into.
	New func() Spec
}

// catalogue is the complete v1 catalogue. Adding an entry is step 1 of the
// seven in docs/design.md §6; a check with fewer than seven steps done is not done.
var catalogue = []CheckDef{
	{KindKubernetes, "resource_exists", "Does the object still exist?",
		func() Spec { return &ResourceExistsSpec{} }},
	{KindKubernetes, "field_equals", "Is the field still the value the runbook claims?",
		func() Spec { return &FieldEqualsSpec{} }},
	{KindKubernetes, "field_gte", "Is the numeric field at least the documented value?",
		func() Spec { return &FieldGTESpec{} }},
	{KindKubernetes, "can_i", "Can the on-call identity still run this command?",
		func() Spec { return &CanISpec{} }},
	{KindKubernetes, "secret_key_present", "Does the Secret still carry this key? (existence only)",
		func() Spec { return &SecretKeyPresentSpec{} }},
	{KindHTTP, "status_is", "Is the documented link alive?",
		func() Spec { return &StatusIsSpec{} }},
	{KindDNS, "record_exists", "Does the name still resolve, and to what?",
		func() Spec { return &RecordExistsSpec{} }},
	{KindIdentity, "group_exists", "Does the group the runbook requires still exist?",
		func() Spec { return &GroupExistsSpec{} }},
	{KindCommand, "parses", "Does the documented command still parse, and do its objects exist?",
		func() Spec { return &ParsesSpec{} }},
}

// Catalogue returns the check catalogue in declaration order.
func Catalogue() []CheckDef {
	out := make([]CheckDef, len(catalogue))
	copy(out, catalogue)
	return out
}

// Lookup finds the definition for a kind+check pair.
func Lookup(kind Kind, check string) (CheckDef, bool) {
	for _, def := range catalogue {
		if def.Kind == kind && def.Check == check {
			return def, true
		}
	}
	return CheckDef{}, false
}

// Kinds returns every kind in the catalogue, in declaration order.
func Kinds() []Kind {
	var out []Kind
	seen := map[Kind]bool{}
	for _, def := range catalogue {
		if !seen[def.Kind] {
			seen[def.Kind] = true
			out = append(out, def.Kind)
		}
	}
	return out
}

// ChecksForKind returns the checks available for a kind, in declaration order.
func ChecksForKind(kind Kind) []string {
	var out []string
	for _, def := range catalogue {
		if def.Kind == kind {
			out = append(out, def.Check)
		}
	}
	return out
}

// KindExists reports whether kind appears in the catalogue at all, which is
// what separates "no such kind" from "no such check for this kind" in an error.
func KindExists(kind Kind) bool {
	return len(ChecksForKind(kind)) > 0
}

// DescribeKinds renders the valid kinds for an error message.
func DescribeKinds() string {
	kinds := Kinds()
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = string(k)
	}
	return strings.Join(out, ", ")
}

// CommonFields are the keys every sonde block may carry regardless of kind.
var CommonFields = []string{"id", "kind", "check", "enabled", "timeout", "verbose"}

// SpecFields returns the field names a spec accepts, taken from its json tags so
// that the accepted set cannot drift from the decoded one. Embedded structs are
// walked, which is how the inline KubernetesTarget fields are reported.
func SpecFields(spec Spec) []string {
	set := map[string]bool{}
	collectFields(reflect.TypeOf(spec), set)
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func collectFields(t reflect.Type, set map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := range t.NumField() {
		field := t.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if field.Anonymous && name == "" {
			collectFields(field.Type, set)
			continue
		}
		if name == "" {
			name = field.Name
		}
		set[name] = true
	}
}

// AcceptedFields is SpecFields plus the fields common to every block, which is
// the set a block's keys are warned against.
func AcceptedFields(spec Spec) []string {
	out := append(SpecFields(spec), CommonFields...)
	sort.Strings(out)
	return out
}

// UnknownCheckError reports a kind+check pair that is not in the catalogue. It
// carries the valid options so the caller can print them; a parser that says
// only "unknown check" makes the user go and read the source.
type UnknownCheckError struct {
	Kind  Kind
	Check string
}

// Error implements error.
func (e *UnknownCheckError) Error() string {
	if !KindExists(e.Kind) {
		return fmt.Sprintf("unknown kind %q: valid kinds are %s", e.Kind, DescribeKinds())
	}
	return fmt.Sprintf("unknown check %q for kind %q: valid checks are %s",
		e.Check, e.Kind, strings.Join(ChecksForKind(e.Kind), ", "))
}
