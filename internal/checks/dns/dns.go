// Package dns executes dns checks: does the name a runbook tells you to fail
// over still resolve, and does it still point where the runbook says?
package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/model"
)

// Resolver is the part of net.Resolver this package uses. It is declared here,
// by the consumer, so that tests can substitute a stub without a live resolver
// and without a network in CI. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	LookupCNAME(ctx context.Context, host string) (string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupNS(ctx context.Context, name string) ([]*net.NS, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
}

// Runner executes dns checks.
type Runner struct {
	resolver Resolver
}

// New returns a runner. A nil resolver means the system resolver.
func New(resolver Resolver) *Runner {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &Runner{resolver: resolver}
}

// Register wires this runner's checks into a registry.
func (r *Runner) Register(reg *checks.Registry) error {
	return reg.Register(model.KindDNS, "record_exists", checks.RunnerFunc(r.recordExists))
}

func (r *Runner) recordExists(ctx context.Context, _ model.Meta, check model.Check) (checks.Outcome, error) {
	spec, ok := check.Spec.(*model.RecordExistsSpec)
	if !ok {
		return checks.Outcome{}, fmt.Errorf("dns/record_exists: spec is %T", check.Spec)
	}

	recordType := strings.ToUpper(spec.Type)
	values, err := r.lookup(ctx, recordType, spec.Name)
	if err != nil {
		// A name that does not exist is documentation rot: the record was
		// removed and the runbook still tells you to fail over to it. Any other
		// resolver failure means Sonde could not tell, and says so.
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return checks.Fail("%s does not resolve", spec.Name), nil
		}
		return checks.Outcome{}, fmt.Errorf("resolve %s %s: %w", recordType, spec.Name, err)
	}
	if len(values) == 0 {
		return checks.Fail("%s has no %s record", spec.Name, recordType), nil
	}

	sort.Strings(values)
	detail := map[string]any{"name": spec.Name, "type": recordType, "values": values}
	if spec.Expect == "" {
		return checks.Pass("%s", strings.Join(values, ", ")).WithDetail(detail), nil
	}

	want := normalise(spec.Expect)
	for _, value := range values {
		if normalise(value) == want {
			return checks.Pass("%s", value).WithDetail(detail), nil
		}
	}
	return checks.Fail("%s, want %s", strings.Join(values, ", "), spec.Expect).WithDetail(detail), nil
}

func (r *Runner) lookup(ctx context.Context, recordType, name string) ([]string, error) {
	switch recordType {
	case "A":
		return r.addresses(ctx, "ip4", name)
	case "AAAA":
		return r.addresses(ctx, "ip6", name)
	case "CNAME":
		target, err := r.resolver.LookupCNAME(ctx, name)
		if err != nil {
			return nil, err
		}
		// A resolver answers LookupCNAME with the name itself when there is no
		// CNAME in the chain, which is an absent record rather than a match.
		if normalise(target) == normalise(name) {
			return nil, nil
		}
		return []string{target}, nil
	case "MX":
		records, err := r.resolver.LookupMX(ctx, name)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(records))
		for _, mx := range records {
			out = append(out, mx.Host)
		}
		return out, nil
	case "NS":
		records, err := r.resolver.LookupNS(ctx, name)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(records))
		for _, ns := range records {
			out = append(out, ns.Host)
		}
		return out, nil
	case "TXT":
		return r.resolver.LookupTXT(ctx, name)
	case "SRV":
		// Both service and proto empty looks the name up directly, which is
		// what a runbook writes: _postgres._tcp.db.internal.
		_, records, err := r.resolver.LookupSRV(ctx, "", "", name)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(records))
		for _, srv := range records {
			out = append(out, fmt.Sprintf("%s:%d", srv.Target, srv.Port))
		}
		return out, nil
	default:
		// The parser rejects unknown types, so reaching this means the
		// catalogue and this switch disagree.
		return nil, fmt.Errorf("no lookup for record type %q", recordType)
	}
}

func (r *Runner) addresses(ctx context.Context, network, name string) ([]string, error) {
	addrs, err := r.resolver.LookupNetIP(ctx, network, name)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, addr.Unmap().String())
	}
	return out, nil
}

// normalise makes two spellings of the same name comparable: DNS is case
// insensitive and a trailing dot is the same name rooted.
func normalise(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}
