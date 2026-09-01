// Package http executes http checks: is the link the runbook tells you to open
// still alive?
//
// Requests are GET or HEAD and carry no body. The parser rejects any other
// method, so nothing here can be made to write.
package http

import (
	"context"
	"errors"
	"fmt"
	"net"
	nethttp "net/http"
	"strings"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/model"
)

// UserAgent identifies Sonde's reads in a target's access log, so that whoever
// finds them can tell what is making them and that they are harmless.
const UserAgent = "sonde/1 (+https://github.com/HamZeus95/sonde)"

// Runner executes http checks with a caller-supplied client, which is how the
// tests point it at an httptest server and how a probe behind a corporate proxy
// gets its transport.
type Runner struct {
	client *nethttp.Client
}

// New returns a runner. A nil client means nethttp.DefaultClient.
func New(client *nethttp.Client) *Runner {
	if client == nil {
		client = nethttp.DefaultClient
	}
	return &Runner{client: client}
}

// Register wires this runner's checks into a registry.
func (r *Runner) Register(reg *checks.Registry) error {
	return reg.Register(model.KindHTTP, "status_is", checks.RunnerFunc(r.statusIs))
}

func (r *Runner) statusIs(ctx context.Context, _ model.Meta, check model.Check) (checks.Outcome, error) {
	spec, ok := check.Spec.(*model.StatusIsSpec)
	if !ok {
		return checks.Outcome{}, fmt.Errorf("http/status_is: spec is %T", check.Spec)
	}

	method := strings.ToUpper(spec.Method)
	if method == "" {
		method = nethttp.MethodGet
	}
	req, err := nethttp.NewRequestWithContext(ctx, method, spec.URL, nil)
	if err != nil {
		return checks.Outcome{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := r.client.Do(req)
	if err != nil {
		if outcome, gone := hostIsGone(err, spec.URL); gone {
			return outcome, nil
		}
		return checks.Outcome{}, fmt.Errorf("%s %s: %w", method, spec.URL, err)
	}
	defer func() {
		// The body is never read, so closing it is the whole of the cleanup.
		_ = resp.Body.Close()
	}()

	want := spec.ExpectedStatus()
	detail := map[string]any{"status": resp.StatusCode, "url": spec.URL, "method": method}
	if resp.StatusCode != want {
		return checks.Fail("%s, want %d", resp.Status, want).WithDetail(detail), nil
	}
	return checks.Pass("%s", resp.Status).WithDetail(detail), nil
}

// hostIsGone separates the one transport failure that means the runbook is
// wrong from every other one, which means Sonde could not tell.
//
// A host that no longer exists in DNS is documentation rot: the dashboard was
// decommissioned and the link outlived it. A refused connection, a timeout or a
// TLS failure is not — the service may be down, the probe may be walled off, and
// reporting either as "your runbook is wrong" would make Sonde a bad monitoring
// system instead of a good documentation one.
func hostIsGone(err error, rawURL string) (checks.Outcome, bool) {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return checks.Fail("host %s does not resolve", dnsErr.Name).
			WithDetail(map[string]any{"url": rawURL, "host": dnsErr.Name}), true
	}
	return checks.Outcome{}, false
}
