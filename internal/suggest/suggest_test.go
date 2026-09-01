package suggest

import (
	"context"
	"strings"
	"testing"

	"github.com/HamZeus95/sonde/internal/model"
	"github.com/HamZeus95/sonde/internal/parse"
)

// fakeDrafter stands in for the model. No test in this package calls an API:
// the drafting is the one part that needs a model, and everything worth testing
// is what happens to a draft afterwards.
type fakeDrafter struct{ drafts []Draft }

func (f fakeDrafter) Draft(context.Context, Request) ([]Draft, error) { return f.drafts, nil }

const runbook = `---
sonde:
  version: 1
  id: payments-scale-up
  owner: team-payments
  environment: prod-eu-1
---

# Runbook: Scale the Payments API

Use this when the queue depth alert has been firing for ten minutes.

## Step 1 — Confirm the deployment exists

    kubectl -n payments get deploy payments-api

## Step 2 — Check the dashboard

Open the Grafana board and confirm the saturation panel.

` + "```sonde" + `
id: dashboard-live
kind: http
check: status_is
url: https://grafana.corp.example/d/abc123
` + "```" + `

## Step 3 — Scale

    kubectl -n payments scale deploy payments-api --replicas=8
`

func TestAnalyse(t *testing.T) {
	request, err := Analyse("runbooks/payments.md", []byte(runbook))
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}

	if request.Meta.ID != "payments-scale-up" {
		t.Errorf("meta id = %q", request.Meta.ID)
	}
	want := []string{
		"Runbook: Scale the Payments API",
		"Step 1 — Confirm the deployment exists",
		"Step 2 — Check the dashboard",
		"Step 3 — Scale",
	}
	if len(request.Headings) != len(want) {
		t.Fatalf("headings = %d, want %d: %+v", len(request.Headings), len(want), request.Headings)
	}
	for i, text := range want {
		if request.Headings[i].Text != text {
			t.Errorf("heading %d = %q, want %q", i, request.Headings[i].Text, text)
		}
		if request.Headings[i].Index != i {
			t.Errorf("heading %d has index %d", i, request.Headings[i].Index)
		}
	}

	if len(request.Existing) != 1 || request.Existing[0].ID != "dashboard-live" {
		t.Errorf("existing = %+v", request.Existing)
	}

	// The model drafts from the steps, not from the answers.
	if strings.Contains(request.Prose, "status_is") {
		t.Error("the prose handed to the model still contains an existing assertion")
	}
	if !strings.Contains(request.Prose, "Open the Grafana board") {
		t.Error("stripping the blocks removed the prose around them")
	}

	// The catalogue tells the model what it may draft, and with which fields.
	if len(request.Catalogue) != len(model.Catalogue()) {
		t.Errorf("catalogue = %d entries", len(request.Catalogue))
	}
	for _, entry := range request.Catalogue {
		if len(entry.Fields) == 0 {
			t.Errorf("%s/%s offers no fields", entry.Kind, entry.Check)
		}
	}
}

func draft(over func(*Draft)) Draft {
	d := Draft{
		ID:           "payments-deploy-exists",
		Kind:         "kubernetes",
		Check:        "resource_exists",
		HeadingIndex: 1,
		Fields:       "resource: deployment/payments-api\nnamespace: payments",
		Rationale:    "Step 1 says the deployment exists.",
	}
	if over != nil {
		over(&d)
	}
	return d
}

func TestValidateAcceptsAGoodDraft(t *testing.T) {
	request, err := Analyse("runbooks/payments.md", []byte(runbook))
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}
	result := Validate(request, []byte(runbook), []Draft{draft(nil)})
	if len(result.Rejected) != 0 {
		t.Fatalf("rejected: %+v", result.Rejected)
	}
	if len(result.Suggestions) != 1 {
		t.Fatalf("suggestions = %d", len(result.Suggestions))
	}
	suggestion := result.Suggestions[0]
	if !strings.Contains(suggestion.Block, "```sonde\nid: payments-deploy-exists\nkind: kubernetes\ncheck: resource_exists\n") {
		t.Errorf("block:\n%s", suggestion.Block)
	}
	if suggestion.Heading != "Step 1 — Confirm the deployment exists" {
		t.Errorf("heading = %q", suggestion.Heading)
	}
}

// TestEverySuggestionParses is the guarantee that makes this command safe to
// use: whatever the model returns, what reaches the reviewer is a block the
// real parser accepts.
func TestEverySuggestionParses(t *testing.T) {
	request, err := Analyse("runbooks/payments.md", []byte(runbook))
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}
	drafts := []Draft{
		draft(nil),
		draft(func(d *Draft) { d.ID = "no-resource"; d.Fields = "namespace: payments" }),
		draft(func(d *Draft) { d.ID = "bad-yaml"; d.Fields = "resource: [unclosed" }),
		draft(func(d *Draft) { d.ID = "invented-check"; d.Check = "resource_is_healthy" }),
		draft(func(d *Draft) { d.ID = "invented-kind"; d.Kind = "terraform" }),
		draft(func(d *Draft) { d.ID = "dashboard-live" }),
		draft(func(d *Draft) { d.ID = "nowhere"; d.HeadingIndex = 99 }),
		draft(func(d *Draft) { d.ID = ""; d.Fields = "resource: deployment/x" }),
		draft(func(d *Draft) { d.ID = "has a space" }),
	}

	result := Validate(request, []byte(runbook), drafts)
	if len(result.Suggestions) != 1 {
		t.Fatalf("suggestions = %d, want 1: %+v", len(result.Suggestions), result.Suggestions)
	}
	if len(result.Rejected) != len(drafts)-1 {
		t.Fatalf("rejected = %d, want %d", len(result.Rejected), len(drafts)-1)
	}
	for _, rejection := range result.Rejected {
		if rejection.Reason == "" {
			t.Errorf("%q was rejected without a reason", rejection.Draft.ID)
		}
	}

	// Applying everything that survived leaves a document that still parses.
	updated := Apply([]byte(runbook), result.Suggestions)
	parsed, err := parse.Source("runbooks/payments.md", updated)
	if err != nil {
		t.Fatalf("the applied document does not parse: %v", err)
	}
	if len(parsed.Checks) != 2 {
		t.Errorf("checks after applying = %d, want 2", len(parsed.Checks))
	}
}

func TestApplyPlacesTheBlockInItsSection(t *testing.T) {
	request, err := Analyse("runbooks/payments.md", []byte(runbook))
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}
	result := Validate(request, []byte(runbook), []Draft{draft(nil)})
	updated := string(Apply([]byte(runbook), result.Suggestions))

	step1 := strings.Index(updated, "## Step 1")
	step2 := strings.Index(updated, "## Step 2")
	block := strings.Index(updated, "id: payments-deploy-exists")
	if block < step1 || block > step2 {
		t.Errorf("the block landed outside step 1:\n%s", updated)
	}
}

// TestApplyIsStableForSeveralBlocks pins the bottom-up insertion: anchors are
// line numbers in the original document, and inserting one block must not move
// where the next one goes.
func TestApplyIsStableForSeveralBlocks(t *testing.T) {
	request, err := Analyse("runbooks/payments.md", []byte(runbook))
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}
	result := Validate(request, []byte(runbook), []Draft{
		draft(nil),
		draft(func(d *Draft) {
			d.ID = "oncall-can-scale"
			d.Check = "can_i"
			d.HeadingIndex = 3
			d.Fields = "verb: patch\nresource: deployments\nnamespace: payments"
		}),
	})
	if len(result.Suggestions) != 2 {
		t.Fatalf("suggestions = %d: %+v", len(result.Suggestions), result.Rejected)
	}

	updated := string(Apply([]byte(runbook), result.Suggestions))
	first := strings.Index(updated, "id: payments-deploy-exists")
	second := strings.Index(updated, "id: oncall-can-scale")
	if first > second {
		t.Error("the blocks were applied in the wrong order")
	}
	if strings.Index(updated, "## Step 3") > second {
		t.Error("the can_i block landed before its own section")
	}
	if _, err := parse.Source("runbooks/payments.md", []byte(updated)); err != nil {
		t.Fatalf("the applied document does not parse: %v", err)
	}
}

func TestDiff(t *testing.T) {
	request, err := Analyse("runbooks/payments.md", []byte(runbook))
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}
	result := Validate(request, []byte(runbook), []Draft{draft(nil)})
	diff := Diff("runbooks/payments.md", []byte(runbook), result.Suggestions)

	for _, want := range []string{
		"--- a/runbooks/payments.md",
		"+++ b/runbooks/payments.md",
		"@@ -",
		"+```sonde",
		"+id: payments-deploy-exists",
	} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff is missing %q:\n%s", want, diff)
		}
	}
	// Only additions: this command never rewrites a line someone wrote.
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			t.Errorf("the diff removes a line: %q", line)
		}
	}
}

func TestDiffOfNothingIsEmpty(t *testing.T) {
	if diff := Diff("x.md", []byte(runbook), nil); diff != "" {
		t.Errorf("diff = %q", diff)
	}
}

// TestADocumentWithNoFrontmatterStillValidates covers the common starting
// point: a wiki page that has never heard of Sonde.
func TestADocumentWithNoFrontmatterStillValidates(t *testing.T) {
	plain := "# Scale the Payments API\n\n## Step 1\n\n    kubectl -n payments get deploy payments-api\n"
	request, err := Analyse("plain.md", []byte(plain))
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}
	if request.Meta.ID != "" {
		t.Error("a document with no frontmatter is not a runbook yet")
	}
	result := Validate(request, []byte(plain), []Draft{draft(nil)})
	if len(result.Suggestions) != 1 {
		t.Fatalf("suggestions = %d: %+v", len(result.Suggestions), result.Rejected)
	}
}

// TestThroughTheDrafterInterface walks the path the command takes: analyse,
// draft, validate, apply. The drafter is a fake, because the model is the one
// part of this that cannot be tested deterministically — and the one part that
// decides nothing.
func TestThroughTheDrafterInterface(t *testing.T) {
	source := []byte(runbook)
	request, err := Analyse("runbooks/payments.md", source)
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}

	drafter := fakeDrafter{drafts: []Draft{
		draft(nil),
		draft(func(d *Draft) { d.ID = "invented"; d.Check = "resource_is_healthy" }),
	}}
	drafts, err := drafter.Draft(context.Background(), request)
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}

	result := Validate(request, source, drafts)
	if len(result.Suggestions) != 1 || len(result.Rejected) != 1 {
		t.Fatalf("suggestions = %d, rejected = %d", len(result.Suggestions), len(result.Rejected))
	}
	if result.SchemaVersion != model.SchemaVersion {
		t.Errorf("schema version = %d", result.SchemaVersion)
	}
	if _, err := parse.Source("runbooks/payments.md", Apply(source, result.Suggestions)); err != nil {
		t.Fatalf("the applied document does not parse: %v", err)
	}
}
