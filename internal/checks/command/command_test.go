package command

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/checks/kubernetes"
	"github.com/HamZeus95/sonde/internal/model"
)

// fakeClient records what was looked up, which is how the test proves the
// command was read and not run.
type fakeClient struct {
	exists  map[string]bool
	lookups []kubernetes.Target
}

func (f *fakeClient) Get(_ context.Context, target kubernetes.Target) (*unstructured.Unstructured, error) {
	f.lookups = append(f.lookups, target)
	if !f.exists[target.Resource+"/"+target.Name] {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: target.Resource}, target.Name)
	}
	return &unstructured.Unstructured{Object: map[string]any{}}, nil
}

func (f *fakeClient) Access(context.Context, kubernetes.AccessRequest) (kubernetes.AccessResult, error) {
	return kubernetes.AccessResult{}, nil
}

func run(t *testing.T, client kubernetes.Client, spec *model.ParsesSpec) (checks.Outcome, error) {
	t.Helper()
	reg := checks.NewRegistry()
	if err := New(client).Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	runner, _ := reg.Runner(model.KindCommand, "parses")
	return runner.Run(context.Background(), model.Meta{Version: 1, ID: "rb", Environment: "prod-eu-1"},
		model.Check{ID: "c", Kind: model.KindCommand, Check: "parses", Spec: spec, Enabled: true})
}

func TestParsesVerifiesTheObjectsNamed(t *testing.T) {
	client := &fakeClient{exists: map[string]bool{"deploy/payments-api": true}}
	outcome, err := run(t, client, &model.ParsesSpec{
		Command: "kubectl -n payments get deploy payments-api",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != model.StatusPass {
		t.Errorf("status = %q (%s)", outcome.Status, outcome.Observed.Summary)
	}
	if len(client.lookups) != 1 || client.lookups[0].Namespace != "payments" {
		t.Errorf("lookups = %+v", client.lookups)
	}
}

func TestParsesFailsWhenTheObjectIsGone(t *testing.T) {
	client := &fakeClient{exists: map[string]bool{}}
	outcome, err := run(t, client, &model.ParsesSpec{
		Command: "kubectl -n payments get deploy payments-api", Namespace: "payments",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != model.StatusFail {
		t.Errorf("status = %q, want fail", outcome.Status)
	}
}

// TestADestructiveCommandIsOnlyRead is the invariant this check has to carry:
// the documented command may be a delete, and Sonde still only looks.
func TestADestructiveCommandIsOnlyRead(t *testing.T) {
	client := &fakeClient{exists: map[string]bool{"statefulset/postgres-primary": true}}
	outcome, err := run(t, client, &model.ParsesSpec{
		Command:   "kubectl -n data exec statefulset/postgres-primary -- pg_ctl promote",
		Namespace: "data",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != model.StatusPass {
		t.Errorf("status = %q (%s)", outcome.Status, outcome.Observed.Summary)
	}
	if len(client.lookups) != 1 {
		t.Fatalf("lookups = %+v", client.lookups)
	}
	if got := client.lookups[0]; got.Resource != "statefulset" || got.Name != "postgres-primary" {
		t.Errorf("looked up %+v", got)
	}
}

// TestNonKubectlIsAnError keeps an unverifiable claim from reporting as a pass.
func TestNonKubectlIsAnError(t *testing.T) {
	client := &fakeClient{}
	if _, err := run(t, client, &model.ParsesSpec{Command: "aws rds describe-db-instances"}); err == nil {
		t.Fatal("a command this build cannot verify must be an error, not a pass")
	}
	if len(client.lookups) != 0 {
		t.Error("nothing should have been looked up")
	}
}

func TestCommandNamingNoObject(t *testing.T) {
	outcome, err := run(t, &fakeClient{}, &model.ParsesSpec{Command: "kubectl get pods -n payments"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != model.StatusPass {
		t.Errorf("status = %q (%s)", outcome.Status, outcome.Observed.Summary)
	}
}
