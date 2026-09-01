package probe

import (
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/HamZeus95/sonde/internal/model"
)

var update = flag.Bool("update", false, "rewrite the shared evidence vectors")

// vectorsPath is read by this package's tests and by the control plane's
// TypeScript tests. Both implementations hash the same inputs to the same
// digests, or the chain a probe writes is one the control plane cannot verify.
const vectorsPath = "../../docs/evidence-vectors.json"

// vector is one hashing case, in the shape both languages read.
type vector struct {
	Name     string `json:"name"`
	ProbeID  string `json:"probe_id"`
	PrevHash string `json:"prev_hash"`
	Result   struct {
		RunbookID string `json:"runbook_id"`
		CheckID   string `json:"check_id"`
		Status    string `json:"status"`
		RanAt     string `json:"ran_at"`
		Summary   string `json:"summary"`
	} `json:"result"`
	Preimage string `json:"preimage"`
	SelfHash string `json:"self_hash"`
}

func (v vector) modelResult(t *testing.T) model.Result {
	t.Helper()
	ranAt, err := time.Parse(time.RFC3339, v.Result.RanAt)
	if err != nil {
		t.Fatalf("parse ran_at: %v", err)
	}
	return model.Result{
		RunbookID: v.Result.RunbookID,
		CheckID:   v.Result.CheckID,
		Status:    model.Status(v.Result.Status),
		RanAt:     ranAt,
		Observed:  model.Observed{Summary: v.Result.Summary},
	}
}

func TestVectors(t *testing.T) {
	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vectors []vector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(vectors) == 0 {
		t.Fatal("no vectors")
	}

	for i, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			result := v.modelResult(t)
			gotPreimage := string(Preimage(v.ProbeID, v.PrevHash, result))
			gotHash := Hash(v.ProbeID, v.PrevHash, result)
			if *update {
				vectors[i].Preimage = gotPreimage
				vectors[i].SelfHash = gotHash
				return
			}
			if gotPreimage != v.Preimage {
				t.Errorf("preimage:\n got %q\nwant %q", gotPreimage, v.Preimage)
			}
			if gotHash != v.SelfHash {
				t.Errorf("self_hash = %s, want %s", gotHash, v.SelfHash)
			}
		})
	}

	if *update {
		out, err := json.MarshalIndent(vectors, "", "  ")
		if err != nil {
			t.Fatalf("encode vectors: %v", err)
		}
		if err := os.WriteFile(vectorsPath, append(out, '\n'), 0o600); err != nil {
			t.Fatalf("write vectors: %v", err)
		}
	}
}

func result(id string, status model.Status, summary string) model.Result {
	return model.Result{
		RunbookID: "db-failover",
		CheckID:   id,
		Status:    status,
		RanAt:     time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
		Observed:  model.Observed{Summary: summary},
	}
}

func TestChainVerifies(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const probeID = "8f14e45f-ea4d-4a1b-9c2e-0f7a6b3c1d2e"
	results := []model.Result{
		result("record", model.StatusPass, "db-eu-1a.internal."),
		result("dashboard", model.StatusFail, "404 Not Found, want 200"),
		result("cluster", model.StatusError, "timed out after 30s"),
	}

	entries, last, err := Chain(priv, probeID, "", results)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if err := VerifyChain(pub, probeID, "", results, entries); err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if last != entries[len(entries)-1].SelfHash {
		t.Error("the returned tail is not the last entry's hash")
	}
	if entries[0].PrevHash != "" {
		t.Error("the first entry a probe ever submits has no predecessor")
	}

	// A second submission continues from the tail, exactly as the daemon does.
	more := []model.Result{result("secret", model.StatusPass, "has key PGPASSWORD")}
	next, _, err := Chain(priv, probeID, last, more)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if err := VerifyChain(pub, probeID, last, more, next); err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
}

// TestAlteredResultFailsVerification is the point of the whole mechanism: a
// control plane that rewrites a fail into a pass cannot produce a chain that
// still verifies.
func TestAlteredResultFailsVerification(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const probeID = "probe-1"
	results := []model.Result{result("dashboard", model.StatusFail, "404 Not Found, want 200")}
	entries, _, err := Chain(priv, probeID, "", results)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}

	for _, tt := range []struct {
		name  string
		alter func(*model.Result)
	}{
		{"status flipped to pass", func(r *model.Result) { r.Status = model.StatusPass }},
		{"summary rewritten", func(r *model.Result) { r.Observed.Summary = "200 OK" }},
		{"backdated", func(r *model.Result) { r.RanAt = r.RanAt.Add(-24 * time.Hour) }},
		{"attributed to another check", func(r *model.Result) { r.CheckID = "record" }},
		{"attributed to another runbook", func(r *model.Result) { r.RunbookID = "payments" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			altered := results[0]
			tt.alter(&altered)
			if err := Verify(pub, probeID, altered, entries[0]); err == nil {
				t.Error("an altered result must not verify")
			}
		})
	}
}

// TestDroppedResultBreaksTheChain covers the other half: silently discarding a
// result leaves a gap a verifier can see.
func TestDroppedResultBreaksTheChain(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const probeID = "probe-1"
	results := []model.Result{
		result("a", model.StatusPass, "one"),
		result("b", model.StatusFail, "two"),
		result("c", model.StatusPass, "three"),
	}
	entries, _, err := Chain(priv, probeID, "", results)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}

	keptResults := []model.Result{results[0], results[2]}
	keptEntries := []Entry{entries[0], entries[2]}
	if err := VerifyChain(pub, probeID, "", keptResults, keptEntries); err == nil {
		t.Fatal("dropping the failing result must break the chain")
	}
}

// TestAnotherKeyCannotForge covers a control plane inventing results for a
// probe: it has the probe's public key and nothing else.
func TestAnotherKeyCannotForge(t *testing.T) {
	pub, _, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	_, impostor, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	results := []model.Result{result("a", model.StatusPass, "invented")}
	entries, _, err := Chain(impostor, "probe-1", "", results)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if err := VerifyChain(pub, "probe-1", "", results, entries); err == nil {
		t.Fatal("a result signed by another key must not verify")
	}
}

// TestSubmillisecondTimesAreStable guards the one lossy step in the preimage:
// ran_at is formatted to the second, so a probe and a verifier that disagree
// about sub-second precision must still agree about the hash.
func TestSubmillisecondTimesAreStable(t *testing.T) {
	base := result("a", model.StatusPass, "one")
	precise := base
	precise.RanAt = base.RanAt.Add(400 * time.Millisecond)
	if Hash("p", "", base) != Hash("p", "", precise) {
		t.Error("sub-second precision changed the hash; the control plane cannot reproduce that")
	}

	// A different second must still change it, or backdating by an hour would
	// go unnoticed.
	later := base
	later.RanAt = base.RanAt.Add(time.Second)
	if Hash("p", "", base) == Hash("p", "", later) {
		t.Error("a different second must change the hash")
	}
}

// TestTimezoneDoesNotChangeTheHash covers a probe whose clock is set to local
// time: the preimage is UTC by construction.
func TestTimezoneDoesNotChangeTheHash(t *testing.T) {
	base := result("a", model.StatusPass, "one")
	elsewhere := base
	elsewhere.RanAt = base.RanAt.In(time.FixedZone("CET", 2*60*60))
	if Hash("p", "", base) != Hash("p", "", elsewhere) {
		t.Error("the same instant in another zone must hash the same")
	}
}

func TestSignRejectsAShortKey(t *testing.T) {
	if _, err := Sign(ed25519.PrivateKey{1, 2, 3}, "p", "", result("a", model.StatusPass, "x")); err == nil {
		t.Fatal("expected an error")
	}
}
