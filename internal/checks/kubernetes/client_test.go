package kubernetes

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/model"
)

// startCluster brings up a real API server with envtest — never a live cluster,
// and never a cluster shared with anything else.
//
// The test is skipped rather than failed when the control-plane binaries are
// absent, so that `go test ./...` works on a machine that has not run
// setup-envtest. CI installs them, so CI runs it.
func startCluster(t *testing.T) *rest.Config {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset: run `setup-envtest use -p env` and export it to run this test")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	return cfg
}

func TestClientAgainstRealAPI(t *testing.T) {
	cfg := startCluster(t)
	client, err := NewClusterFromConfig(cfg)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const namespace = "payments"
	if _, err := typed.CoreV1().Namespaces().Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	replicas := int32(3)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "payments-api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "payments-api"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "api", Image: "ghcr.io/corp/payments-api:1.4.2"},
				}},
			},
		},
	}
	if _, err := typed.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if _, err := typed.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: namespace},
		StringData: map[string]string{"PGPASSWORD": "hunter2-do-not-leak"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create secret: %v", err)
	}

	reg := checks.NewRegistry()
	if err := New(client).Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	meta := model.Meta{Version: 1, ID: "rb", Environment: "test"}
	run := func(t *testing.T, name string, spec model.Spec) checks.Outcome {
		t.Helper()
		runner, ok := reg.Runner(model.KindKubernetes, name)
		if !ok {
			t.Fatalf("kubernetes/%s is not registered", name)
		}
		outcome, err := runner.Run(ctx, meta, model.Check{
			ID: "c", Kind: model.KindKubernetes, Check: name, Spec: spec, Enabled: true,
		})
		if err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
		return outcome
	}

	// Every spelling a runbook might use has to reach the same object, because
	// the canonical URI folds them all together too.
	t.Run("resource type spellings", func(t *testing.T) {
		for _, resource := range []string{
			"deployment/payments-api", "deployments/payments-api",
			"deploy/payments-api", "deployment.apps/payments-api",
		} {
			outcome := run(t, "resource_exists", &model.ResourceExistsSpec{
				KubernetesTarget: model.KubernetesTarget{Resource: resource, Namespace: namespace},
			})
			if outcome.Status != model.StatusPass {
				t.Errorf("%s: status = %q (%s)", resource, outcome.Status, outcome.Observed.Summary)
			}
		}
	})

	t.Run("cluster scoped resource", func(t *testing.T) {
		outcome := run(t, "resource_exists", &model.ResourceExistsSpec{
			KubernetesTarget: model.KubernetesTarget{Resource: "namespace/payments"},
		})
		if outcome.Status != model.StatusPass {
			t.Errorf("status = %q (%s)", outcome.Status, outcome.Observed.Summary)
		}
	})

	t.Run("field_equals reads the live image", func(t *testing.T) {
		outcome := run(t, "field_equals", &model.FieldEqualsSpec{
			KubernetesTarget: model.KubernetesTarget{Resource: "deployment/payments-api", Namespace: namespace},
			Path:             "spec.template.spec.containers[0].image",
			Value:            json.RawMessage(`"ghcr.io/corp/payments-api:1.4.2"`),
		})
		if outcome.Status != model.StatusPass {
			t.Errorf("status = %q (%s)", outcome.Status, outcome.Observed.Summary)
		}
	})

	t.Run("field_gte reads the live replica count", func(t *testing.T) {
		outcome := run(t, "field_gte", &model.FieldGTESpec{
			KubernetesTarget: model.KubernetesTarget{Resource: "deployment/payments-api", Namespace: namespace},
			Path:             "spec.replicas",
			Value:            3,
		})
		if outcome.Status != model.StatusPass {
			t.Errorf("status = %q (%s)", outcome.Status, outcome.Observed.Summary)
		}
	})

	t.Run("secret_key_present never returns the value", func(t *testing.T) {
		outcome := run(t, "secret_key_present", &model.SecretKeyPresentSpec{
			KubernetesTarget: model.KubernetesTarget{Resource: "secret/db-creds", Namespace: namespace},
			Key:              "PGPASSWORD",
		})
		if outcome.Status != model.StatusPass {
			t.Fatalf("status = %q (%s)", outcome.Status, outcome.Observed.Summary)
		}
		encoded, err := json.Marshal(outcome.Observed)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(encoded), "hunter2-do-not-leak") {
			t.Fatalf("a secret value reached the result: %s", encoded)
		}
	})

	t.Run("can_i about the probe itself", func(t *testing.T) {
		outcome := run(t, "can_i", &model.CanISpec{Verb: "get", Resource: "deployments", Namespace: namespace})
		if outcome.Status != model.StatusPass {
			t.Errorf("the envtest admin should be allowed: %q (%s)", outcome.Status, outcome.Observed.Summary)
		}
	})

	// This is the regression test for a bug the k3d demo caught: a review sent
	// without the resource's API group asks about a core-group "deployments"
	// that does not exist, and the API answers no. The check then reported that
	// the on-call group could not do something it could — a false accusation,
	// which is the failure mode the product cannot afford.
	t.Run("can_i resolves the resource API group", func(t *testing.T) {
		// The role is created here rather than reusing the built-in edit
		// ClusterRole, which a bare envtest control plane does not bootstrap.
		if _, err := typed.RbacV1().Roles(namespace).Create(ctx, &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "scale-payments", Namespace: namespace},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"apps"},
				Resources: []string{"deployments"},
				Verbs:     []string{"get", "list", "patch"},
			}},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create role: %v", err)
		}
		if _, err := typed.RbacV1().RoleBindings(namespace).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "sre-oncall-scale", Namespace: namespace},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "scale-payments"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "system:sre-oncall"},
			},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create rolebinding: %v", err)
		}
		outcome := run(t, "can_i", &model.CanISpec{
			Verb: "patch", Resource: "deployments", Namespace: namespace, AsGroup: "system:sre-oncall",
		})
		if outcome.Status != model.StatusPass {
			t.Errorf("status = %q, want pass (%s)", outcome.Status, outcome.Observed.Summary)
		}
	})

	t.Run("can_i about a subresource", func(t *testing.T) {
		if _, err := typed.RbacV1().Roles(namespace).Create(ctx, &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "exec-into-pods", Namespace: namespace},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"pods/exec"},
				Verbs:     []string{"create"},
			}},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create role: %v", err)
		}
		if _, err := typed.RbacV1().RoleBindings(namespace).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "dba-exec", Namespace: namespace},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "exec-into-pods"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "system:db-operators"},
			},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create rolebinding: %v", err)
		}

		allowed := run(t, "can_i", &model.CanISpec{
			Verb: "create", Resource: "pods", Subresource: "exec",
			Namespace: namespace, AsGroup: "system:db-operators",
		})
		if allowed.Status != model.StatusPass {
			t.Errorf("status = %q, want pass (%s)", allowed.Status, allowed.Observed.Summary)
		}
		// The grant is on the subresource alone, so the same verb on the pod
		// itself must still come back denied — which is what proves the
		// subresource reached the review rather than being dropped.
		denied := run(t, "can_i", &model.CanISpec{
			Verb: "create", Resource: "pods",
			Namespace: namespace, AsGroup: "system:db-operators",
		})
		if denied.Status != model.StatusFail {
			t.Errorf("status = %q, want fail (%s)", denied.Status, denied.Observed.Summary)
		}
	})

	t.Run("can_i about a group with no binding", func(t *testing.T) {
		outcome := run(t, "can_i", &model.CanISpec{
			Verb: "delete", Resource: "namespaces", AsGroup: "system:unbound-group",
		})
		// The group has no bindings, so the honest answer is that it cannot do
		// what the runbook says it can.
		if outcome.Status != model.StatusFail {
			t.Errorf("status = %q, want fail (%s)", outcome.Status, outcome.Observed.Summary)
		}
	})

	t.Run("a deleted object fails", func(t *testing.T) {
		if err := typed.AppsV1().Deployments(namespace).Delete(ctx, "payments-api", metav1.DeleteOptions{}); err != nil {
			t.Fatalf("delete deployment: %v", err)
		}
		// This is the loop the whole product is built on: the infrastructure
		// changed, the document did not, and the check goes red.
		outcome := run(t, "resource_exists", &model.ResourceExistsSpec{
			KubernetesTarget: model.KubernetesTarget{Resource: "deployment/payments-api", Namespace: namespace},
		})
		if outcome.Status != model.StatusFail {
			t.Errorf("status = %q, want fail (%s)", outcome.Status, outcome.Observed.Summary)
		}
	})

	t.Run("an unknown resource type is an error", func(t *testing.T) {
		runner, _ := reg.Runner(model.KindKubernetes, "resource_exists")
		_, err := runner.Run(ctx, meta, model.Check{
			ID: "c", Kind: model.KindKubernetes, Check: "resource_exists", Enabled: true,
			Spec: &model.ResourceExistsSpec{KubernetesTarget: model.KubernetesTarget{
				Resource: "kafkatopic/orders", Namespace: namespace}},
		})
		if err == nil {
			t.Error("a type the cluster does not know is an error, not a missing object")
		}
		if apierrors.IsNotFound(err) {
			t.Error("an unresolvable type must not look like a deleted object")
		}
	})
}
