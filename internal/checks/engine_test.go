package checks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HamZeus95/sonde/internal/model"
)

func check(id string, opts ...func(*model.Check)) model.Check {
	c := model.Check{
		ID:      id,
		Kind:    model.KindHTTP,
		Check:   "status_is",
		Spec:    &model.StatusIsSpec{URL: "https://example.com"},
		Enabled: true,
	}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

func runbook(checks ...model.Check) model.Runbook {
	return model.Runbook{
		Path:   "runbooks/rb.md",
		Meta:   model.Meta{Version: 1, ID: "rb", Environment: "prod-eu-1"},
		Checks: checks,
	}
}

func registryWith(t *testing.T, runner Runner) *Registry {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Register(model.KindHTTP, "status_is", runner); err != nil {
		t.Fatalf("register: %v", err)
	}
	return reg
}

// TestErrorIsNotFailure is the engine's most important behaviour. A runner that
// cannot reach the infrastructure must never produce a result that says the
// runbook is wrong.
func TestErrorIsNotFailure(t *testing.T) {
	reg := registryWith(t, RunnerFunc(func(context.Context, model.Meta, model.Check) (Outcome, error) {
		return Outcome{}, errors.New("connection refused")
	}))
	run := NewEngine(reg).Run(context.Background(), []model.Runbook{runbook(check("c"))})

	if got := run.Results[0].Status; got != model.StatusError {
		t.Fatalf("status = %q, want error", got)
	}
	if run.Summary.Failed != 0 {
		t.Errorf("an unreachable target must not count as a failure: %+v", run.Summary)
	}
	if run.Summary.Errored != 1 {
		t.Errorf("summary = %+v", run.Summary)
	}
	if run.Results[0].Observed.Summary != "connection refused" {
		t.Errorf("summary = %q", run.Results[0].Observed.Summary)
	}
}

func TestDisabledChecksAreSkipped(t *testing.T) {
	reg := registryWith(t, RunnerFunc(func(context.Context, model.Meta, model.Check) (Outcome, error) {
		t.Error("a disabled check must not be executed")
		return Pass("unreachable"), nil
	}))
	disabled := check("c", func(c *model.Check) { c.Enabled = false })
	run := NewEngine(reg).Run(context.Background(), []model.Runbook{runbook(disabled)})

	if got := run.Results[0].Status; got != model.StatusSkipped {
		t.Fatalf("status = %q, want skipped", got)
	}
	if run.Summary.Skipped != 1 {
		t.Errorf("summary = %+v", run.Summary)
	}
}

// TestUnsupportedCheckIsAnError covers a build without a runner for a check
// that exists in the catalogue: a probe with no identity credentials, say.
func TestUnsupportedCheckIsAnError(t *testing.T) {
	run := NewEngine(NewRegistry()).Run(context.Background(), []model.Runbook{runbook(check("c"))})
	if got := run.Results[0].Status; got != model.StatusError {
		t.Fatalf("status = %q, want error", got)
	}
	if run.Summary.Failed != 0 {
		t.Error("an unsupported check must not be reported as a failing assertion")
	}
}

// TestDetailNeedsVerbose pins invariant 6: results are minimal by default.
func TestDetailNeedsVerbose(t *testing.T) {
	reg := registryWith(t, RunnerFunc(func(context.Context, model.Meta, model.Check) (Outcome, error) {
		return Pass("200 OK").WithDetail(map[string]any{"body": "everything"}), nil
	}))

	quiet := NewEngine(reg).Run(context.Background(), []model.Runbook{runbook(check("c"))})
	if quiet.Results[0].Observed.Detail != nil {
		t.Errorf("detail leaked without verbose: %v", quiet.Results[0].Observed.Detail)
	}

	loud := NewEngine(reg).Run(context.Background(), []model.Runbook{
		runbook(check("c", func(c *model.Check) { c.Verbose = true })),
	})
	if loud.Results[0].Observed.Detail == nil {
		t.Error("verbose asked for detail and got none")
	}
}

func TestTimeoutIsReportedAsAnError(t *testing.T) {
	reg := registryWith(t, RunnerFunc(func(ctx context.Context, _ model.Meta, _ model.Check) (Outcome, error) {
		<-ctx.Done()
		return Outcome{}, ctx.Err()
	}))
	slow := check("c", func(c *model.Check) { c.Timeout = &model.Duration{Duration: 10 * time.Millisecond} })
	run := NewEngine(reg).Run(context.Background(), []model.Runbook{runbook(slow)})

	if got := run.Results[0].Status; got != model.StatusError {
		t.Fatalf("status = %q, want error", got)
	}
	if summary := run.Results[0].Observed.Summary; summary == "" {
		t.Error("a timeout should say so")
	}
}

// TestResultOrderIsStable pins that concurrency changes when a check runs, not
// where its result lands: golden output and diffs depend on it.
func TestResultOrderIsStable(t *testing.T) {
	reg := registryWith(t, RunnerFunc(func(context.Context, model.Meta, model.Check) (Outcome, error) {
		return Pass("ok"), nil
	}))
	var checks []model.Check
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		checks = append(checks, check(id))
	}
	for range 5 {
		run := NewEngine(reg, WithConcurrency(4)).Run(context.Background(), []model.Runbook{runbook(checks...)})
		for i, r := range run.Results {
			if r.CheckID != checks[i].ID {
				t.Fatalf("result %d is %q, want %q", i, r.CheckID, checks[i].ID)
			}
		}
	}
}

func TestRegistryRejectsUnknownAndDuplicatePairs(t *testing.T) {
	reg := NewRegistry()
	runner := RunnerFunc(func(context.Context, model.Meta, model.Check) (Outcome, error) {
		return Pass("ok"), nil
	})
	if err := reg.Register("terraform", "resource_exists", runner); err == nil {
		t.Error("registering a check outside the catalogue should fail")
	}
	if err := reg.Register(model.KindHTTP, "status_is", runner); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Register(model.KindHTTP, "status_is", runner); err == nil {
		t.Error("registering the same pair twice should fail")
	}
	if got := reg.Supported(); len(got) != 1 || got[0] != "http/status_is" {
		t.Errorf("Supported() = %v", got)
	}
	if len(reg.Unsupported()) != len(model.Catalogue())-1 {
		t.Errorf("Unsupported() = %v", reg.Unsupported())
	}
}
