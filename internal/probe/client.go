package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// UserAgent identifies the probe to its control plane.
const UserAgent = "sonde-probe/1"

// maxResponseBytes caps what the probe will read from the control plane. The
// control plane is the less trusted side of this connection — it is outside the
// customer's network — so its answers are bounded like any other remote input.
const maxResponseBytes = 8 << 20

// Client talks to a control plane. Every call is outbound; the probe accepts no
// connections at all.
type Client struct {
	baseURL *url.URL
	http    *http.Client
}

// NewClient builds a client authenticated with a probe certificate.
//
// caPEM, when non-empty, is the only root the client will trust for the control
// plane. A self-hosted control plane with a private CA needs it; one with a
// publicly trusted certificate does not.
func NewClient(baseURL string, cert *tls.Certificate, caPEM []byte, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse control plane url %q: %w", baseURL, err)
	}
	if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
		return nil, fmt.Errorf("control plane url %q must be https", baseURL)
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if cert != nil {
		tlsConfig.Certificates = []tls.Certificate{*cert}
	}
	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("ca bundle contains no certificates")
		}
		tlsConfig.RootCAs = pool
	}

	return &Client{
		baseURL: parsed,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
	}, nil
}

// Enrol exchanges a one-time token for a probe identity and certificate.
func (c *Client) Enrol(ctx context.Context, req EnrolRequest) (EnrolResponse, error) {
	var out EnrolResponse
	err := c.do(ctx, http.MethodPost, "/v1/auth/enrol", req, &out)
	return out, err
}

// RenewCertificate issues a fresh certificate, authenticated by the one it
// replaces. A probe that lets its certificate lapse has to be enrolled again by
// a human, which is the point: a lapsed probe is not silently readmitted.
func (c *Client) RenewCertificate(ctx context.Context, csr []byte) (EnrolResponse, error) {
	var out EnrolResponse
	err := c.do(ctx, http.MethodPost, "/v1/auth/certificate", CertificateRequestBody{CSR: string(csr)}, &out)
	return out, err
}

// Jobs long-polls for work. The control plane holds the request open for up to
// wait before answering with an empty list, which keeps an idle probe at one
// request per wait rather than one per second.
func (c *Client) Jobs(ctx context.Context, wait time.Duration) (JobsResponse, error) {
	var out JobsResponse
	path := fmt.Sprintf("/v1/jobs?wait=%d", int(wait.Seconds()))
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// SubmitResults returns executed checks with their chain entries.
func (c *Client) SubmitResults(ctx context.Context, req ResultsRequest) (ResultsResponse, error) {
	var out ResultsResponse
	err := c.do(ctx, http.MethodPost, "/v1/results", req, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.String()+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var errorBody ErrorResponse
		if json.Unmarshal(payload, &errorBody) == nil && errorBody.Error != "" {
			return &StatusError{Status: resp.StatusCode, Message: errorBody.Error, Path: path}
		}
		return &StatusError{Status: resp.StatusCode, Message: resp.Status, Path: path}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// StatusError is a non-2xx answer from the control plane.
type StatusError struct {
	Status  int
	Message string
	Path    string
}

// Error implements error.
func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: %d %s", e.Path, e.Status, e.Message)
}

// Retryable reports whether waiting and trying again could help. A rejected
// certificate or a refused chain will not fix itself.
func (e *StatusError) Retryable() bool {
	switch {
	case e.Status == http.StatusTooManyRequests:
		return true
	case e.Status >= 500:
		return true
	default:
		return false
	}
}
