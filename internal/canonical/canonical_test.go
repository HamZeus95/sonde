package canonical

import (
	"errors"
	"testing"

	"github.com/HamZeus95/sonde/internal/model"
)

func TestKubernetes(t *testing.T) {
	tests := []struct {
		name                               string
		cluster, namespace, kind, resource string
		want                               string
	}{
		{"plain", "prod-eu-1", "payments", "deployment", "payments-api",
			"k8s://prod-eu-1/payments/deployment/payments-api"},
		{"plural kind", "prod-eu-1", "payments", "deployments", "payments-api",
			"k8s://prod-eu-1/payments/deployment/payments-api"},
		{"kubectl short name", "prod-eu-1", "payments", "deploy", "payments-api",
			"k8s://prod-eu-1/payments/deployment/payments-api"},
		{"api group suffix", "prod-eu-1", "payments", "Deployment.apps", "payments-api",
			"k8s://prod-eu-1/payments/deployment/payments-api"},
		{"cluster scoped", "prod-eu-1", "", "namespace", "payments",
			"k8s://prod-eu-1/_/namespace/payments"},
		{"unknown kind is left alone", "prod-eu-1", "data", "kafkatopic", "orders",
			"k8s://prod-eu-1/data/kafkatopic/orders"},
		{"mixed case cluster", "PROD-EU-1", "Payments", "deployment", "payments-api",
			"k8s://prod-eu-1/payments/deployment/payments-api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Kubernetes(tt.cluster, tt.namespace, tt.kind, tt.resource); got != tt.want {
				t.Errorf("Kubernetes() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTTP(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "http://grafana.corp.example/d/abc123", "http://grafana.corp.example/d/abc123"},
		// https and http address the same resource: the scheme in a canonical
		// URI names the provider, not the wire protocol.
		{"https folds onto http", "https://grafana.corp.example/d/abc123", "http://grafana.corp.example/d/abc123"},
		{"query stripped", "https://grafana.corp.example/d/abc123?orgId=1&from=now-6h", "http://grafana.corp.example/d/abc123"},
		{"fragment stripped", "https://wiki.corp.example/page#section", "http://wiki.corp.example/page"},
		{"default port dropped", "https://grafana.corp.example:443/d/abc123", "http://grafana.corp.example/d/abc123"},
		{"non-default port kept", "http://grafana.corp.example:8080/d/abc", "http://grafana.corp.example:8080/d/abc"},
		{"host lowercased", "https://Grafana.Corp.Example/D/abc", "http://grafana.corp.example/D/abc"},
		{"trailing slash trimmed", "https://status.corp.example/", "http://status.corp.example"},
		{"root", "https://status.corp.example", "http://status.corp.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HTTP(tt.in)
			if err != nil {
				t.Fatalf("HTTP(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("HTTP(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDNS(t *testing.T) {
	tests := []struct{ name, host, recordType, want string }{
		{"plain", "db-primary.internal", "CNAME", "dns://db-primary.internal/CNAME"},
		{"root dot dropped", "db-primary.internal.", "CNAME", "dns://db-primary.internal/CNAME"},
		{"case folded", "DB-Primary.Internal", "cname", "dns://db-primary.internal/CNAME"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DNS(tt.host, tt.recordType); got != tt.want {
				t.Errorf("DNS() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIdentityGroup(t *testing.T) {
	want := "idp://keycloak/prod/group/sre-oncall"
	if got := IdentityGroup("Keycloak", "Prod", "sre-oncall"); got != want {
		t.Errorf("IdentityGroup() = %q, want %q", got, want)
	}
}

func TestAWS(t *testing.T) {
	want := "aws://123456789012/eu-central-1/rds/db/payments-primary"
	if got := AWS("123456789012", "eu-central-1", "RDS", "db", "payments-primary"); got != want {
		t.Errorf("AWS() = %q, want %q", got, want)
	}
}

func TestForCheck(t *testing.T) {
	meta := model.Meta{Version: 1, ID: "rb", Environment: "prod-eu-1"}
	tests := []struct {
		name    string
		meta    model.Meta
		spec    model.Spec
		want    string
		wantErr error
	}{
		{
			name: "environment supplies the cluster",
			meta: meta,
			spec: &model.ResourceExistsSpec{KubernetesTarget: model.KubernetesTarget{
				Resource: "deployment/payments-api", Namespace: "payments"}},
			want: "k8s://prod-eu-1/payments/deployment/payments-api",
		},
		{
			name: "an explicit cluster wins over the environment",
			meta: meta,
			spec: &model.ResourceExistsSpec{KubernetesTarget: model.KubernetesTarget{
				Resource: "deployment/payments-api", Namespace: "payments", Cluster: "prod-eu-2"}},
			want: "k8s://prod-eu-2/payments/deployment/payments-api",
		},
		{
			name: "no cluster anywhere",
			meta: model.Meta{Version: 1, ID: "rb"},
			spec: &model.ResourceExistsSpec{KubernetesTarget: model.KubernetesTarget{
				Resource: "deployment/payments-api", Namespace: "payments"}},
			wantErr: ErrNoCluster,
		},
		{
			name:    "can_i names a type, not an object",
			meta:    meta,
			spec:    &model.CanISpec{Verb: "patch", Resource: "deployments", Namespace: "payments"},
			wantErr: ErrNotIndexable,
		},
		{
			name:    "a command is resolved at execution time",
			meta:    meta,
			spec:    &model.ParsesSpec{Command: "kubectl get deploy payments-api"},
			wantErr: ErrNotIndexable,
		},
		{
			name: "identity falls back to the environment as the realm",
			meta: meta,
			spec: &model.GroupExistsSpec{Provider: "keycloak", Group: "sre-oncall"},
			want: "idp://keycloak/prod-eu-1/group/sre-oncall",
		},
		{
			name:    "no realm anywhere",
			meta:    model.Meta{Version: 1, ID: "rb"},
			spec:    &model.GroupExistsSpec{Provider: "keycloak", Group: "sre-oncall"},
			wantErr: ErrNoRealm,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ForCheck(tt.meta, model.Check{Spec: tt.spec})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				if got != "" {
					t.Errorf("expected no URI alongside %v, got %q", tt.wantErr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ForCheck: %v", err)
			}
			if got != tt.want {
				t.Errorf("ForCheck() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestEveryCatalogueEntryHasARule fails when a check kind is added without the
// canonicalisation step, which is step 7 of the seven in docs/design.md §6. Missing
// it silently drops the check out of the reverse index — the one feature the
// product cannot afford to lose entries from.
//
// The specs here are zero-valued, so most of them fail validation. That is
// fine: the only thing under test is that the type switch has an arm for every
// spec in the catalogue.
func TestEveryCatalogueEntryHasARule(t *testing.T) {
	meta := model.Meta{Version: 1, ID: "rb", Environment: "prod-eu-1"}
	for _, def := range model.Catalogue() {
		t.Run(string(def.Kind)+"/"+def.Check, func(t *testing.T) {
			_, err := ForCheck(meta, model.Check{Kind: def.Kind, Check: def.Check, Spec: def.New()})
			if errors.Is(err, ErrNoRule) {
				t.Errorf("%v", err)
			}
		})
	}
}

// TestPurity pins the promise the reverse index rests on: the same input
// produces the same URI, every time, with no I/O in between.
func TestPurity(t *testing.T) {
	meta := model.Meta{Version: 1, ID: "rb", Environment: "prod-eu-1"}
	check := model.Check{Spec: &model.ResourceExistsSpec{KubernetesTarget: model.KubernetesTarget{
		Resource: "deploy/payments-api", Namespace: "payments"}}}
	first, err := ForCheck(meta, check)
	if err != nil {
		t.Fatalf("ForCheck: %v", err)
	}
	for range 100 {
		got, err := ForCheck(meta, check)
		if err != nil || got != first {
			t.Fatalf("ForCheck is not deterministic: %q then %q (%v)", first, got, err)
		}
	}
}
