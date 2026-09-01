package http

import (
	"context"
	"errors"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/model"
)

func run(t *testing.T, client *nethttp.Client, spec *model.StatusIsSpec) (checks.Outcome, error) {
	t.Helper()
	reg := checks.NewRegistry()
	if err := New(client).Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	runner, ok := reg.Runner(model.KindHTTP, "status_is")
	if !ok {
		t.Fatal("http/status_is is not registered")
	}
	return runner.Run(context.Background(), model.Meta{}, model.Check{
		ID: "c", Kind: model.KindHTTP, Check: "status_is", Spec: spec, Enabled: true,
	})
}

func TestStatusIs(t *testing.T) {
	var gotMethod, gotAgent string
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		gotMethod, gotAgent = r.Method, r.UserAgent()
		switch r.URL.Path {
		case "/gone":
			w.WriteHeader(nethttp.StatusNotFound)
		case "/broken":
			w.WriteHeader(nethttp.StatusInternalServerError)
		default:
			w.WriteHeader(nethttp.StatusOK)
		}
	}))
	defer server.Close()

	t.Run("a live link passes", func(t *testing.T) {
		outcome, err := run(t, server.Client(), &model.StatusIsSpec{URL: server.URL + "/dashboard"})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if outcome.Status != model.StatusPass {
			t.Errorf("status = %q (%s)", outcome.Status, outcome.Observed.Summary)
		}
		if gotMethod != nethttp.MethodGet {
			t.Errorf("method = %q, want GET", gotMethod)
		}
		if gotAgent != UserAgent {
			t.Errorf("user agent = %q, want %q", gotAgent, UserAgent)
		}
	})

	t.Run("a dead link fails", func(t *testing.T) {
		outcome, err := run(t, server.Client(), &model.StatusIsSpec{URL: server.URL + "/gone"})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if outcome.Status != model.StatusFail {
			t.Errorf("status = %q, want fail", outcome.Status)
		}
	})

	t.Run("expect is honoured", func(t *testing.T) {
		outcome, err := run(t, server.Client(), &model.StatusIsSpec{URL: server.URL + "/gone", Expect: 404})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if outcome.Status != model.StatusPass {
			t.Errorf("status = %q, want pass: a runbook may document a 404", outcome.Status)
		}
	})

	t.Run("HEAD is sent when asked", func(t *testing.T) {
		if _, err := run(t, server.Client(), &model.StatusIsSpec{URL: server.URL, Method: "head"}); err != nil {
			t.Fatalf("run: %v", err)
		}
		if gotMethod != nethttp.MethodHead {
			t.Errorf("method = %q, want HEAD", gotMethod)
		}
	})
}

// TestUnreachableHostIsAnError covers the line the product depends on: a
// service that is down is not a document that is wrong.
func TestUnreachableHostIsAnError(t *testing.T) {
	server := httptest.NewServer(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {}))
	url := server.URL
	server.Close() // nothing is listening now

	outcome, err := run(t, nethttp.DefaultClient, &model.StatusIsSpec{URL: url})
	if err == nil {
		t.Fatalf("a refused connection must be an error, got status %q", outcome.Status)
	}
}

// TestMissingHostFails covers the other side of that line: a host that no
// longer exists in DNS is documentation rot.
func TestMissingHostFails(t *testing.T) {
	client := &nethttp.Client{Transport: roundTripperFunc(func(*nethttp.Request) (*nethttp.Response, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "grafana.gone.example", IsNotFound: true}
	})}
	outcome, err := run(t, client, &model.StatusIsSpec{URL: "https://grafana.gone.example/d/abc"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != model.StatusFail {
		t.Fatalf("status = %q, want fail", outcome.Status)
	}
}

// TestOtherTransportErrorsAreErrors guards against widening the DNS exception.
func TestOtherTransportErrorsAreErrors(t *testing.T) {
	client := &nethttp.Client{Transport: roundTripperFunc(func(*nethttp.Request) (*nethttp.Response, error) {
		return nil, errors.New("tls: handshake failure")
	})}
	if _, err := run(t, client, &model.StatusIsSpec{URL: "https://grafana.corp.example/d/abc"}); err == nil {
		t.Fatal("a TLS failure must be an error, not a failing assertion")
	}
}

type roundTripperFunc func(*nethttp.Request) (*nethttp.Response, error)

func (f roundTripperFunc) RoundTrip(r *nethttp.Request) (*nethttp.Response, error) { return f(r) }
