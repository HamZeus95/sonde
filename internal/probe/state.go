package probe

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State is everything a probe keeps between restarts.
//
// The private key is generated inside the pod and written here; it never leaves
// the machine, is never sent to the control plane, and is not recoverable from
// anything the control plane stores. A probe that loses this directory
// re-enrols as a new probe rather than resuming someone else's chain.
type State struct {
	// ProbeID is assigned by the control plane at enrolment.
	ProbeID string `json:"probe_id"`
	// Name and Environment are what the operator called this probe.
	Name        string `json:"name"`
	Environment string `json:"environment"`
	// LastHash is the SelfHash of the last entry this probe submitted. It is
	// kept locally as well as on the server so that a server which has
	// forgotten, or rewritten, a probe's history is detectable rather than
	// silently accommodated.
	LastHash string `json:"last_hash"`
	// PendingHash is the tail of a batch that was sent and not yet
	// acknowledged. A submission can be stored and its answer lost on the way
	// back — a dropped connection, a proxy timeout, a pod evicted between the
	// write and the response — after which the control plane is one batch
	// ahead of LastHash through no fault of anyone's. Recording what was in
	// flight is what lets the next poll tell that case apart from a control
	// plane that rewrote this probe's history, which is the one that needs a
	// human.
	PendingHash string `json:"pending_hash,omitempty"`
}

// stateDir file names. The key is separate from the JSON so its permissions can
// be reasoned about on their own.
const (
	stateFile   = "probe.json"
	keyFile     = "key.pem"
	certFile    = "cert.pem"
	caFile      = "ca.pem"
	keyFileMode = 0o600
	dirMode     = 0o700
)

// Store persists probe state in a directory.
type Store struct {
	dir string
}

// NewStore returns a store rooted at dir.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir reports the directory this store uses.
func (s *Store) Dir() string { return s.dir }

// Enrolled reports whether this directory already holds an identity.
func (s *Store) Enrolled() bool {
	if _, err := os.Stat(filepath.Join(s.dir, stateFile)); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(s.dir, keyFile))
	return err == nil
}

// LoadState reads the probe's state.
func (s *Store) LoadState() (State, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, stateFile)) //nolint:gosec // a path this process owns
	if err != nil {
		return State{}, fmt.Errorf("read probe state: %w", err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("parse probe state: %w", err)
	}
	return state, nil
}

// SaveState writes the probe's state, replacing it atomically so that a crash
// mid-write cannot leave a probe with a truncated chain position.
func (s *Store) SaveState(state State) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode probe state: %w", err)
	}
	return s.writeAtomic(stateFile, append(raw, '\n'), 0o600)
}

// LoadKey reads the probe's private key.
func (s *Store) LoadKey() (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, keyFile)) //nolint:gosec // a path this process owns
	if err != nil {
		return nil, fmt.Errorf("read probe key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("probe key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse probe key: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("probe key is %T, want ed25519", parsed)
	}
	return key, nil
}

// SaveKey writes the probe's private key with owner-only permissions.
func (s *Store) SaveKey(key ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode probe key: %w", err)
	}
	return s.writeAtomic(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), keyFileMode)
}

// SaveCertificate stores the client certificate the control plane issued and
// the CA bundle used to verify the control plane itself.
func (s *Store) SaveCertificate(certPEM, caPEM []byte) error {
	if err := s.writeAtomic(certFile, certPEM, 0o600); err != nil {
		return err
	}
	if len(caPEM) == 0 {
		return nil
	}
	return s.writeAtomic(caFile, caPEM, 0o600)
}

// LoadCertificate reads the client certificate and CA bundle. A missing CA
// bundle is not an error: a control plane with a publicly trusted certificate
// needs none.
func (s *Store) LoadCertificate() (certPEM, caPEM []byte, err error) {
	certPEM, err = os.ReadFile(filepath.Join(s.dir, certFile)) //nolint:gosec // a path this process owns
	if err != nil {
		return nil, nil, fmt.Errorf("read probe certificate: %w", err)
	}
	caPEM, err = os.ReadFile(filepath.Join(s.dir, caFile)) //nolint:gosec // a path this process owns
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("read ca bundle: %w", err)
	}
	return certPEM, caPEM, nil
}

func (s *Store) writeAtomic(name string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(s.dir, dirMode); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	path := filepath.Join(s.dir, name)
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", temp, err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// CertificateRequest builds a PEM CSR for the probe's key.
//
// The control plane decides what the certificate says; the probe only proves it
// holds the key. The common name it asks for is advisory, and the issued
// certificate is what actually identifies the probe.
func CertificateRequest(key ed25519.PrivateKey, name string) ([]byte, error) {
	template := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: name},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		return nil, fmt.Errorf("create csr: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// PublicKeyPEM encodes a public key for transport at enrolment, so that the
// control plane can verify signatures independently of the certificate.
func PublicKeyPEM(pub ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("encode public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// privateKeyPEM re-encodes a loaded key so it can be paired with a certificate
// for TLS. The bytes never leave this process.
func privateKeyPEM(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode probe key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// certificateExpiry reports when the leaf certificate stops being valid, which
// is what the renewal loop schedules against.
func certificateExpiry(certPEM []byte) (time.Time, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return time.Time{}, fmt.Errorf("probe certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse probe certificate: %w", err)
	}
	return cert.NotAfter, nil
}
