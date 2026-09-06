package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HamZeus95/sonde/internal/evidence"
	"github.com/HamZeus95/sonde/internal/model"
)

// buildBundle writes a small, honest bundle: one probe, one chain, a manifest
// whose digests match. Everything here starts from a bundle that verifies, so a
// failure means the tampering was caught rather than that the fixture is wrong.
func buildBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	pub, priv, err := evidence.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const probeID = "51038d99-6e94-4679-be1c-b50677947d50"

	results := []model.Result{
		{RunbookID: "payments-scale-up", CheckID: "deploy-exists", Status: model.StatusPass,
			RanAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC), Observed: model.Observed{Summary: "exists"}},
		{RunbookID: "payments-scale-up", CheckID: "oncall-can-scale", Status: model.StatusFail,
			RanAt: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), Observed: model.Observed{Summary: "cannot patch"}},
	}
	entries, _, err := evidence.Chain(priv, probeID, "", results)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}

	var lines strings.Builder
	for i := range results {
		encoded, err := json.Marshal(evidence.BundleRun{
			ProbeID: probeID, Result: results[i], Entry: entries[i],
			RunbookID: results[i].RunbookID, CheckID: results[i].CheckID,
		})
		if err != nil {
			t.Fatalf("marshal run: %v", err)
		}
		lines.Write(encoded)
		lines.WriteByte('\n')
	}
	writeBundleFile(t, dir, evidence.RunsFile, []byte(lines.String()))

	publicKey := publicKeyPEM(t, pub)
	manifest := evidence.Manifest{
		Version:     evidence.BundleVersion,
		Tenant:      "acme",
		GeneratedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		From:        time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		To:          time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Probes: []evidence.BundleProbe{{
			ID: probeID, Name: "eu-1", Environment: "prod-eu-1", PublicKey: publicKey,
		}},
		Files: map[string]string{evidence.RunsFile: digestOf(t, dir, evidence.RunsFile)},
	}
	manifest.Counts.Runs = len(results)
	manifest.Counts.Runbooks = 1
	manifest.Counts.Checks = 2
	manifest.Counts.Passed = 1
	manifest.Counts.Failed = 1
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeBundleFile(t, dir, evidence.ManifestFile, encoded)
	return dir
}

// TestVerifyExitCodes pins the numbers an auditor's CI branches on. The library
// beneath this is tested thoroughly; these are the four answers the command
// itself is allowed to give.
func TestVerifyExitCodes(t *testing.T) {
	t.Run("a bundle that verifies exits 0", func(t *testing.T) {
		out, code := run(t, "verify", buildBundle(t))
		if code != exitOK {
			t.Fatalf("exit code = %d, want %d\n%s", code, exitOK, out)
		}
		if !strings.Contains(out, "verified") {
			t.Errorf("output should say what verified:\n%s", out)
		}
	})

	t.Run("a tampered bundle exits 1", func(t *testing.T) {
		dir := buildBundle(t)
		// Turn the failing result into a passing one, which is exactly what a
		// party being audited would want to do.
		runs := filepath.Join(dir, evidence.RunsFile)
		content, err := os.ReadFile(runs) //nolint:gosec // a path the test chose
		if err != nil {
			t.Fatalf("read runs: %v", err)
		}
		edited := strings.Replace(string(content), `"status":"fail"`, `"status":"pass"`, 1)
		if edited == string(content) {
			t.Fatal("the fixture no longer contains a failing result")
		}
		writeBundleFile(t, dir, evidence.RunsFile, []byte(edited))

		out, code := run(t, "verify", dir)
		if code != exitFailed {
			t.Fatalf("exit code = %d, want %d\n%s", code, exitFailed, out)
		}
	})

	t.Run("something that is not a bundle exits 4", func(t *testing.T) {
		// Not evidence that failed to verify — evidence that could not be
		// read. Conflating the two would tell an auditor a bundle is bad when
		// they pointed at the wrong directory.
		out, code := run(t, "verify", t.TempDir())
		if code != exitExecution {
			t.Fatalf("exit code = %d, want %d\n%s", code, exitExecution, out)
		}
	})

	t.Run("a missing bundle exits 4", func(t *testing.T) {
		out, code := run(t, "verify", filepath.Join(t.TempDir(), "absent.tar.gz"))
		if code != exitExecution {
			t.Fatalf("exit code = %d, want %d\n%s", code, exitExecution, out)
		}
	})

	t.Run("an unknown format exits 2", func(t *testing.T) {
		out, code := run(t, "verify", "--format", "yaml", buildBundle(t))
		if code != exitUsage {
			t.Fatalf("exit code = %d, want %d\n%s", code, exitUsage, out)
		}
	})
}

// TestVerifyJSONIsMachineReadable covers the other half of the contract: a
// pipeline that reads the report rather than the exit code.
func TestVerifyJSONIsMachineReadable(t *testing.T) {
	out, code := run(t, "verify", "--format", "json", buildBundle(t))
	if code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	var report evidence.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not the report: %v\n%s", err, out)
	}
	if !report.OK() {
		t.Errorf("problems: %v", report.Problems)
	}
	if report.Verified != 2 {
		t.Errorf("verified = %d, want 2", report.Verified)
	}
}

func writeBundleFile(t *testing.T, dir, name string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func digestOf(t *testing.T, dir, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a path the test chose
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func publicKeyPEM(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}
