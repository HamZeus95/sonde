package kubernetes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/model"
)

// fakeClient answers from a table, so the runners can be tested without a
// cluster. The envtest-backed test in client_test.go covers the real client.
type fakeClient struct {
	objects map[string]map[string]any
	access  AccessResult
	err     error
	lastReq AccessRequest
}

func (f *fakeClient) Get(_ context.Context, target Target) (*unstructured.Unstructured, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := target.Namespace + "/" + target.Resource + "/" + target.Name
	object, ok := f.objects[key]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: target.Resource}, target.Name)
	}
	return &unstructured.Unstructured{Object: object}, nil
}

func (f *fakeClient) Access(_ context.Context, req AccessRequest) (AccessResult, error) {
	f.lastReq = req
	if f.err != nil {
		return AccessResult{}, f.err
	}
	return f.access, nil
}

func run(t *testing.T, client Client, check string, spec model.Spec) (checks.Outcome, error) {
	t.Helper()
	reg := checks.NewRegistry()
	if err := New(client).Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	runner, ok := reg.Runner(model.KindKubernetes, check)
	if !ok {
		t.Fatalf("kubernetes/%s is not registered", check)
	}
	return runner.Run(context.Background(), model.Meta{Version: 1, ID: "rb", Environment: "prod-eu-1"},
		model.Check{ID: "c", Kind: model.KindKubernetes, Check: check, Spec: spec, Enabled: true})
}

func deployment(replicas int64, image string) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "payments-api", "namespace": "payments", "uid": "abc"},
		"spec": map[string]any{
			"replicas": replicas,
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{"name": "api", "image": image}},
				},
			},
		},
	}
}

// at builds a target in the payments namespace.
func at(resource string) model.KubernetesTarget {
	return model.KubernetesTarget{Resource: resource, Namespace: "payments"}
}

func TestResourceExists(t *testing.T) {
	client := &fakeClient{objects: map[string]map[string]any{
		"payments/deployment/payments-api": deployment(3, "ghcr.io/corp/payments-api:1.4.2"),
	}}

	outcome, err := run(t, client, "resource_exists",
		&model.ResourceExistsSpec{KubernetesTarget: at("deployment/payments-api")})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != model.StatusPass {
		t.Errorf("status = %q (%s)", outcome.Status, outcome.Observed.Summary)
	}

	gone, err := run(t, client, "resource_exists",
		&model.ResourceExistsSpec{KubernetesTarget: at("deployment/retired")})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gone.Status != model.StatusFail {
		t.Errorf("a deleted object means the runbook is wrong, got %q", gone.Status)
	}
}

// TestAPIFailureIsAnError separates a cluster we cannot read from a resource
// that is gone.
func TestAPIFailureIsAnError(t *testing.T) {
	client := &fakeClient{err: apierrors.NewServiceUnavailable("apiserver is down")}
	if _, err := run(t, client, "resource_exists",
		&model.ResourceExistsSpec{KubernetesTarget: at("deployment/payments-api")}); err == nil {
		t.Fatal("an unreachable apiserver must be an error, not a failing assertion")
	}
}

func TestFieldEquals(t *testing.T) {
	client := &fakeClient{objects: map[string]map[string]any{
		"payments/deployment/payments-api": deployment(3, "ghcr.io/corp/payments-api:1.4.2"),
	}}
	tests := []struct {
		name  string
		path  string
		value string
		want  model.Status
	}{
		{"image still matches", "spec.template.spec.containers[0].image", `"ghcr.io/corp/payments-api:1.4.2"`, model.StatusPass},
		{"image moved on", "spec.template.spec.containers[0].image", `"ghcr.io/corp/payments-api:1.0.0"`, model.StatusFail},
		{"replica count as a number", "spec.replicas", `3`, model.StatusPass},
		{"a string is not a number", "spec.replicas", `"3"`, model.StatusFail},
		{"missing field", "spec.strategy.type", `"RollingUpdate"`, model.StatusFail},
		{"index out of range", "spec.template.spec.containers[4].image", `"x"`, model.StatusFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, err := run(t, client, "field_equals", &model.FieldEqualsSpec{
				KubernetesTarget: at("deployment/payments-api"),
				Path:             tt.path,
				Value:            json.RawMessage(tt.value),
			})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if outcome.Status != tt.want {
				t.Errorf("status = %q, want %q (%s)", outcome.Status, tt.want, outcome.Observed.Summary)
			}
		})
	}
}

func TestFieldGTE(t *testing.T) {
	client := &fakeClient{objects: map[string]map[string]any{
		"payments/deployment/payments-api": deployment(3, "img"),
	}}
	for _, tt := range []struct {
		name  string
		floor float64
		want  model.Status
	}{
		{"at the floor", 3, model.StatusPass},
		{"above the floor", 2, model.StatusPass},
		{"below the floor", 5, model.StatusFail},
	} {
		t.Run(tt.name, func(t *testing.T) {
			outcome, err := run(t, client, "field_gte", &model.FieldGTESpec{
				KubernetesTarget: at("deployment/payments-api"),
				Path:             "spec.replicas",
				Value:            tt.floor,
			})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if outcome.Status != tt.want {
				t.Errorf("status = %q, want %q (%s)", outcome.Status, tt.want, outcome.Observed.Summary)
			}
		})
	}

	t.Run("a non-numeric field has no answer", func(t *testing.T) {
		if _, err := run(t, client, "field_gte", &model.FieldGTESpec{
			KubernetesTarget: at("deployment/payments-api"),
			Path:             "metadata.name",
			Value:            1,
		}); err == nil {
			t.Fatal("comparing a string with >= should be an error, not a failure")
		}
	})
}

func TestCanI(t *testing.T) {
	t.Run("a denial means the runbook cannot be followed", func(t *testing.T) {
		client := &fakeClient{access: AccessResult{Allowed: false, Reason: "no binding"}}
		outcome, err := run(t, client, "can_i", &model.CanISpec{
			Verb: "patch", Resource: "deployments", Namespace: "payments", AsGroup: "system:sre-oncall",
		})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if outcome.Status != model.StatusFail {
			t.Errorf("status = %q, want fail", outcome.Status)
		}
		if !client.lastReq.SubjectReview() {
			t.Error("as_group must ask about another identity")
		}
		if got := client.lastReq.Groups; len(got) != 1 || got[0] != "system:sre-oncall" {
			t.Errorf("groups = %v", got)
		}
	})

	t.Run("no subject asks about the probe itself", func(t *testing.T) {
		client := &fakeClient{access: AccessResult{Allowed: true}}
		outcome, err := run(t, client, "can_i", &model.CanISpec{Verb: "get", Resource: "pods", Namespace: "payments"})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if outcome.Status != model.StatusPass {
			t.Errorf("status = %q, want pass", outcome.Status)
		}
		if client.lastReq.SubjectReview() {
			t.Error("a can_i with no subject must use the self review, which needs no grant")
		}
	})
}

// TestSecretKeyPresentReadsNoValues is the test that guards invariant 3.
func TestSecretKeyPresentReadsNoValues(t *testing.T) {
	const secretValue = "hunter2-do-not-leak"
	client := &fakeClient{objects: map[string]map[string]any{
		"data/secret/db-creds": {
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata":   map[string]any{"name": "db-creds", "namespace": "data"},
			"data":       map[string]any{"PGPASSWORD": secretValue, "PGUSER": secretValue},
		},
	}}
	spec := &model.SecretKeyPresentSpec{
		KubernetesTarget: model.KubernetesTarget{Resource: "secret/db-creds", Namespace: "data"},
		Key:              "PGPASSWORD",
	}
	outcome, err := run(t, client, "secret_key_present", spec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != model.StatusPass {
		t.Fatalf("status = %q (%s)", outcome.Status, outcome.Observed.Summary)
	}
	encoded, err := json.Marshal(outcome.Observed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), secretValue) {
		t.Fatalf("a secret value reached the result: %s", encoded)
	}

	missing := &model.SecretKeyPresentSpec{
		KubernetesTarget: model.KubernetesTarget{Resource: "secret/db-creds", Namespace: "data"},
		Key:              "PGPASSWORD_OLD",
	}
	gone, err := run(t, client, "secret_key_present", missing)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gone.Status != model.StatusFail {
		t.Errorf("status = %q, want fail", gone.Status)
	}
	if strings.Contains(gone.Observed.Summary, secretValue) {
		t.Errorf("a secret value reached the summary: %s", gone.Observed.Summary)
	}
}
