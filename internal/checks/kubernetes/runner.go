package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/model"
)

// Runner executes the kubernetes checks.
type Runner struct {
	client Client
}

// New returns a runner backed by client.
func New(client Client) *Runner { return &Runner{client: client} }

// Register wires this runner's checks into a registry.
func (r *Runner) Register(reg *checks.Registry) error {
	for check, run := range map[string]checks.RunnerFunc{
		"resource_exists":    r.resourceExists,
		"field_equals":       r.fieldEquals,
		"field_gte":          r.fieldGTE,
		"can_i":              r.canI,
		"secret_key_present": r.secretKeyPresent,
	} {
		if err := reg.Register(model.KindKubernetes, check, run); err != nil {
			return err
		}
	}
	return nil
}

// target resolves the object a check addresses, taking the cluster from the
// check when it sets one and from the runbook's environment otherwise — the
// same precedence the canonical URI uses, so that what runs and what is indexed
// can never disagree.
func target(meta model.Meta, t model.KubernetesTarget) (Target, error) {
	ref, err := model.ParseResourceRef(t.Resource)
	if err != nil {
		return Target{}, err
	}
	cluster := t.Cluster
	if cluster == "" {
		cluster = meta.Environment
	}
	return Target{Cluster: cluster, Namespace: t.Namespace, Resource: ref.Type, Name: ref.Name}, nil
}

func describeTarget(t Target) string {
	if t.Namespace == "" {
		return fmt.Sprintf("%s/%s", t.Resource, t.Name)
	}
	return fmt.Sprintf("%s/%s in %s", t.Resource, t.Name, t.Namespace)
}

func (r *Runner) resourceExists(ctx context.Context, meta model.Meta, check model.Check) (checks.Outcome, error) {
	spec, ok := check.Spec.(*model.ResourceExistsSpec)
	if !ok {
		return checks.Outcome{}, fmt.Errorf("kubernetes/resource_exists: spec is %T", check.Spec)
	}
	t, err := target(meta, spec.KubernetesTarget)
	if err != nil {
		return checks.Outcome{}, err
	}
	object, err := r.client.Get(ctx, t)
	if apierrors.IsNotFound(err) {
		return checks.Fail("%s does not exist", describeTarget(t)), nil
	}
	if err != nil {
		return checks.Outcome{}, err
	}
	return checks.Pass("%s exists", describeTarget(t)).
		WithDetail(map[string]any{"uid": string(object.GetUID()), "resource_version": object.GetResourceVersion()}), nil
}

func (r *Runner) fieldEquals(ctx context.Context, meta model.Meta, check model.Check) (checks.Outcome, error) {
	spec, ok := check.Spec.(*model.FieldEqualsSpec)
	if !ok {
		return checks.Outcome{}, fmt.Errorf("kubernetes/field_equals: spec is %T", check.Spec)
	}
	t, err := target(meta, spec.KubernetesTarget)
	if err != nil {
		return checks.Outcome{}, err
	}

	var want any
	if err := json.Unmarshal(spec.Value, &want); err != nil {
		return checks.Outcome{}, fmt.Errorf("read expected value: %w", err)
	}

	object, err := r.client.Get(ctx, t)
	if apierrors.IsNotFound(err) {
		return checks.Fail("%s does not exist", describeTarget(t)), nil
	}
	if err != nil {
		return checks.Outcome{}, err
	}

	got, found, err := lookupPath(object.Object, spec.Path)
	if err != nil {
		return checks.Outcome{}, err
	}
	detail := map[string]any{"path": spec.Path, "want": want}
	if !found {
		return checks.Fail("%s has no field %s", describeTarget(t), spec.Path).WithDetail(detail), nil
	}
	detail["got"] = got
	if !equalValues(got, want) {
		return checks.Fail("%s is %s, want %s", spec.Path, describe(got), describe(want)).WithDetail(detail), nil
	}
	return checks.Pass("%s is %s", spec.Path, describe(got)).WithDetail(detail), nil
}

func (r *Runner) fieldGTE(ctx context.Context, meta model.Meta, check model.Check) (checks.Outcome, error) {
	spec, ok := check.Spec.(*model.FieldGTESpec)
	if !ok {
		return checks.Outcome{}, fmt.Errorf("kubernetes/field_gte: spec is %T", check.Spec)
	}
	t, err := target(meta, spec.KubernetesTarget)
	if err != nil {
		return checks.Outcome{}, err
	}

	object, err := r.client.Get(ctx, t)
	if apierrors.IsNotFound(err) {
		return checks.Fail("%s does not exist", describeTarget(t)), nil
	}
	if err != nil {
		return checks.Outcome{}, err
	}

	got, found, err := lookupPath(object.Object, spec.Path)
	if err != nil {
		return checks.Outcome{}, err
	}
	detail := map[string]any{"path": spec.Path, "want": spec.Value}
	if !found {
		return checks.Fail("%s has no field %s", describeTarget(t), spec.Path).WithDetail(detail), nil
	}
	number, numeric := asNumber(got)
	if !numeric {
		// The field exists but is not a number, so the comparison the runbook
		// asks for has no answer. That is a broken assertion, not a broken
		// cluster, but it is also not a determination that the claim is false.
		return checks.Outcome{}, fmt.Errorf("%s is %s, which is not a number", spec.Path, describe(got))
	}
	detail["got"] = number
	if number < spec.Value {
		return checks.Fail("%s is %s, want at least %s", spec.Path, formatNumber(number), formatNumber(spec.Value)).WithDetail(detail), nil
	}
	return checks.Pass("%s is %s", spec.Path, formatNumber(number)).WithDetail(detail), nil
}

func (r *Runner) canI(ctx context.Context, meta model.Meta, check model.Check) (checks.Outcome, error) {
	spec, ok := check.Spec.(*model.CanISpec)
	if !ok {
		return checks.Outcome{}, fmt.Errorf("kubernetes/can_i: spec is %T", check.Spec)
	}
	cluster := spec.Cluster
	if cluster == "" {
		cluster = meta.Environment
	}
	req := AccessRequest{
		Cluster:     cluster,
		Verb:        spec.Verb,
		Resource:    spec.Resource,
		Subresource: spec.Subresource,
		Namespace:   spec.Namespace,
		Name:        spec.Name,
		User:        spec.AsUser,
	}
	if spec.AsGroup != "" {
		req.Groups = []string{spec.AsGroup}
	}

	answer, err := r.client.Access(ctx, req)
	if err != nil {
		return checks.Outcome{}, err
	}

	subject := "the probe"
	switch {
	case spec.AsUser != "":
		subject = spec.AsUser
	case spec.AsGroup != "":
		subject = spec.AsGroup
	}
	action := spec.Verb + " " + spec.Resource
	if spec.Subresource != "" {
		action += "/" + spec.Subresource
	}
	if spec.Namespace != "" {
		action += " in " + spec.Namespace
	}
	detail := map[string]any{"subject": subject, "verb": spec.Verb, "resource": spec.Resource,
		"subresource": spec.Subresource, "namespace": spec.Namespace,
		"allowed": answer.Allowed, "reason": answer.Reason}

	if !answer.Allowed {
		// A denial is a determination, and it means the runbook tells someone
		// to run a command they are not allowed to run. That is exactly the
		// kind of rot no other tool looks for.
		return checks.Fail("%s cannot %s", subject, action).WithDetail(detail), nil
	}
	return checks.Pass("%s can %s", subject, action).WithDetail(detail), nil
}

func (r *Runner) secretKeyPresent(ctx context.Context, meta model.Meta, check model.Check) (checks.Outcome, error) {
	spec, ok := check.Spec.(*model.SecretKeyPresentSpec)
	if !ok {
		return checks.Outcome{}, fmt.Errorf("kubernetes/secret_key_present: spec is %T", check.Spec)
	}
	t, err := target(meta, spec.KubernetesTarget)
	if err != nil {
		return checks.Outcome{}, err
	}

	object, err := r.client.Get(ctx, t)
	if apierrors.IsNotFound(err) {
		return checks.Fail("%s does not exist", describeTarget(t)), nil
	}
	if err != nil {
		return checks.Outcome{}, err
	}

	// Only the key set is read. The values in data are never touched: not
	// compared, not logged, not hashed, not put in an Observed. That is why
	// this reads the map's keys and never its values, and why the detail below
	// carries names and a count and nothing else.
	keys := secretKeys(object.Object)
	detail := map[string]any{"keys": keys, "key_count": len(keys)}
	for _, key := range keys {
		if key == spec.Key {
			return checks.Pass("%s has key %s", describeTarget(t), spec.Key).WithDetail(detail), nil
		}
	}
	// The summary names the missing key and counts the rest. The key names
	// themselves go in the detail, which only a check that asked for verbose
	// ever sees: a Secret's key set is inventory, and results are minimal by
	// default.
	return checks.Fail("%s has no key %s (%d other keys)", describeTarget(t), spec.Key, len(keys)).
		WithDetail(detail), nil
}

// secretKeys returns the names of the keys in a Secret, sorted, reading no
// value. Both data and stringData are consulted because a Secret applied from a
// manifest may be observed either way.
func secretKeys(object map[string]any) []string {
	seen := map[string]bool{}
	for _, field := range []string{"data", "stringData"} {
		node, ok := object[field].(map[string]any)
		if !ok {
			continue
		}
		for key := range node {
			seen[key] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
