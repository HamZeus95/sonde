package impact

import (
	"os"
	"path/filepath"
	"testing"
)

func canonicals(report Report) []string {
	out := make([]string, 0, len(report.Removed))
	for _, r := range report.Removed {
		out = append(out, r.Canonical)
	}
	return out
}

func TestManifests(t *testing.T) {
	report, err := Manifests("testdata/before", "testdata/after", "prod-eu-1")
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	got := canonicals(report)
	want := []string{"k8s://prod-eu-1/payments/deployment/payments-api"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("removed = %v, want %v", got, want)
	}
	if report.Removed[0].Source != "payments.yaml" {
		t.Errorf("source = %q", report.Removed[0].Source)
	}
}

// TestMovingAnObjectIsNotADeletion covers the reason the diff is by object
// identity rather than by file: a refactor that splits one manifest into two
// must not warn that every runbook is broken.
func TestMovingAnObjectIsNotADeletion(t *testing.T) {
	report, err := Manifests("testdata/before", "testdata/after", "prod-eu-1")
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	for _, r := range report.Removed {
		if r.Name == "db-creds" {
			t.Error("the Secret moved to another file; it was not deleted")
		}
	}
}

// TestNonKubernetesYAMLIsIgnored covers the repository full of YAML that has
// nothing to do with deployment.
func TestNonKubernetesYAMLIsIgnored(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "workflow.yaml"), "name: ci\non:\n  push:\n")
	write(t, filepath.Join(dir, "values.yaml"), "replicaCount: 3\nimage:\n  tag: v1\n")
	report, err := Manifests(dir, t.TempDir(), "prod-eu-1")
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("removed = %+v, want none", report.Removed)
	}
}

// TestWithoutAClusterTheComponentsCarryTheAnswer covers the common case: a
// manifest names no cluster, so the index is queried on the parts instead.
func TestWithoutAClusterTheComponentsCarryTheAnswer(t *testing.T) {
	report, err := Manifests("testdata/before", "testdata/after", "")
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	if len(report.Removed) != 1 {
		t.Fatalf("removed = %+v", report.Removed)
	}
	removal := report.Removed[0]
	if removal.Canonical != "" {
		t.Errorf("canonical = %q, want empty without a cluster", removal.Canonical)
	}
	if removal.Type != "deployment" || removal.Name != "payments-api" || removal.Namespace != "payments" {
		t.Errorf("components = %+v", removal)
	}
}

// TestMissingBeforeTreeIsEmpty covers a pull request that only adds files.
func TestMissingBeforeTreeIsEmpty(t *testing.T) {
	report, err := Manifests(filepath.Join(t.TempDir(), "absent"), "testdata/after", "prod-eu-1")
	if err != nil {
		t.Fatalf("Manifests: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Errorf("removed = %+v", report.Removed)
	}
}

func TestPlan(t *testing.T) {
	report, err := Plan("testdata/plan.json", "prod-eu-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := map[string]bool{
		"k8s://prod-eu-1/payments/deployment/payments-api":        true,
		"k8s://prod-eu-1/data/statefulset/postgres-primary":       true,
		"k8s://prod-eu-1/data/kafkatopic/orders":                  true,
		"dns://db-primary.internal/CNAME":                         true,
		"aws://123456789012/eu-central-1/rds/db/payments-primary": true,
	}
	for _, got := range canonicals(report) {
		if !want[got] {
			t.Errorf("unexpected removal %q", got)
		}
		delete(want, got)
	}
	for missing := range want {
		t.Errorf("missing removal %q", missing)
	}
}

// TestReplacementIsNotRemoval is the difference between a useful bot and a
// muted one: a resource that is destroyed and recreated is still there
// afterwards.
func TestReplacementIsNotRemoval(t *testing.T) {
	report, err := Plan("testdata/plan.json", "prod-eu-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, r := range report.Removed {
		if r.Name == "app-config" {
			t.Error("a delete-then-create is a replacement, not a removal")
		}
	}
}

// TestUnmappableChangesAreReported covers the gap being visible rather than
// silent: a resource nobody can look up should be said out loud.
func TestUnmappableChangesAreReported(t *testing.T) {
	report, err := Plan("testdata/plan.json", "prod-eu-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	byAddress := map[string]string{}
	for _, u := range report.Unmappable {
		byAddress[u.Source] = u.Reason
	}
	if reason, ok := byAddress["random_password.db"]; !ok || reason == "" {
		t.Errorf("a provider with no URI form should be reported: %v", byAddress)
	}
	if reason, ok := byAddress["aws_s3_bucket.evidence"]; !ok || reason == "" {
		t.Errorf("an ARN with no region or account should be reported: %v", byAddress)
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	first, err := Plan("testdata/plan.json", "prod-eu-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for range 10 {
		again, err := Plan("testdata/plan.json", "prod-eu-1")
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		for i := range first.Removed {
			if again.Removed[i] != first.Removed[i] {
				t.Fatal("the same plan produced a different order; a comment that reorders itself gets muted")
			}
		}
	}
}

func TestStripVersionSuffix(t *testing.T) {
	for input, want := range map[string]string{
		"deployment":        "deployment",
		"stateful_set_v1":   "stateful_set",
		"ingress_v1":        "ingress",
		"config_map_v2":     "config_map",
		"persistent_volume": "persistent_volume",
	} {
		if got := stripVersionSuffix(input); got != want {
			t.Errorf("stripVersionSuffix(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseARN(t *testing.T) {
	removal, err := parseARN("arn:aws:rds:eu-central-1:123456789012:db:payments-primary", "aws_db_instance")
	if err != nil {
		t.Fatalf("parseARN: %v", err)
	}
	if removal.Canonical != "aws://123456789012/eu-central-1/rds/db/payments-primary" {
		t.Errorf("canonical = %q", removal.Canonical)
	}
	for _, bad := range []string{
		"not-an-arn",
		"arn:aws:s3:::bucket",
	} {
		if _, err := parseARN(bad, "aws_thing"); err == nil {
			t.Errorf("parseARN(%q): expected an error", bad)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
