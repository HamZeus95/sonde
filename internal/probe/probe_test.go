package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HamZeus95/sonde/internal/checks"
	httpchecks "github.com/HamZeus95/sonde/internal/checks/http"
	"github.com/HamZeus95/sonde/internal/evidence"
	"github.com/HamZeus95/sonde/internal/model"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// liveTarget is something for the leased checks to actually check.
func liveTarget(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}

func httpRegistry(t *testing.T) *checks.Registry {
	t.Helper()
	reg := checks.NewRegistry()
	if err := httpchecks.New(nil).Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	return reg
}

func job(id int64, checkID, url string, expect int) Job {
	return Job{
		ID: id,
		Runbook: JobRunbook{
			ID: "dashboards", Path: "runbooks/dashboards.md",
			Environment: "prod-eu-1", Criticality: model.CriticalityHigh,
		},
		Check: model.Check{
			ID: checkID, Kind: model.KindHTTP, Check: "status_is", Enabled: true,
			Spec: &model.StatusIsSpec{URL: url, Expect: expect},
		},
	}
}

// newProbe enrols a probe against the stub control plane.
func newProbe(t *testing.T, cp *testControlPlane, dir string, mutate func(*Options)) *Probe {
	t.Helper()
	cp.issueToken("one-time-token")
	opts := Options{
		ControlPlane:   cp.URL(),
		StateDir:       dir,
		EnrolmentToken: "one-time-token",
		CAFile:         cp.caFile(t, t.TempDir()),
		Name:           "eu-1",
		Environment:    "prod-eu-1",
		Registry:       httpRegistry(t),
		PollWait:       time.Second,
		Logger:         quietLogger(),
	}
	if mutate != nil {
		mutate(&opts)
	}
	p, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("new probe: %v", err)
	}
	return p
}

// TestEnrolAndReport is the whole Phase 2 loop in one test: enrol with a
// one-time token, authenticate with the issued certificate, lease work, execute
// it, and return results the control plane can verify.
func TestEnrolAndReport(t *testing.T) {
	cp := newTestControlPlane(t)
	target := liveTarget(t)
	dir := t.TempDir()

	p := newProbe(t, cp, dir, nil)
	if p.ProbeID() != "probe-eu-1" {
		t.Fatalf("probe id = %q", p.ProbeID())
	}

	cp.enqueue(
		job(1, "dashboard-live", target.URL, 200),
		job(2, "dashboard-wrong", target.URL, 404),
	)
	worked, err := p.cycle(context.Background())
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if !worked {
		t.Fatal("the probe reported no work despite two jobs being queued")
	}

	stored := cp.probeState()
	if len(stored.results) != 2 {
		t.Fatalf("stored %d results", len(stored.results))
	}
	if stored.results[0].Status != model.StatusPass || stored.results[1].Status != model.StatusFail {
		t.Errorf("statuses = %q, %q", stored.results[0].Status, stored.results[1].Status)
	}
	// The control plane verified every signature and link before storing, so
	// reaching here at all is the assertion. Check the chain shape too.
	if stored.entries[0].PrevHash != "" {
		t.Error("the first entry must be a genesis")
	}
	if stored.entries[1].PrevHash != stored.entries[0].SelfHash {
		t.Error("the second entry does not follow the first")
	}
	if err := evidence.VerifyChain(stored.publicKey, stored.id, "", stored.results, stored.entries); err != nil {
		t.Fatalf("stored chain does not verify: %v", err)
	}
}

// TestChainContinuesAcrossRestarts covers the probe being killed and coming
// back: it resumes its own chain rather than starting a second one.
func TestChainContinuesAcrossRestarts(t *testing.T) {
	cp := newTestControlPlane(t)
	target := liveTarget(t)
	dir := t.TempDir()

	first := newProbe(t, cp, dir, nil)
	cp.enqueue(job(1, "a", target.URL, 200))
	if _, err := first.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	// A new process over the same state directory: no token, no second
	// enrolment, same identity.
	restarted, err := New(context.Background(), Options{
		ControlPlane: cp.URL(),
		StateDir:     dir,
		CAFile:       cp.caFile(t, t.TempDir()),
		Registry:     httpRegistry(t),
		PollWait:     time.Second,
		Logger:       quietLogger(),
	})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if restarted.ProbeID() != first.ProbeID() {
		t.Fatalf("restart changed identity: %q then %q", first.ProbeID(), restarted.ProbeID())
	}

	cp.enqueue(job(2, "b", target.URL, 200))
	if _, err := restarted.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	stored := cp.probeState()
	if len(stored.entries) != 2 {
		t.Fatalf("stored %d entries", len(stored.entries))
	}
	if stored.entries[1].PrevHash != stored.entries[0].SelfHash {
		t.Error("the restarted probe started a new chain instead of continuing its own")
	}
	if err := evidence.VerifyChain(stored.publicKey, stored.id, "", stored.results, stored.entries); err != nil {
		t.Fatalf("chain across the restart does not verify: %v", err)
	}
}

// TestRejectedWorkIsNotRecorded covers a submission the control plane refuses:
// the probe must not advance its chain, or the next batch would claim to follow
// something the control plane never stored.
func TestRejectedWorkIsNotRecorded(t *testing.T) {
	cp := newTestControlPlane(t)
	target := liveTarget(t)
	dir := t.TempDir()
	p := newProbe(t, cp, dir, nil)

	cp.mu.Lock()
	cp.rejectAll = true
	cp.mu.Unlock()

	cp.enqueue(job(1, "a", target.URL, 200))
	if _, err := p.cycle(context.Background()); err == nil {
		t.Fatal("a refused submission must be an error")
	}
	if p.state.LastHash != "" {
		t.Errorf("the chain advanced despite the submission failing: %q", p.state.LastHash)
	}

	cp.mu.Lock()
	cp.rejectAll = false
	cp.mu.Unlock()

	cp.enqueue(job(1, "a", target.URL, 200))
	if _, err := p.cycle(context.Background()); err != nil {
		t.Fatalf("cycle after recovery: %v", err)
	}
	if got := cp.probeState().entries[0].PrevHash; got != "" {
		t.Errorf("the retried batch should still be a genesis, got prev %q", got)
	}
}

// TestChainDivergenceStopsTheProbe covers a control plane restored from a
// backup, or one that rewrote a probe's history. Continuing would write a chain
// with a hole in it, so the probe refuses.
func TestChainDivergenceStopsTheProbe(t *testing.T) {
	cp := newTestControlPlane(t)
	target := liveTarget(t)
	dir := t.TempDir()
	p := newProbe(t, cp, dir, nil)

	cp.enqueue(job(1, "a", target.URL, 200))
	if _, err := p.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	// The control plane forgets everything after that submission.
	stored := cp.probeState()
	cp.mu.Lock()
	stored.chainTail = ""
	cp.mu.Unlock()

	cp.enqueue(job(2, "b", target.URL, 200))
	_, err := p.cycle(context.Background())
	if err == nil {
		t.Fatal("a control plane that lost this probe's history must stop it")
	}
	if got := err.Error(); got == "" {
		t.Error("the error should say what diverged")
	}

	// With the operator's explicit consent, the probe adopts the new position
	// and says so.
	p.opts.AllowChainReset = true
	cp.enqueue(job(3, "c", target.URL, 200))
	if _, err := p.cycle(context.Background()); err != nil {
		t.Fatalf("cycle with reset allowed: %v", err)
	}
	if p.state.LastHash == "" {
		t.Error("the probe should have continued from the adopted position")
	}
}

// TestEnrolmentTokenIsSingleUse pins the property that makes a leaked token
// harmless once a probe has started.
func TestEnrolmentTokenIsSingleUse(t *testing.T) {
	cp := newTestControlPlane(t)
	newProbe(t, cp, t.TempDir(), nil)

	_, err := New(context.Background(), Options{
		ControlPlane:   cp.URL(),
		StateDir:       t.TempDir(),
		EnrolmentToken: "one-time-token",
		CAFile:         cp.caFile(t, t.TempDir()),
		Name:           "impostor",
		Registry:       httpRegistry(t),
		Logger:         quietLogger(),
	})
	if err == nil {
		t.Fatal("the same enrolment token must not enrol a second probe")
	}
}

// TestUnenrolledProbeNeedsAToken keeps a probe from silently running with no
// identity at all.
func TestUnenrolledProbeNeedsAToken(t *testing.T) {
	cp := newTestControlPlane(t)
	_, err := New(context.Background(), Options{
		ControlPlane: cp.URL(),
		StateDir:     t.TempDir(),
		Registry:     httpRegistry(t),
		Logger:       quietLogger(),
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}

// TestPrivateKeyNeverLeaves reads what the probe actually sent at enrolment.
func TestPrivateKeyNeverLeaves(t *testing.T) {
	cp := newTestControlPlane(t)
	dir := t.TempDir()

	var captured []byte
	cp.server.Config.Handler = recordingHandler(cp.server.Config.Handler, &captured)

	newProbe(t, cp, dir, nil)

	key, err := NewStore(dir).LoadKey()
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	seed := key.Seed()
	if len(captured) == 0 {
		t.Fatal("nothing was captured")
	}
	if containsBytes(captured, seed) {
		t.Fatal("the private key seed appeared in an enrolment request")
	}
	var req EnrolRequest
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatalf("captured body is not an enrolment request: %v", err)
	}
	if req.PublicKey == "" || req.CSR == "" {
		t.Error("enrolment must carry the public key and a csr")
	}
}

func recordingHandler(next http.Handler, out *[]byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/enrol" {
			body, _ := io.ReadAll(r.Body)
			*out = body
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
	})
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}

// TestALostAnswerIsNotDivergence covers the failure this product cannot afford
// to treat as tampering: the control plane stores a batch and the answer never
// arrives — a dropped connection, a proxy timeout, a pod evicted between the
// write and the response.
//
// The control plane is then one batch ahead of what the probe recorded, which
// looks exactly like a rewritten history unless the probe remembers what it had
// in flight. Getting this wrong stops a probe until a human restarts it, and a
// stopped probe means checks silently not being verified.
func TestALostAnswerIsNotDivergence(t *testing.T) {
	cp := newTestControlPlane(t)
	target := liveTarget(t)
	dir := t.TempDir()
	p := newProbe(t, cp, dir, nil)

	cp.mu.Lock()
	cp.dropAnswers = true
	cp.mu.Unlock()

	cp.enqueue(job(1, "a", target.URL, 200))
	if _, err := p.cycle(context.Background()); err == nil {
		t.Fatal("a submission whose answer was lost must be an error at the time")
	}
	if p.state.LastHash != "" {
		t.Errorf("the chain advanced on an answer that never arrived: %q", p.state.LastHash)
	}
	if p.state.PendingHash == "" {
		t.Fatal("the probe did not record what it had in flight")
	}

	cp.mu.Lock()
	cp.dropAnswers = false
	cp.mu.Unlock()

	// The next poll finds the control plane one batch ahead. That is the batch
	// this probe signed, so it carries on rather than stopping.
	cp.enqueue(job(2, "b", target.URL, 200))
	if _, err := p.cycle(context.Background()); err != nil {
		t.Fatalf("a lost answer was treated as divergence: %v", err)
	}
	if p.state.PendingHash != "" {
		t.Errorf("pending hash was not cleared: %q", p.state.PendingHash)
	}

	stored := cp.probeState()
	if len(stored.entries) != 2 {
		t.Fatalf("stored %d entries, want 2", len(stored.entries))
	}
	if err := evidence.VerifyChain(stored.publicKey, stored.id, "", stored.results, stored.entries); err != nil {
		t.Fatalf("the chain across the lost answer does not verify: %v", err)
	}
}

// TestARewrittenHistoryIsStillDivergence is the other half: remembering what
// was in flight must not become a way to accept any tail the control plane
// offers.
func TestARewrittenHistoryIsStillDivergence(t *testing.T) {
	cp := newTestControlPlane(t)
	target := liveTarget(t)
	dir := t.TempDir()
	p := newProbe(t, cp, dir, nil)

	cp.mu.Lock()
	cp.dropAnswers = true
	cp.mu.Unlock()

	cp.enqueue(job(1, "a", target.URL, 200))
	if _, err := p.cycle(context.Background()); err == nil {
		t.Fatal("expected the dropped answer to be an error")
	}

	cp.mu.Lock()
	cp.dropAnswers = false
	cp.probes[p.ProbeID()].chainTail = "0000000000000000000000000000000000000000000000000000000000000000"
	cp.mu.Unlock()

	cp.enqueue(job(2, "b", target.URL, 200))
	_, err := p.cycle(context.Background())
	if !errors.Is(err, ErrChainDiverged) {
		t.Fatalf("a tail this probe never wrote must still stop it, got %v", err)
	}
}
