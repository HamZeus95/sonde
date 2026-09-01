package dns

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/model"
)

// stubResolver answers from a table. Tests must never touch a real resolver:
// CI has no DNS worth asserting on, and a check that depends on the internet is
// a flaky check.
type stubResolver struct {
	addrs map[string][]string
	cname map[string]string
	txt   map[string][]string
	err   error
}

func (s stubResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if s.err != nil {
		return nil, s.err
	}
	values, ok := s.addrs[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	out := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		addr, err := netip.ParseAddr(value)
		if err != nil {
			return nil, err
		}
		out = append(out, addr)
	}
	return out, nil
}

func (s stubResolver) LookupCNAME(_ context.Context, host string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	target, ok := s.cname[host]
	if !ok {
		return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return target, nil
}

func (s stubResolver) LookupMX(context.Context, string) ([]*net.MX, error) { return nil, s.err }
func (s stubResolver) LookupNS(context.Context, string) ([]*net.NS, error) { return nil, s.err }
func (s stubResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.txt[name], nil
}
func (s stubResolver) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "", nil, s.err
}

func run(t *testing.T, resolver Resolver, spec *model.RecordExistsSpec) (checks.Outcome, error) {
	t.Helper()
	reg := checks.NewRegistry()
	if err := New(resolver).Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	runner, _ := reg.Runner(model.KindDNS, "record_exists")
	return runner.Run(context.Background(), model.Meta{}, model.Check{
		ID: "c", Kind: model.KindDNS, Check: "record_exists", Spec: spec, Enabled: true,
	})
}

func TestRecordExists(t *testing.T) {
	resolver := stubResolver{
		addrs: map[string][]string{"db.internal": {"10.0.0.5"}},
		cname: map[string]string{
			"db-primary.internal": "db-eu-1a.internal.",
			"db-moved.internal":   "db-eu-1b.internal.",
			"no-cname.internal":   "no-cname.internal.",
		},
		txt: map[string][]string{"corp.example": {"v=spf1 -all"}},
	}

	tests := []struct {
		name string
		spec *model.RecordExistsSpec
		want model.Status
	}{
		{"existing A record", &model.RecordExistsSpec{Name: "db.internal", Type: "A"}, model.StatusPass},
		{"missing name", &model.RecordExistsSpec{Name: "gone.internal", Type: "A"}, model.StatusFail},
		{"cname matches", &model.RecordExistsSpec{Name: "db-primary.internal", Type: "CNAME", Expect: "db-eu-1a.internal"}, model.StatusPass},
		{"cname trailing dot is the same name", &model.RecordExistsSpec{Name: "db-primary.internal", Type: "CNAME", Expect: "DB-EU-1A.internal."}, model.StatusPass},
		{"cname repointed", &model.RecordExistsSpec{Name: "db-moved.internal", Type: "CNAME", Expect: "db-eu-1a.internal"}, model.StatusFail},
		{"no cname in the chain", &model.RecordExistsSpec{Name: "no-cname.internal", Type: "CNAME"}, model.StatusFail},
		{"txt", &model.RecordExistsSpec{Name: "corp.example", Type: "TXT", Expect: "v=spf1 -all"}, model.StatusPass},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, err := run(t, resolver, tt.spec)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if outcome.Status != tt.want {
				t.Errorf("status = %q, want %q (%s)", outcome.Status, tt.want, outcome.Observed.Summary)
			}
		})
	}
}

// TestResolverFailureIsAnError separates a broken resolver from a removed
// record. Only the second one means the runbook is wrong.
func TestResolverFailureIsAnError(t *testing.T) {
	broken := stubResolver{err: &net.DNSError{Err: "server misbehaving", Name: "db.internal", IsTemporary: true}}
	if _, err := run(t, broken, &model.RecordExistsSpec{Name: "db.internal", Type: "A"}); err == nil {
		t.Fatal("a failing resolver must be an error, not a failing assertion")
	}
}
