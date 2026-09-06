package evidence

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HamZeus95/sonde/internal/model"
)

// buildBundle writes a small, honest bundle: two probes, a chain each, and a
// manifest whose digests match. Everything that follows starts from one that
// verifies, so a failure means the tampering was detected and not that the
// fixture was broken.
func buildBundle(t *testing.T) (dir string, key ed25519.PrivateKey, runs []BundleRun) {
	t.Helper()
	dir = t.TempDir()

	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const probeID = "51038d99-6e94-4679-be1c-b50677947d50"

	results := []model.Result{
		{RunbookID: "payments-scale-up", CheckID: "deploy-exists", Status: model.StatusPass,
			RanAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC), Observed: model.Observed{Summary: "exists"}},
		{RunbookID: "payments-scale-up", CheckID: "deploy-exists", Status: model.StatusFail,
			RanAt: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), Observed: model.Observed{Summary: "does not exist"}},
		{RunbookID: "payments-scale-up", CheckID: "oncall-can-scale", Status: model.StatusError,
			RanAt: time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC), Observed: model.Observed{Summary: "timed out"}},
	}
	entries, _, err := Chain(priv, probeID, "", results)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	for i := range results {
		runs = append(runs, BundleRun{
			ProbeID: probeID, Result: results[i], Entry: entries[i],
			RunbookID: results[i].RunbookID, CheckID: results[i].CheckID,
		})
	}

	var lines strings.Builder
	for _, run := range runs {
		encoded, err := json.Marshal(run)
		if err != nil {
			t.Fatalf("marshal run: %v", err)
		}
		lines.Write(encoded)
		lines.WriteByte('\n')
	}

	runbooks, err := json.Marshal([]BundleRunbook{{
		ID: "payments-scale-up", Path: "runbooks/payments.md", Owner: "team-payments",
		Environment: "prod-eu-1", Criticality: "high",
		Checks: []string{"deploy-exists", "oncall-can-scale"},
	}})
	if err != nil {
		t.Fatalf("marshal runbooks: %v", err)
	}

	write(t, dir, RunsFile, []byte(lines.String()))
	write(t, dir, RunbooksFile, runbooks)

	manifest := Manifest{
		Version:     BundleVersion,
		Tenant:      "acme",
		GeneratedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		From:        time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		To:          time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Probes: []BundleProbe{{
			ID: probeID, Name: "eu-1", Environment: "prod-eu-1",
			PublicKey: publicKeyPEM(t, pub), ChainStart: "",
		}},
		Files: map[string]string{
			RunsFile:     digestOf(t, dir, RunsFile),
			RunbooksFile: digestOf(t, dir, RunbooksFile),
		},
	}
	manifest.Counts.Runs = len(runs)
	manifest.Counts.Runbooks = 1
	manifest.Counts.Checks = 2
	manifest.Counts.Passed = 1
	manifest.Counts.Failed = 1
	manifest.Counts.Errored = 1
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	write(t, dir, ManifestFile, encoded)
	return dir, priv, runs
}

func TestAGoodBundleVerifies(t *testing.T) {
	dir, _, _ := buildBundle(t)
	report := verifyDir(t, dir)
	if !report.OK() {
		t.Fatalf("problems: %v", report.Problems)
	}
	if report.Verified != 3 {
		t.Errorf("verified = %d, want 3", report.Verified)
	}
	if len(report.Chains) != 1 {
		t.Errorf("chains = %v", report.Chains)
	}
}

// TestARewrittenResultIsCaught is the whole point of the format: the party
// holding the database cannot turn a fail into a pass.
func TestARewrittenResultIsCaught(t *testing.T) {
	dir, _, _ := buildBundle(t)
	rewrite(t, dir, RunsFile, func(line string) string {
		return strings.Replace(line, `"status":"fail"`, `"status":"pass"`, 1)
	})
	report := verifyDir(t, dir)
	if report.OK() {
		t.Fatal("a rewritten result verified")
	}
	if !mentions(report.Problems, "hash mismatch") && !mentions(report.Problems, "changed since export") {
		t.Errorf("problems do not name the tampering: %v", report.Problems)
	}
}

// TestARemovedResultIsCaught covers the other half: a failing check cannot be
// quietly dropped from the history.
func TestARemovedResultIsCaught(t *testing.T) {
	dir, _, _ := buildBundle(t)
	content, err := os.ReadFile(filepath.Join(dir, RunsFile))
	if err != nil {
		t.Fatalf("read runs: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	// Drop the failing result and re-digest, so the manifest still matches and
	// only the chain can give it away.
	kept := append([]string{lines[0]}, lines[2])
	write(t, dir, RunsFile, []byte(strings.Join(kept, "\n")+"\n"))
	redigest(t, dir)

	report := verifyDir(t, dir)
	if report.OK() {
		t.Fatal("a removed result verified")
	}
	if !mentions(report.Problems, "missing or out of order") {
		t.Errorf("problems do not name the gap: %v", report.Problems)
	}
}

// TestAnEditedBundleIsCaughtByTheManifest covers the cheapest tampering: edit a
// file and hope nobody hashes it.
func TestAnEditedBundleIsCaughtByTheManifest(t *testing.T) {
	dir, _, _ := buildBundle(t)
	write(t, dir, RunbooksFile, []byte(`[{"id":"something-else","path":"x.md","checks":[]}]`))
	report := verifyDir(t, dir)
	if report.OK() {
		t.Fatal("an edited bundle verified")
	}
	if !mentions(report.Problems, "changed since export") {
		t.Errorf("problems: %v", report.Problems)
	}
}

// TestAnUnknownProbeIsCaught covers a bundle carrying results it has no key
// for: an export that quietly dropped a probe's public key would otherwise let
// its results through unchecked.
func TestAnUnknownProbeIsCaught(t *testing.T) {
	dir, _, _ := buildBundle(t)
	rewriteManifest(t, dir, func(m *Manifest) { m.Probes = nil })
	report := verifyDir(t, dir)
	if report.OK() {
		t.Fatal("results signed by an unlisted probe verified")
	}
	if !mentions(report.Problems, "the manifest does not list") {
		t.Errorf("problems: %v", report.Problems)
	}
}

// TestAnotherKeyCannotSign covers a control plane substituting its own key for
// the probe's.
func TestAnotherKeyCannotSign(t *testing.T) {
	dir, _, _ := buildBundle(t)
	impostor, _, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	rewriteManifest(t, dir, func(m *Manifest) { m.Probes[0].PublicKey = publicKeyPEM(t, impostor) })
	report := verifyDir(t, dir)
	if report.OK() {
		t.Fatal("results verified against a key that did not sign them")
	}
	if !mentions(report.Problems, "signature does not verify") {
		t.Errorf("problems: %v", report.Problems)
	}
}

// TestARunOutsideThePeriodIsReported covers a bundle that claims one quarter
// and contains another.
func TestARunOutsideThePeriodIsReported(t *testing.T) {
	dir, _, _ := buildBundle(t)
	rewriteManifest(t, dir, func(m *Manifest) {
		m.From = time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	})
	report := verifyDir(t, dir)
	if !mentions(report.Problems, "outside the period") {
		t.Errorf("problems: %v", report.Problems)
	}
}

func TestAnArchiveVerifiesLikeADirectory(t *testing.T) {
	dir, _, _ := buildBundle(t)
	archive := filepath.Join(t.TempDir(), "evidence.tar.gz")
	writeArchive(t, dir, archive, "sonde-evidence-2026-Q3/")

	bundle, err := Open(archive)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	report, err := Verify(bundle)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !report.OK() {
		t.Fatalf("problems: %v", report.Problems)
	}
	if report.Verified != 3 {
		t.Errorf("verified = %d", report.Verified)
	}
}

func TestAFutureFormatIsRefused(t *testing.T) {
	dir, _, _ := buildBundle(t)
	rewriteManifest(t, dir, func(m *Manifest) { m.Version = "sonde-bundle-v99" })
	bundle, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := Verify(bundle); err == nil {
		t.Fatal("a bundle in an unknown format must be refused, not guessed at")
	}
}

func TestSomethingThatIsNotABundle(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "notes.txt", []byte("hello"))
	bundle, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := Verify(bundle); err == nil {
		t.Fatal("expected an error")
	}
}

// ---------------------------------------------------------------------------

func verifyDir(t *testing.T, dir string) Report {
	t.Helper()
	bundle, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	report, err := Verify(bundle)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return report
}

func write(t *testing.T, dir, name string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func digestOf(t *testing.T, dir, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, name))
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
		t.Fatalf("encode key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func rewrite(t *testing.T, dir, name string, transform func(string) string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	write(t, dir, name, []byte(transform(string(content))))
}

func rewriteManifest(t *testing.T, dir string, change func(*Manifest)) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	change(&manifest)
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	write(t, dir, ManifestFile, encoded)
}

// redigest recomputes the manifest's file digests, which is what a determined
// editor would do.
func redigest(t *testing.T, dir string) {
	t.Helper()
	rewriteManifest(t, dir, func(m *Manifest) {
		for name := range m.Files {
			m.Files[name] = digestOf(t, dir, name)
		}
	})
}

func writeArchive(t *testing.T, dir, archive, prefix string) {
	t.Helper()
	handle, err := os.Create(archive) //nolint:gosec // a path the test chose
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer func() { _ = handle.Close() }()

	gz := gzip.NewWriter(handle)
	writer := tar.NewWriter(gz)
	for _, name := range []string{ManifestFile, RunsFile, RunbooksFile} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := writer.WriteHeader(&tar.Header{
			Name: prefix + name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
}

func mentions(problems []string, text string) bool {
	for _, problem := range problems {
		if strings.Contains(problem, text) {
			return true
		}
	}
	return false
}

// TestRunsAreNotHeldInMemory is the regression test for the bug this file's
// streaming exists to fix: Open used to read every file into a map, so
// verifying three years of history needed three years of history in RAM.
//
// The assertion is structural rather than a memory measurement, because a
// measurement is a threshold somebody eventually raises. runs.jsonl must not
// be among the files the bundle is holding — whatever its size.
func TestRunsAreNotHeldInMemory(t *testing.T) {
	dir, _, _ := buildBundle(t)
	archive := filepath.Join(t.TempDir(), "evidence.tar.gz")
	writeArchive(t, dir, archive, "sonde-evidence-2026-Q3/")

	for _, path := range []string{dir, archive} {
		bundle, err := Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		if _, held := bundle.File(RunsFile); held {
			t.Errorf("%s: %s is held in memory", path, RunsFile)
		}
		if !bundle.Has(RunsFile) {
			t.Errorf("%s: %s is not in the bundle", path, RunsFile)
		}
		// The manifest still is: everything else needs it, and it does not
		// grow with the period.
		if _, held := bundle.File(ManifestFile); !held {
			t.Errorf("%s: %s is not held", path, ManifestFile)
		}

		reader, err := bundle.OpenFile(RunsFile)
		if err != nil {
			t.Fatalf("%s: open %s: %v", path, RunsFile, err)
		}
		streamed, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("%s: read %s: %v", path, RunsFile, err)
		}
		_ = reader.Close()
		onDisk, err := os.ReadFile(filepath.Join(dir, RunsFile))
		if err != nil {
			t.Fatalf("read runs: %v", err)
		}
		if !bytes.Equal(streamed, onDisk) {
			t.Errorf("%s: streamed %s differs from the file", path, RunsFile)
		}
	}
}

// TestALongHistoryVerifies exercises the scanner over more runs than fit in one
// buffer, which is where a line-splitting mistake would show up: an off-by-one
// in the line numbering, a dropped last line, or a chain that appears to have a
// gap because a line was cut in half.
func TestALongHistoryVerifies(t *testing.T) {
	const count = 5000

	dir := t.TempDir()
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const probeID = "9a2c0f4e-7f3b-4a1d-9a08-3d4f6c2b1e57"

	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	results := make([]model.Result, count)
	for i := range results {
		results[i] = model.Result{
			RunbookID: "payments-scale-up",
			CheckID:   "deploy-exists",
			Status:    model.StatusPass,
			RanAt:     start.Add(time.Duration(i) * time.Minute),
			Observed:  model.Observed{Summary: "exists"},
		}
	}
	entries, _, err := Chain(priv, probeID, "", results)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}

	var lines bytes.Buffer
	for i := range results {
		encoded, err := json.Marshal(BundleRun{
			ProbeID: probeID, Result: results[i], Entry: entries[i],
			RunbookID: results[i].RunbookID, CheckID: results[i].CheckID,
		})
		if err != nil {
			t.Fatalf("marshal run: %v", err)
		}
		lines.Write(encoded)
		lines.WriteByte('\n')
	}
	write(t, dir, RunsFile, lines.Bytes())

	manifest := Manifest{
		Version:     BundleVersion,
		Tenant:      "acme",
		GeneratedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		From:        start,
		To:          start.Add(time.Duration(count) * time.Minute),
		Probes: []BundleProbe{{
			ID: probeID, Name: "eu-1", Environment: "prod-eu-1",
			PublicKey: publicKeyPEM(t, pub),
		}},
		Files: map[string]string{RunsFile: digestOf(t, dir, RunsFile)},
	}
	manifest.Counts.Runs = count
	manifest.Counts.Passed = count
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	write(t, dir, ManifestFile, encoded)

	report := verifyDir(t, dir)
	if !report.OK() {
		t.Fatalf("problems: %v", report.Problems)
	}
	if report.Verified != count {
		t.Errorf("verified = %d, want %d", report.Verified, count)
	}
}
