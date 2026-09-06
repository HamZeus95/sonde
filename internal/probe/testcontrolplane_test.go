package probe

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/HamZeus95/sonde/internal/evidence"
	"github.com/HamZeus95/sonde/internal/model"
)

// testControlPlane is a control plane in miniature: a small CA, the four routes
// a probe calls, and enough bookkeeping to verify what arrives.
//
// It exists so the wire protocol and the chain can be exercised end to end
// before the real control plane is written in TypeScript, and so that a change
// to either side of the protocol fails a test here first.
type testControlPlane struct {
	t      *testing.T
	server *httptest.Server

	caCert *x509.Certificate
	caKey  ed25519.PrivateKey
	caPEM  []byte

	mu        sync.Mutex
	tokens    map[string]bool
	probes    map[string]*registeredProbe
	queue     []Job
	certTTL   time.Duration
	rejectAll bool
	// dropAnswers stores a submission and then aborts the connection, which is
	// what a proxy timeout or an evicted pod looks like from the probe: the
	// work landed and the acknowledgement did not.
	dropAnswers bool
}

type registeredProbe struct {
	id        string
	publicKey ed25519.PublicKey
	chainTail string
	results   []model.Result
	entries   []Entry
	// completed records which job ids came back, so a re-lease that duplicated
	// work would be visible.
	completed []int64
}

func newTestControlPlane(t *testing.T) *testControlPlane {
	t.Helper()
	cp := &testControlPlane{
		t:       t,
		tokens:  map[string]bool{},
		probes:  map[string]*registeredProbe{},
		certTTL: 24 * time.Hour,
	}
	cp.newCA()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/enrol", cp.handleEnrol)
	mux.HandleFunc("POST /v1/auth/certificate", cp.handleCertificate)
	mux.HandleFunc("GET /v1/jobs", cp.handleJobs)
	mux.HandleFunc("POST /v1/results", cp.handleResults)

	server := httptest.NewUnstartedServer(mux)
	pool := x509.NewCertPool()
	pool.AddCert(cp.caCert)
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{cp.serverCertificate()},
		// Enrolment arrives without a client certificate; everything else must
		// present one, which the handlers enforce.
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  pool,
		MinVersion: tls.VersionTLS12,
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	cp.server = server
	return cp
}

func (cp *testControlPlane) URL() string { return cp.server.URL }

// caFile writes the CA where a probe can be pointed at it.
func (cp *testControlPlane) caFile(t *testing.T, dir string) string {
	t.Helper()
	path := dir + "/ca.pem"
	if err := writeFile(path, cp.caPEM); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	return path
}

func (cp *testControlPlane) newCA() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		cp.t.Fatalf("generate ca key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "sonde test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		cp.t.Fatalf("create ca: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		cp.t.Fatalf("parse ca: %v", err)
	}
	cp.caCert, cp.caKey = cert, priv
	cp.caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func (cp *testControlPlane) serverCertificate() tls.Certificate {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		cp.t.Fatalf("generate server key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, cp.caCert, pub, cp.caKey)
	if err != nil {
		cp.t.Fatalf("create server cert: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		cp.t.Fatalf("encode server key: %v", err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		cp.t.Fatalf("build server keypair: %v", err)
	}
	return cert
}

// issue signs a client certificate for a CSR, the way the real control plane
// does at enrolment and renewal.
func (cp *testControlPlane) issue(csrPEM string, probeID string) ([]byte, time.Time) {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		cp.t.Fatal("csr is not PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		cp.t.Fatalf("parse csr: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		cp.t.Fatalf("csr signature: %v", err)
	}
	notAfter := time.Now().Add(cp.certTTL)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		// The certificate carries the identity the control plane assigned, not
		// the one the probe asked for.
		Subject:     pkix.Name{CommonName: probeID},
		NotBefore:   time.Now().Add(-time.Minute),
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, cp.caCert, csr.PublicKey, cp.caKey)
	if err != nil {
		cp.t.Fatalf("issue certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), notAfter
}

func (cp *testControlPlane) issueToken(token string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.tokens[token] = true
}

func (cp *testControlPlane) enqueue(jobs ...Job) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.queue = append(cp.queue, jobs...)
}

func (cp *testControlPlane) probeState() *registeredProbe {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	for _, p := range cp.probes {
		return p
	}
	return nil
}

func (cp *testControlPlane) handleEnrol(w http.ResponseWriter, r *http.Request) {
	var req EnrolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed body")
		return
	}

	cp.mu.Lock()
	defer cp.mu.Unlock()
	if !cp.tokens[req.Token] {
		// Single use and short lived: a token that has been spent, or was
		// never issued, buys nothing.
		writeError(w, http.StatusUnauthorized, "unknown or spent enrolment token")
		return
	}
	delete(cp.tokens, req.Token)

	block, _ := pem.Decode([]byte(req.PublicKey))
	if block == nil {
		writeError(w, http.StatusBadRequest, "public key is not PEM")
		return
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "public key is unreadable")
		return
	}
	pub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		writeError(w, http.StatusBadRequest, "public key is not ed25519")
		return
	}

	id := "probe-" + req.Name
	cp.probes[id] = &registeredProbe{id: id, publicKey: pub}
	certPEM, notAfter := cp.issue(req.CSR, id)
	writeJSON(w, http.StatusOK, EnrolResponse{
		ProbeID:     id,
		Certificate: string(certPEM),
		CABundle:    string(cp.caPEM),
		NotAfter:    notAfter,
	})
}

// probeFor identifies the caller by its client certificate. There is no bearer
// token anywhere in this protocol after enrolment.
func (cp *testControlPlane) probeFor(r *http.Request) (*registeredProbe, bool) {
	if len(r.TLS.PeerCertificates) == 0 {
		return nil, false
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	p, ok := cp.probes[r.TLS.PeerCertificates[0].Subject.CommonName]
	return p, ok
}

func (cp *testControlPlane) handleCertificate(w http.ResponseWriter, r *http.Request) {
	p, ok := cp.probeFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "client certificate required")
		return
	}
	var req CertificateRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed body")
		return
	}
	certPEM, notAfter := cp.issue(req.CSR, p.id)
	writeJSON(w, http.StatusOK, EnrolResponse{
		ProbeID:     p.id,
		Certificate: string(certPEM),
		CABundle:    string(cp.caPEM),
		NotAfter:    notAfter,
	})
}

func (cp *testControlPlane) handleJobs(w http.ResponseWriter, r *http.Request) {
	p, ok := cp.probeFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "client certificate required")
		return
	}
	cp.mu.Lock()
	jobs := cp.queue
	cp.queue = nil
	tail := p.chainTail
	cp.mu.Unlock()

	writeJSON(w, http.StatusOK, JobsResponse{ProbeID: p.id, ChainTail: tail, Jobs: jobs})
}

func (cp *testControlPlane) handleResults(w http.ResponseWriter, r *http.Request) {
	p, ok := cp.probeFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "client certificate required")
		return
	}
	var req ResultsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed body")
		return
	}

	cp.mu.Lock()
	defer cp.mu.Unlock()
	if cp.rejectAll {
		writeError(w, http.StatusServiceUnavailable, "storage is down")
		return
	}
	if req.ChainTail != p.chainTail {
		writeError(w, http.StatusConflict, "chain tail does not match")
		return
	}

	// This is the verification the real control plane performs on every
	// submission: every signature valid, every link intact.
	results := make([]model.Result, len(req.Results))
	entries := make([]Entry, len(req.Results))
	for i, signed := range req.Results {
		results[i], entries[i] = signed.Result, signed.Entry
	}
	if err := evidence.VerifyChain(p.publicKey, p.id, p.chainTail, results, entries); err != nil {
		writeError(w, http.StatusBadRequest, "chain does not verify: "+err.Error())
		return
	}

	p.results = append(p.results, results...)
	p.entries = append(p.entries, entries...)
	for _, signed := range req.Results {
		p.completed = append(p.completed, signed.JobID)
	}
	if len(entries) > 0 {
		p.chainTail = entries[len(entries)-1].SelfHash
	}
	if cp.dropAnswers {
		// Stored, and the caller never hears so.
		panic(http.ErrAbortHandler)
	}
	writeJSON(w, http.StatusOK, ResultsResponse{Accepted: len(req.Results), ChainTail: p.chainTail})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, ErrorResponse{Error: message})
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
