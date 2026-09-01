package checks

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/HamZeus95/sonde/internal/model"
)

// DefaultConcurrency is how many checks run at once. Checks are almost entirely
// waiting on a remote API, so this is about not hammering a cluster rather than
// about CPU.
const DefaultConcurrency = 8

// Engine executes the checks in a set of runbooks.
type Engine struct {
	registry    *Registry
	concurrency int
	// now is injectable so that tests can assert on timestamps.
	now func() time.Time
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

// WithConcurrency sets how many checks run at once.
func WithConcurrency(n int) EngineOption {
	return func(e *Engine) {
		if n > 0 {
			e.concurrency = n
		}
	}
}

// WithClock replaces the clock, for tests.
func WithClock(now func() time.Time) EngineOption {
	return func(e *Engine) {
		if now != nil {
			e.now = now
		}
	}
}

// NewEngine returns an engine backed by a registry.
func NewEngine(registry *Registry, opts ...EngineOption) *Engine {
	e := &Engine{registry: registry, concurrency: DefaultConcurrency, now: time.Now}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// job is one check to execute, kept with its runbook so results can name it.
type job struct {
	runbook model.Runbook
	check   model.Check
}

// Run executes every check in every runbook and returns the results in a
// deterministic order: runbook order, then the order the checks appear in the
// document. Concurrency changes when a check runs, never where its result lands.
func (e *Engine) Run(ctx context.Context, runbooks []model.Runbook) model.Run {
	started := e.now().UTC()

	var jobs []job
	for _, rb := range runbooks {
		for _, check := range rb.Checks {
			jobs = append(jobs, job{runbook: rb, check: check})
		}
	}

	results := make([]model.Result, len(jobs))
	queue := make(chan int)
	var wg sync.WaitGroup
	workers := min(e.concurrency, max(len(jobs), 1))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				results[i] = e.runOne(ctx, jobs[i])
			}
		}()
	}
	for i := range jobs {
		queue <- i
	}
	close(queue)
	wg.Wait()

	return model.Run{
		SchemaVersion: model.SchemaVersion,
		StartedAt:     started,
		FinishedAt:    e.now().UTC(),
		Results:       results,
		Summary:       model.Summarise(results),
	}
}

// Execute runs one check and returns its result.
//
// The probe receives work one check at a time rather than one document at a
// time, and needs the same semantics Run applies: the disabled check, the
// missing runner, the timeout, and the rule that detail needs verbose.
func (e *Engine) Execute(ctx context.Context, runbook model.Runbook, check model.Check) model.Result {
	return e.runOne(ctx, job{runbook: runbook, check: check})
}

func (e *Engine) runOne(ctx context.Context, j job) model.Result {
	result := model.Result{
		RunbookPath: j.runbook.Path,
		RunbookID:   j.runbook.Meta.ID,
		CheckID:     j.check.ID,
		Kind:        j.check.Kind,
		Check:       j.check.Check,
		Canonical:   j.check.Canonical,
		Line:        j.check.Line,
		RanAt:       e.now().UTC(),
	}

	if !j.check.Enabled {
		result.Status = model.StatusSkipped
		result.Observed = model.Observed{Summary: "disabled in the runbook"}
		return result
	}

	runner, ok := e.registry.Runner(j.check.Kind, j.check.Check)
	if !ok {
		// Not a failing assertion: this build cannot answer the question. A
		// pass here would be a lie and a fail would be a false accusation.
		result.Status = model.StatusError
		result.Observed = model.Observed{
			Summary: fmt.Sprintf("no runner for %s/%s in this build", j.check.Kind, j.check.Check),
		}
		return result
	}

	timeout := model.DefaultTimeout
	if j.check.Timeout != nil {
		timeout = j.check.Timeout.Duration
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	outcome, err := runner.Run(runCtx, j.runbook.Meta, j.check)
	result.LatencyMS = time.Since(start).Milliseconds()

	if err != nil {
		result.Status = model.StatusError
		result.Observed = model.Observed{Summary: errorSummary(runCtx, timeout, err)}
		return result
	}

	result.Status = outcome.Status
	result.Observed = outcome.Observed
	// Results are minimal unless the check asked for more.
	if !j.check.Verbose {
		result.Observed.Detail = nil
	}
	return result
}

// errorSummary says why no answer was obtained. A deadline is reported as a
// deadline rather than as whatever the underlying client called it, because
// "timed out after 30s" tells the reader what to change.
func errorSummary(ctx context.Context, timeout time.Duration, err error) string {
	if ctx.Err() != nil && context.Cause(ctx) != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("timed out after %s: %s", timeout, err)
		}
		return fmt.Sprintf("cancelled: %s", err)
	}
	return err.Error()
}
