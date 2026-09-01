// Package identity executes identity checks: does the group a runbook tells you
// to be in still exist in the identity provider?
//
// Every request is a read against the provider's admin API. Nothing here
// creates, modifies or deletes an identity, a group or a membership.
package identity

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/model"
)

// Provider answers questions about one identity provider. It is declared here,
// by the consumer, so a second provider is a new implementation rather than a
// change to the runner.
type Provider interface {
	// GroupExists reports whether a group exists in a realm. A group that is
	// absent is a false answer, not an error; an unreachable provider is an
	// error.
	GroupExists(ctx context.Context, realm, group string) (bool, error)
}

// Runner executes identity checks against the providers it was given.
type Runner struct {
	providers map[string]Provider
}

// New returns a runner. Providers are keyed by the `provider` field a runbook
// writes, lowercased.
func New(providers map[string]Provider) *Runner {
	normalised := make(map[string]Provider, len(providers))
	for name, provider := range providers {
		normalised[strings.ToLower(name)] = provider
	}
	return &Runner{providers: normalised}
}

// Register wires this runner's checks into a registry. It registers nothing
// when no provider is configured: a check nothing can answer must report error
// rather than a fabricated pass, and an unregistered check is exactly that.
func (r *Runner) Register(reg *checks.Registry) error {
	if len(r.providers) == 0 {
		return nil
	}
	return reg.Register(model.KindIdentity, "group_exists", checks.RunnerFunc(r.groupExists))
}

func (r *Runner) groupExists(ctx context.Context, meta model.Meta, check model.Check) (checks.Outcome, error) {
	spec, ok := check.Spec.(*model.GroupExistsSpec)
	if !ok {
		return checks.Outcome{}, fmt.Errorf("identity/group_exists: spec is %T", check.Spec)
	}
	provider, ok := r.providers[strings.ToLower(spec.Provider)]
	if !ok {
		return checks.Outcome{}, fmt.Errorf("no credentials configured for identity provider %q (configured: %s)",
			spec.Provider, strings.Join(r.configured(), ", "))
	}
	realm := spec.Realm
	if realm == "" {
		realm = meta.Environment
	}
	if realm == "" {
		return checks.Outcome{}, fmt.Errorf("no realm: set realm on the check, or environment in the runbook frontmatter")
	}

	exists, err := provider.GroupExists(ctx, realm, spec.Group)
	if err != nil {
		return checks.Outcome{}, err
	}
	detail := map[string]any{"provider": spec.Provider, "realm": realm, "group": spec.Group}
	if !exists {
		return checks.Fail("group %s does not exist in realm %s", spec.Group, realm).WithDetail(detail), nil
	}
	return checks.Pass("group %s exists in realm %s", spec.Group, realm).WithDetail(detail), nil
}

func (r *Runner) configured() []string {
	out := make([]string, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
