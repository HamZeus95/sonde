// Package probe is the daemon half of Sonde: it leases work from a control
// plane, executes checks read-only inside the customer's network, and returns
// signed results.
//
// The probe only ever dials out. It listens on no port, and no credential it
// holds — kubeconfig, cloud key, IdP secret — is ever sent anywhere. What
// leaves is a result: a status, a short summary, and a signature made with a
// key generated in the pod and never transmitted.
package probe

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/evidence"
	"github.com/HamZeus95/sonde/internal/model"
)

// Defaults for the daemon loop.
const (
	// DefaultPollWait is how long the control plane holds an empty long poll.
	DefaultPollWait = 30 * time.Second
	// DefaultRenewBefore is how long before expiry a certificate is renewed.
	// Generous, because a probe that misses the window needs a human.
	DefaultRenewBefore = 6 * time.Hour
	// minBackoff and maxBackoff bound the wait after a failed poll.
	minBackoff = 2 * time.Second
	maxBackoff = 2 * time.Minute
)

// ErrChainDiverged means the control plane's idea of this probe's history does
// not match the probe's own.
//
// That is either tampering or a control plane restored from a backup taken
// before results this probe has already submitted. Both need a human: the probe
// stops rather than writing a chain with a hole in it, and an operator who
// knows it was a restore can restart with AllowChainReset.
var ErrChainDiverged = errors.New("the control plane's chain tail does not match this probe's")

// Options configure the daemon.
type Options struct {
	// ControlPlane is the base URL of the control plane.
	ControlPlane string
	// StateDir holds the probe's key, certificate and chain position.
	StateDir string
	// EnrolmentToken is used once, on first start, and then never again.
	EnrolmentToken string
	// CAFile is the certificate authority to trust for the control plane
	// itself. Empty means the system roots, which is what a control plane
	// behind a publicly trusted certificate needs. A self-hosted one with a
	// private CA has to supply it here, because enrolment happens before the
	// control plane has told the probe anything.
	CAFile string
	// Name and Environment describe this probe at enrolment. Environment is
	// also the cluster name its checks resolve against.
	Name        string
	Environment string
	// Registry holds the runners this probe can execute.
	Registry *checks.Registry
	// PollWait is how long each long poll may block.
	PollWait time.Duration
	// Concurrency is how many leased checks run at once.
	Concurrency int
	// AllowChainReset adopts the control plane's tail when the two disagree,
	// after saying so. For an operator who knows why they differ.
	AllowChainReset bool
	// Version is reported at enrolment.
	Version string
	// Logger receives the daemon's operational log.
	Logger *slog.Logger
	// Clock is injectable for tests.
	Now func() time.Time
}

func (o *Options) applyDefaults() {
	if o.PollWait <= 0 {
		o.PollWait = DefaultPollWait
	}
	if o.Concurrency <= 0 {
		o.Concurrency = checks.DefaultConcurrency
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Version == "" {
		o.Version = "dev"
	}
}

// Probe is an enrolled daemon.
type Probe struct {
	opts   Options
	store  *Store
	engine *checks.Engine
	log    *slog.Logger

	key      ed25519.PrivateKey
	state    State
	client   *Client
	notAfter time.Time
}

// New prepares a probe, enrolling it if this state directory holds no identity.
//
// Enrolment happens once. After it, the token is worthless: it is single use
// and expires in fifteen minutes, so a token that leaks after a probe has
// started buys nothing.
func New(ctx context.Context, opts Options) (*Probe, error) {
	opts.applyDefaults()
	if opts.ControlPlane == "" {
		return nil, fmt.Errorf("control plane url is required")
	}
	if opts.Registry == nil {
		return nil, fmt.Errorf("registry is required")
	}

	p := &Probe{
		opts:   opts,
		store:  NewStore(opts.StateDir),
		engine: checks.NewEngine(opts.Registry, checks.WithConcurrency(opts.Concurrency)),
		log:    opts.Logger,
	}

	if !p.store.Enrolled() {
		if opts.EnrolmentToken == "" {
			return nil, fmt.Errorf("this probe is not enrolled and no enrolment token was given")
		}
		if err := p.enrol(ctx); err != nil {
			return nil, err
		}
	}
	if err := p.load(); err != nil {
		return nil, err
	}
	return p, nil
}

// ProbeID reports the identity the control plane assigned.
func (p *Probe) ProbeID() string { return p.state.ProbeID }

func (p *Probe) enrol(ctx context.Context) error {
	pub, priv, err := evidence.GenerateKey()
	if err != nil {
		return err
	}
	publicPEM, err := PublicKeyPEM(pub)
	if err != nil {
		return err
	}
	name := p.opts.Name
	if name == "" {
		name = "probe"
	}
	csr, err := CertificateRequest(priv, name)
	if err != nil {
		return err
	}

	caPEM, err := p.caBundle()
	if err != nil {
		return err
	}
	// The enrolment call is the only one made without a client certificate:
	// the token is what authenticates it.
	client, err := NewClient(p.opts.ControlPlane, nil, caPEM, 30*time.Second)
	if err != nil {
		return err
	}
	answer, err := client.Enrol(ctx, EnrolRequest{
		Token:       p.opts.EnrolmentToken,
		Name:        name,
		Environment: p.opts.Environment,
		PublicKey:   string(publicPEM),
		CSR:         string(csr),
		Version:     p.opts.Version,
	})
	if err != nil {
		return fmt.Errorf("enrol: %w", err)
	}

	// The key is written before anything else: an identity whose private key
	// was lost between the response and the disk is an identity nobody can use.
	if err := p.store.SaveKey(priv); err != nil {
		return err
	}
	if err := p.store.SaveCertificate([]byte(answer.Certificate), []byte(answer.CABundle)); err != nil {
		return err
	}
	if err := p.store.SaveState(State{
		ProbeID:     answer.ProbeID,
		Name:        name,
		Environment: p.opts.Environment,
	}); err != nil {
		return err
	}
	p.log.Info("enrolled", "probe_id", answer.ProbeID, "certificate_expires", answer.NotAfter)
	return nil
}

// load reads the identity from disk and builds an authenticated client.
func (p *Probe) load() error {
	state, err := p.store.LoadState()
	if err != nil {
		return err
	}
	key, err := p.store.LoadKey()
	if err != nil {
		return err
	}
	certPEM, caPEM, err := p.store.LoadCertificate()
	if err != nil {
		return err
	}
	if len(caPEM) == 0 {
		if caPEM, err = p.caBundle(); err != nil {
			return err
		}
	}
	keyPEM, err := privateKeyPEM(key)
	if err != nil {
		return err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("load probe certificate: %w", err)
	}
	notAfter, err := certificateExpiry(certPEM)
	if err != nil {
		return err
	}
	// The client's timeout has to outlast a long poll, or every idle cycle
	// would look like a network failure.
	client, err := NewClient(p.opts.ControlPlane, &cert, caPEM, p.opts.PollWait+30*time.Second)
	if err != nil {
		return err
	}

	p.state, p.key, p.client, p.notAfter = state, key, client, notAfter
	return nil
}

// Run polls for work until the context is cancelled.
func (p *Probe) Run(ctx context.Context) error {
	p.log.Info("probe started",
		"probe_id", p.state.ProbeID,
		"control_plane", p.opts.ControlPlane,
		"environment", p.state.Environment,
		"checks", len(p.opts.Registry.Supported()))

	backoff := minBackoff
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := p.renewIfDue(ctx); err != nil {
			p.log.Error("certificate renewal failed", "error", err)
		}

		// Whether the cycle found work is not interesting here: an empty poll
		// already blocked for the poll interval, so going straight back round
		// is not a busy loop. The caller of cycle that does care is the test.
		_, err := p.cycle(ctx)
		switch {
		case errors.Is(err, ErrChainDiverged):
			return err
		case errors.Is(err, context.Canceled):
			return nil
		case err != nil:
			p.log.Error("poll failed", "error", err, "retry_in", backoff)
			if !sleep(ctx, backoff) {
				return nil
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = minBackoff
	}
}

// cycle leases work, executes it, and submits the results.
func (p *Probe) cycle(ctx context.Context) (worked bool, err error) {
	lease, err := p.client.Jobs(ctx, p.opts.PollWait)
	if err != nil {
		return false, err
	}
	if len(lease.Jobs) == 0 {
		return false, nil
	}
	if err := p.reconcileChain(lease.ChainTail); err != nil {
		return false, err
	}

	p.log.Info("leased", "jobs", len(lease.Jobs))
	results := p.execute(ctx, lease.Jobs)

	plain := make([]model.Result, len(results))
	for i, r := range results {
		plain[i] = r.Result
	}
	entries, tail, err := evidence.Chain(p.key, p.state.ProbeID, p.state.LastHash, plain)
	if err != nil {
		return true, err
	}
	for i := range results {
		results[i].Entry = entries[i]
	}

	answer, err := p.client.SubmitResults(ctx, ResultsRequest{
		ChainTail: p.state.LastHash,
		Results:   results,
	})
	if err != nil {
		return true, fmt.Errorf("submit results: %w", err)
	}
	if answer.ChainTail != tail {
		return true, fmt.Errorf("%w: it stored %q, this probe wrote %q", ErrChainDiverged, answer.ChainTail, tail)
	}

	p.state.LastHash = tail
	if err := p.store.SaveState(p.state); err != nil {
		return true, err
	}
	p.log.Info("submitted", "results", answer.Accepted, "chain_tail", short(tail))
	return true, nil
}

// execute runs the leased checks, bounded, preserving lease order so that the
// chain a probe writes is the order it was given work in.
func (p *Probe) execute(ctx context.Context, jobs []Job) []SignedResult {
	results := make([]SignedResult, len(jobs))
	queue := make(chan int)
	var wg sync.WaitGroup
	for range min(p.opts.Concurrency, max(len(jobs), 1)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				j := jobs[i]
				runbook := model.Runbook{Path: j.Runbook.Path, Meta: j.Runbook.Meta()}
				results[i] = SignedResult{
					JobID:  j.ID,
					Result: p.engine.Execute(ctx, runbook, j.Check),
				}
			}
		}()
	}
	for i := range jobs {
		queue <- i
	}
	close(queue)
	wg.Wait()
	return results
}

// reconcileChain compares the control plane's record of this probe's history
// with the probe's own.
func (p *Probe) reconcileChain(remote string) error {
	if remote == p.state.LastHash {
		return nil
	}
	if p.opts.AllowChainReset {
		p.log.Warn("adopting the control plane's chain tail",
			"local", short(p.state.LastHash), "remote", short(remote),
			"note", "evidence before this point is no longer a single unbroken chain")
		p.state.LastHash = remote
		return p.store.SaveState(p.state)
	}
	return fmt.Errorf("%w: it holds %q, this probe holds %q; if the control plane was restored from a backup, restart with --allow-chain-reset",
		ErrChainDiverged, short(remote), short(p.state.LastHash))
}

// caBundle reads the operator-supplied CA, if there is one.
func (p *Probe) caBundle() ([]byte, error) {
	if p.opts.CAFile == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(p.opts.CAFile) //nolint:gosec // the path the operator named
	if err != nil {
		return nil, fmt.Errorf("read ca file: %w", err)
	}
	return pem, nil
}

func (p *Probe) renewIfDue(ctx context.Context) error {
	if p.notAfter.IsZero() || p.opts.Now().Add(DefaultRenewBefore).Before(p.notAfter) {
		return nil
	}
	csr, err := CertificateRequest(p.key, p.state.Name)
	if err != nil {
		return err
	}
	answer, err := p.client.RenewCertificate(ctx, csr)
	if err != nil {
		return err
	}
	if err := p.store.SaveCertificate([]byte(answer.Certificate), []byte(answer.CABundle)); err != nil {
		return err
	}
	p.log.Info("certificate renewed", "expires", answer.NotAfter)
	return p.load()
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// short trims a hash for a log line, where the first bytes are enough to tell
// two chains apart.
func short(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
