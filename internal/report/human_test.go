package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/HamZeus95/sonde/internal/model"
)

func TestHumanDocumentEmpty(t *testing.T) {
	var out bytes.Buffer
	if err := HumanDocument(&out, model.Document{SchemaVersion: model.SchemaVersion}); err != nil {
		t.Fatalf("HumanDocument: %v", err)
	}
	// Finding nothing is the common first run in a repository that has not
	// added frontmatter yet, so the empty case says what to do next.
	if !strings.Contains(out.String(), "frontmatter") {
		t.Errorf("the empty case should explain what a runbook is:\n%s", out.String())
	}
}

func TestHumanDocumentShowsWhatMatters(t *testing.T) {
	doc := model.Document{
		SchemaVersion: model.SchemaVersion,
		Runbooks: []model.Runbook{{
			Path: "runbooks/db-failover.md",
			Meta: model.Meta{Version: 1, ID: "db-failover", Environment: "prod-eu-1", Criticality: model.CriticalityHigh},
			Checks: []model.Check{
				{ID: "exists", Kind: model.KindKubernetes, Check: "resource_exists", Line: 12, Enabled: true,
					Canonical: "k8s://prod-eu-1/data/statefulset/postgres"},
				{ID: "retired", Kind: model.KindHTTP, Check: "status_is", Line: 30, Enabled: false},
			},
			Warnings: []model.Warning{{Line: 30, CheckID: "retired", Message: "unknown field \"retries\""}},
		}},
	}
	var out bytes.Buffer
	if err := HumanDocument(&out, doc); err != nil {
		t.Fatalf("HumanDocument: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"runbooks/db-failover.md", "db-failover", "prod-eu-1", "high",
		"k8s://prod-eu-1/data/statefulset/postgres",
		"not indexed", "(disabled)", "unknown field",
		"1 runbook, 2 checks, 1 warning",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
}
