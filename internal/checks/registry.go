// Package checks executes assertions against live infrastructure.
//
// Every runner here performs reads and nothing else. No runner may issue a
// create, update, patch, delete, exec or scale against customer infrastructure,
// and the one apparent exception — SubjectAccessReview, which the Kubernetes API
// models as a create — is documented where it appears.
package checks

import (
	"context"
	"fmt"
	"sort"

	"github.com/HamZeus95/sonde/internal/model"
)

// Runner executes one kind+check pair.
//
// The split between the return values is the product's most important
// distinction, made structural so that a runner cannot blur it by accident:
//
//   - An Outcome is an answer. Its status is pass or fail, and fail means the
//     assertion did not hold — the runbook is wrong.
//   - An error means no answer was obtainable: the API timed out, RBAC denied
//     the read, the resolver was unreachable. The engine records that as
//     status error, which never counts toward drift.
//
// A runner that cannot tell the two apart should return the error. Silence is
// recoverable; a false accusation that someone's documentation is wrong is the
// fastest way to lose them.
type Runner interface {
	Run(ctx context.Context, meta model.Meta, check model.Check) (Outcome, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, meta model.Meta, check model.Check) (Outcome, error)

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, meta model.Meta, check model.Check) (Outcome, error) {
	return f(ctx, meta, check)
}

// Outcome is a determined answer: the assertion held, or it did not.
type Outcome struct {
	Status   model.Status
	Observed model.Observed
}

// Pass reports that the assertion held. The summary says what was seen, not
// that it passed: "3 replicas" is useful in a report, "ok" is not.
func Pass(format string, args ...any) Outcome {
	return Outcome{Status: model.StatusPass, Observed: model.Observed{Summary: fmt.Sprintf(format, args...)}}
}

// Fail reports that the assertion did not hold, and therefore that the runbook
// is wrong. Use it only when the answer is known.
func Fail(format string, args ...any) Outcome {
	return Outcome{Status: model.StatusFail, Observed: model.Observed{Summary: fmt.Sprintf(format, args...)}}
}

// WithDetail attaches structured detail to an outcome. The engine drops it
// unless the check set verbose, so a runner may always attach it — but it must
// never put a secret value, a full manifest or a response body in it.
func (o Outcome) WithDetail(detail map[string]any) Outcome {
	o.Observed.Detail = detail
	return o
}

// Registry maps kind+check pairs to the runners that execute them.
//
// model.Catalogue is the authority on which pairs exist. This registry is the
// authority on which of them a given build, holding a given set of clients, can
// actually run: a probe with no identity credentials registers no identity
// runner, and its identity checks report error rather than pretending.
type Registry struct {
	runners map[string]Runner
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{runners: map[string]Runner{}}
}

func key(kind model.Kind, check string) string { return string(kind) + "/" + check }

// Register adds a runner. It rejects a pair that is not in the catalogue, and a
// pair that is already registered: both mean the caller believes in a check
// that does not exist, or has wired one up twice.
func (r *Registry) Register(kind model.Kind, check string, runner Runner) error {
	if _, ok := model.Lookup(kind, check); !ok {
		return &model.UnknownCheckError{Kind: kind, Check: check}
	}
	if runner == nil {
		return fmt.Errorf("register %s: runner is nil", key(kind, check))
	}
	if _, exists := r.runners[key(kind, check)]; exists {
		return fmt.Errorf("register %s: already registered", key(kind, check))
	}
	r.runners[key(kind, check)] = runner
	return nil
}

// MustRegister is Register for wiring done at start-up, where a mistake is a
// bug in Sonde rather than in a runbook.
func (r *Registry) MustRegister(kind model.Kind, check string, runner Runner) {
	if err := r.Register(kind, check, runner); err != nil {
		panic(err)
	}
}

// Runner returns the runner for a pair.
func (r *Registry) Runner(kind model.Kind, check string) (Runner, bool) {
	runner, ok := r.runners[key(kind, check)]
	return runner, ok
}

// Supported lists the registered pairs, sorted.
func (r *Registry) Supported() []string {
	out := make([]string, 0, len(r.runners))
	for k := range r.runners {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Unsupported lists catalogue pairs with no runner in this registry, sorted. It
// is what `sonde check` prints when a runbook uses a check this build cannot
// execute, so that the gap is stated rather than discovered.
func (r *Registry) Unsupported() []string {
	var out []string
	for _, def := range model.Catalogue() {
		if _, ok := r.runners[key(def.Kind, def.Check)]; !ok {
			out = append(out, key(def.Kind, def.Check))
		}
	}
	sort.Strings(out)
	return out
}
