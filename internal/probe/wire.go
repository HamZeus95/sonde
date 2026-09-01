package probe

import (
	"time"

	"github.com/HamZeus95/sonde/internal/model"
)

// The wire types between a probe and a control plane.
//
// sonde-cloud's packages/contracts mirrors these as zod schemas. When one side
// changes, both change in the same session and model.SchemaVersion is bumped;
// there is no codegen yet, so the discipline is the only thing keeping them in
// step.
//
// The probe only ever makes outbound requests. There is no route here that the
// control plane calls: it cannot reach into a customer's network, and nothing
// in this file gives it a way to start.

// EnrolRequest exchanges a one-time enrolment token for a client certificate.
//
// The probe sends its public key and a CSR; it never sends the private key,
// which does not leave the pod.
type EnrolRequest struct {
	Token       string `json:"token"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	PublicKey   string `json:"public_key"`
	CSR         string `json:"csr"`
	// Version is the probe's build, recorded so an operator can see which
	// probes are behind without the probe reporting anything else about the
	// machine it runs on.
	Version string `json:"version"`
}

// EnrolResponse carries the identity the control plane assigned.
type EnrolResponse struct {
	ProbeID     string    `json:"probe_id"`
	Certificate string    `json:"certificate"`
	CABundle    string    `json:"ca_bundle"`
	NotAfter    time.Time `json:"not_after"`
}

// CertificateRequestBody renews a client certificate before it expires. It is
// authenticated by the certificate being replaced, so a probe that lets its
// certificate lapse must be re-enrolled by a human.
type CertificateRequestBody struct {
	CSR string `json:"csr"`
}

// JobsResponse is the answer to a long poll.
type JobsResponse struct {
	ProbeID string `json:"probe_id"`
	// ChainTail is the self_hash of the last entry the control plane holds for
	// this probe, empty if it holds none. The probe compares it with its own
	// and refuses to submit if they disagree.
	ChainTail string `json:"chain_tail"`
	Jobs      []Job  `json:"jobs"`
}

// Job is one check to execute, with the runbook context the check needs to
// resolve its cluster.
type Job struct {
	ID      int64       `json:"id"`
	Runbook JobRunbook  `json:"runbook"`
	Check   model.Check `json:"check"`
}

// JobRunbook is the part of a runbook a check needs at execution time. The
// document itself is not sent: the probe executes assertions, it does not read
// prose.
type JobRunbook struct {
	ID          string            `json:"id"`
	Path        string            `json:"path"`
	Owner       string            `json:"owner,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Criticality model.Criticality `json:"criticality,omitempty"`
}

// Meta rebuilds the runbook metadata a check runner expects.
func (r JobRunbook) Meta() model.Meta {
	return model.Meta{
		Version:     model.ContractVersion,
		ID:          r.ID,
		Owner:       r.Owner,
		Environment: r.Environment,
		Criticality: r.Criticality,
	}
}

// ResultsRequest submits executed checks.
type ResultsRequest struct {
	// ChainTail is the tail the probe started this batch from. The control
	// plane rejects the batch if it does not match what it holds, rather than
	// accepting a chain with a hole in it.
	ChainTail string         `json:"chain_tail"`
	Results   []SignedResult `json:"results"`
}

// SignedResult is one result with its chain entry.
type SignedResult struct {
	JobID  int64        `json:"job_id"`
	Result model.Result `json:"result"`
	Entry  Entry        `json:"entry"`
}

// ResultsResponse reports what was stored.
type ResultsResponse struct {
	Accepted  int    `json:"accepted"`
	ChainTail string `json:"chain_tail"`
}

// ErrorResponse is the body of every non-2xx answer.
type ErrorResponse struct {
	Error string `json:"error"`
}
