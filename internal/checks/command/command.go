// Package command executes command checks: does the invocation printed in the
// runbook still parse, and do the objects it names still exist?
//
// The command is never executed. It is tokenised, the objects it addresses are
// looked up read-only, and that is the whole of it — including when the command
// itself is `kubectl delete`. A runbook is a document; running what it contains
// is the one thing Sonde must never do.
package command

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/checks/kubernetes"
	"github.com/HamZeus95/sonde/internal/model"
)

// Runner executes command checks.
type Runner struct {
	client kubernetes.Client
}

// New returns a runner. The Kubernetes client is what lets the runner verify
// the objects a kubectl invocation names.
func New(client kubernetes.Client) *Runner { return &Runner{client: client} }

// Register wires this runner's checks into a registry.
func (r *Runner) Register(reg *checks.Registry) error {
	return reg.Register(model.KindCommand, "parses", checks.RunnerFunc(r.parses))
}

func (r *Runner) parses(ctx context.Context, meta model.Meta, check model.Check) (checks.Outcome, error) {
	spec, ok := check.Spec.(*model.ParsesSpec)
	if !ok {
		return checks.Outcome{}, fmt.Errorf("command/parses: spec is %T", check.Spec)
	}

	parsed, err := Parse(spec.Command)
	if err != nil {
		// A command that cannot be parsed is a claim that cannot be evaluated,
		// not a claim shown to be false.
		return checks.Outcome{}, err
	}
	if parsed.Tool != "kubectl" {
		return checks.Outcome{}, fmt.Errorf("command/parses can verify kubectl invocations; %q needs a %s client this build does not have",
			parsed.Tool, parsed.Tool)
	}

	cluster := spec.Cluster
	if cluster == "" {
		cluster = meta.Environment
	}
	namespace := parsed.Namespace
	if namespace == "" {
		namespace = spec.Namespace
	}

	if len(parsed.Resources) == 0 {
		return checks.Pass("parses; names no specific object").
			WithDetail(map[string]any{"verb": parsed.Verb}), nil
	}

	var missing []string
	for _, ref := range parsed.Resources {
		target := kubernetes.Target{
			Cluster:   cluster,
			Namespace: namespace,
			Resource:  ref.Type,
			Name:      ref.Name,
		}
		_, err := r.client.Get(ctx, target)
		switch {
		case apierrors.IsNotFound(err):
			missing = append(missing, ref.Type+"/"+ref.Name)
		case err != nil:
			return checks.Outcome{}, err
		}
	}

	named := make([]string, 0, len(parsed.Resources))
	for _, ref := range parsed.Resources {
		named = append(named, ref.Type+"/"+ref.Name)
	}
	detail := map[string]any{"verb": parsed.Verb, "namespace": namespace, "names": named}
	if len(missing) > 0 {
		return checks.Fail("%s does not exist", strings.Join(missing, ", ")).WithDetail(detail), nil
	}
	return checks.Pass("parses; %s exists", strings.Join(named, ", ")).WithDetail(detail), nil
}
