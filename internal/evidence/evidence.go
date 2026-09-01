// Package evidence is the signed, hash-chained record of what Sonde checked.
//
// It is deliberately small and dependency-free beyond the model types, because
// two very different things need it: the probe, which produces the chain inside
// a customer's network, and the verifier, which an auditor runs against an
// exported bundle on a laptop with no access to anything.
//
// What the chain proves is narrow and worth stating plainly: that every result
// in it was signed by the key a probe held, and that none was altered or
// removed. It does not prove that the probe was pointed at the right cluster,
// or that a control plane did not omit an entire probe. The first is the
// customer's own configuration; the second is why a bundle lists every probe it
// knows about and why a probe keeps its own tail.
package evidence

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/HamZeus95/sonde/internal/model"
)

// EvidenceVersion prefixes every hash preimage. A change to the preimage
// format changes this string, so an old and a new chain can never be confused
// for one another.
const EvidenceVersion = "sonde-evidence-v1"

// GenerateKey creates a signing keypair.
//
// The private half is generated where it will be used and never travels: a
// control plane that held it could produce results indistinguishable from a
// probe's, and then none of this would prove anything.
func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	return pub, priv, nil
}

// Entry is one link in a probe's hash chain: a result, the hash of the result
// before it, and a signature over the two.
//
// The chain is per probe. A verifier walking it can tell that no result was
// altered and that none was dropped, which is what turns a dashboard into an
// artifact an auditor will accept.
type Entry struct {
	// PrevHash is the SelfHash of this probe's previous entry, empty for the
	// first entry a probe ever submits.
	PrevHash string `json:"prev_hash"`
	// SelfHash is the hex SHA-256 of the preimage below.
	SelfHash string `json:"self_hash"`
	// Signature is base64 Ed25519 over the 32 raw bytes of SelfHash.
	Signature string `json:"signature"`
}

// Preimage renders the exact bytes that are hashed for a result.
//
// It is a line format rather than JSON on purpose. The control plane verifies
// this hash in TypeScript, and two languages agreeing on canonical JSON — key
// order, number formatting, unicode escaping — is a bug waiting to happen.
// Lines with fixed order and fixed fields cannot drift.
//
// The summary is included as its own digest so that arbitrary text, including
// text with newlines, can never break the format. Observed detail is
// deliberately outside the chain: it exists only when a check asked for
// verbose, and what the evidence attests is that a named check had a given
// status at a given time, attested by a given probe, in an unbroken sequence.
func Preimage(probeID string, prevHash string, r model.Result) []byte {
	summary := sha256.Sum256([]byte(r.Observed.Summary))
	var b strings.Builder
	b.WriteString(EvidenceVersion)
	b.WriteByte('\n')
	writeField(&b, "prev", prevHash)
	writeField(&b, "probe", probeID)
	writeField(&b, "runbook", r.RunbookID)
	writeField(&b, "check", r.CheckID)
	writeField(&b, "status", string(r.Status))
	writeField(&b, "ran_at", r.RanAt.UTC().Format(time.RFC3339))
	writeField(&b, "summary", hex.EncodeToString(summary[:]))
	return []byte(b.String())
}

func writeField(b *strings.Builder, name, value string) {
	b.WriteString(name)
	b.WriteByte(':')
	b.WriteString(value)
	b.WriteByte('\n')
}

// Hash returns the hex SHA-256 of a result's preimage.
func Hash(probeID string, prevHash string, r model.Result) string {
	sum := sha256.Sum256(Preimage(probeID, prevHash, r))
	return hex.EncodeToString(sum[:])
}

// Sign builds the chain entry for a result.
//
// The signature covers the 32 raw bytes of the hash rather than the preimage,
// so a verifier holding only the stored hashes can check a whole chain without
// reconstructing any preimage.
func Sign(key ed25519.PrivateKey, probeID string, prevHash string, r model.Result) (Entry, error) {
	if len(key) != ed25519.PrivateKeySize {
		return Entry{}, fmt.Errorf("signing key is %d bytes, want %d", len(key), ed25519.PrivateKeySize)
	}
	selfHash := Hash(probeID, prevHash, r)
	raw, err := hex.DecodeString(selfHash)
	if err != nil {
		return Entry{}, fmt.Errorf("decode hash: %w", err)
	}
	return Entry{
		PrevHash:  prevHash,
		SelfHash:  selfHash,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw)),
	}, nil
}

// verify checks one entry against the result it claims to cover. VerifyEntry in
// bundle.go is the exported name; this stays unexported so there is one.
//
// It is here, in the probe's own package, because the probe must be able to
// verify its own chain after a restart before appending to it. The control
// plane runs the same check in TypeScript against the vectors in
// docs/evidence.md.
func verify(pub ed25519.PublicKey, probeID string, r model.Result, entry Entry) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key is %d bytes, want %d", len(pub), ed25519.PublicKeySize)
	}
	expected := Hash(probeID, entry.PrevHash, r)
	if entry.SelfHash != expected {
		return fmt.Errorf("hash mismatch: entry claims %s, result hashes to %s", entry.SelfHash, expected)
	}
	raw, err := hex.DecodeString(entry.SelfHash)
	if err != nil {
		return fmt.Errorf("decode hash: %w", err)
	}
	signature, err := base64.StdEncoding.DecodeString(entry.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pub, raw, signature) {
		return fmt.Errorf("signature does not verify against the probe's public key")
	}
	return nil
}

// Chain signs a run's results in order, threading each entry's hash into the
// next. It returns the entries and the hash the next submission must carry as
// its prev.
func Chain(key ed25519.PrivateKey, probeID string, prevHash string, results []model.Result) ([]Entry, string, error) {
	entries := make([]Entry, 0, len(results))
	for _, r := range results {
		entry, err := Sign(key, probeID, prevHash, r)
		if err != nil {
			return nil, prevHash, fmt.Errorf("sign %s/%s: %w", r.RunbookID, r.CheckID, err)
		}
		entries = append(entries, entry)
		prevHash = entry.SelfHash
	}
	return entries, prevHash, nil
}

// VerifyChain walks a sequence of results and entries, checking every signature
// and every link. It is what an evidence bundle's consumer runs.
func VerifyChain(pub ed25519.PublicKey, probeID string, prevHash string, results []model.Result, entries []Entry) error {
	if len(results) != len(entries) {
		return fmt.Errorf("%d results and %d entries", len(results), len(entries))
	}
	for i, entry := range entries {
		if entry.PrevHash != prevHash {
			return fmt.Errorf("chain broken at %d: entry follows %q, expected %q", i, entry.PrevHash, prevHash)
		}
		if err := verify(pub, probeID, results[i], entry); err != nil {
			return fmt.Errorf("entry %d: %w", i, err)
		}
		prevHash = entry.SelfHash
	}
	return nil
}
