package evidence

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/HamZeus95/sonde/internal/model"
)

// BundleVersion is the evidence bundle's format version. A verifier that does
// not recognise it refuses rather than guessing: half-understood evidence is
// worse than none.
const BundleVersion = "sonde-bundle-v1"

// The files a bundle is made of.
const (
	ManifestFile = "manifest.json"
	RunsFile     = "runs.jsonl"
	RunbooksFile = "runbooks.json"
	ControlsFile = "controls.json"
)

// Manifest describes a bundle and lists the keys needed to verify it.
type Manifest struct {
	Version     string    `json:"version"`
	Tenant      string    `json:"tenant"`
	GeneratedAt time.Time `json:"generated_at"`
	// From and To bound the period the bundle covers. A verifier reports a run
	// outside them rather than silently accepting a bundle that claims one
	// period and contains another.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Probes are the identities whose signatures appear, with the public half
	// of each key. The private halves exist only inside the customer's own
	// probes, which is what makes a signature mean anything.
	Probes []BundleProbe `json:"probes"`
	// Files maps each file in the bundle to the SHA-256 of its bytes, so a
	// bundle that was edited after export does not verify.
	Files  map[string]string `json:"files"`
	Counts struct {
		Runs     int `json:"runs"`
		Runbooks int `json:"runbooks"`
		Checks   int `json:"checks"`
		Passed   int `json:"passed"`
		Failed   int `json:"failed"`
		Errored  int `json:"errored"`
		Skipped  int `json:"skipped"`
	} `json:"counts"`
}

// BundleProbe is one signing identity.
type BundleProbe struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	// PublicKey is a PEM SPKI Ed25519 key.
	PublicKey string `json:"public_key"`
	// ChainStart is the hash the first run in this bundle follows, empty when
	// the bundle begins at the probe's own genesis. It is what lets a partial
	// export — one quarter out of three years — still be a chain.
	ChainStart string `json:"chain_start"`
}

// BundleRun is one line of runs.jsonl: a result and its chain entry.
type BundleRun struct {
	ProbeID   string       `json:"probe_id"`
	Result    model.Result `json:"result"`
	Entry     Entry        `json:"entry"`
	RunbookID string       `json:"runbook_id"`
	CheckID   string       `json:"check_id"`
}

// BundleRunbook is what a run's assertions were, at export time.
type BundleRunbook struct {
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	Owner       string   `json:"owner,omitempty"`
	Environment string   `json:"environment,omitempty"`
	Criticality string   `json:"criticality,omitempty"`
	Checks      []string `json:"checks"`
}

// Report is what verification found.
type Report struct {
	Manifest Manifest `json:"manifest"`
	// Problems is empty when the bundle verifies. Each entry names one thing
	// that is wrong, in the order found.
	Problems []string `json:"problems,omitempty"`
	// Verified counts the runs whose signature and chain link both held.
	Verified int `json:"verified"`
	// Chains reports the tail each probe's chain ended on, which an operator
	// can compare against what the probe itself holds.
	Chains map[string]string `json:"chains"`
}

// OK reports whether the bundle verified completely.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Bundle is an opened bundle.
//
// The small files — the manifest, the runbook index, the control mapping — are
// held in memory, because everything that follows needs them. runs.jsonl is
// not: it is the one file that grows with the period, and a three-year bundle
// has to verify on an auditor's laptop rather than on a machine sized for it.
type Bundle struct {
	small map[string][]byte
	names []string
	open  func(name string) (io.ReadCloser, error)
}

// maxSmallFile caps what is held in memory. The manifest of a very large
// bundle is still a manifest; anything this size is not one.
const maxSmallFile = 32 << 20

// maxBundleFile caps what a single streamed entry may expand to. A bundle
// arrives from whoever is being audited, so it is untrusted input like any
// other.
const maxBundleFile = 512 << 20

// maxRunLine caps one line of runs.jsonl. A result is a status and a short
// summary; a line this long is not one, and reading it would undo the point of
// streaming the file.
const maxRunLine = 8 << 20

// Open reads a bundle from a directory or a .tar.gz.
func Open(path string) (*Bundle, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if info.IsDir() {
		return openDirectory(path)
	}
	return openArchive(path)
}

func openDirectory(root string) (*Bundle, error) {
	paths := map[string]string{}
	bundle := &Bundle{small: map[string][]byte{}}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		name := filepath.ToSlash(relative)
		paths[name] = path
		bundle.names = append(bundle.names, name)
		if name == RunsFile {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.Size() > maxSmallFile {
			return nil
		}
		content, readErr := os.ReadFile(path) //nolint:gosec // a path the user named
		if readErr != nil {
			return readErr
		}
		bundle.small[name] = content
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	bundle.open = func(name string) (io.ReadCloser, error) {
		path, ok := paths[name]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return os.Open(path) //nolint:gosec // a path inside the bundle the user named
	}
	return bundle, nil
}

func openArchive(path string) (*Bundle, error) {
	bundle := &Bundle{small: map[string][]byte{}}
	// One pass to learn what the archive holds and to keep the small files.
	// runs.jsonl is deliberately not kept: it is re-read, as a stream, when
	// the time comes to verify it.
	err := walkArchive(path, func(name string, reader io.Reader) error {
		bundle.names = append(bundle.names, name)
		if name == RunsFile {
			return nil
		}
		content, readErr := io.ReadAll(io.LimitReader(reader, maxSmallFile))
		if readErr != nil {
			return fmt.Errorf("read %s from %s: %w", name, path, readErr)
		}
		bundle.small[name] = content
		return nil
	})
	if err != nil {
		return nil, err
	}
	bundle.open = func(name string) (io.ReadCloser, error) {
		return openArchiveEntry(path, name)
	}
	return bundle, nil
}

// walkArchive calls fn once per regular file in a .tar.gz, with a reader
// positioned at its content. The reader is only valid until fn returns.
func walkArchive(path string, fn func(name string, reader io.Reader) error) error {
	handle, err := os.Open(path) //nolint:gosec // a path the user named
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = handle.Close() }()

	gz, err := gzip.NewReader(handle)
	if err != nil {
		return fmt.Errorf("read %s as gzip: %w", path, err)
	}
	defer func() { _ = gz.Close() }()

	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name, err := entryName(path, header.Name)
		if err != nil {
			return err
		}
		if err := fn(name, reader); err != nil {
			return err
		}
	}
}

// entryName normalises a tar entry to the name it is known by inside the
// bundle, refusing anything that points outside it.
func entryName(archive, raw string) (string, error) {
	name := filepath.ToSlash(filepath.Clean(raw))
	if strings.HasPrefix(name, "../") || filepath.IsAbs(name) {
		return "", fmt.Errorf("%s contains an entry outside the bundle: %q", archive, raw)
	}
	// A bundle is written with a top-level directory more often than not.
	return strings.TrimPrefix(name, topLevel(name)), nil
}

// archiveEntry keeps the readers a tar entry is read through alive for as long
// as the caller holds it.
type archiveEntry struct {
	io.Reader
	gz     *gzip.Reader
	handle *os.File
}

func (e *archiveEntry) Close() error {
	_ = e.gz.Close()
	return e.handle.Close()
}

// openArchiveEntry scans to one entry and hands back a reader over it. Scanning
// again per file costs a second pass over the compressed bytes and saves
// holding the uncompressed ones, which is the trade the whole file is about.
func openArchiveEntry(path, want string) (io.ReadCloser, error) {
	handle, err := os.Open(path) //nolint:gosec // a path the user named
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	gz, err := gzip.NewReader(handle)
	if err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("read %s as gzip: %w", path, err)
	}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = gz.Close()
			_ = handle.Close()
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name, err := entryName(path, header.Name)
		if err != nil {
			_ = gz.Close()
			_ = handle.Close()
			return nil, err
		}
		if name == want {
			return &archiveEntry{Reader: io.LimitReader(reader, maxBundleFile), gz: gz, handle: handle}, nil
		}
	}
	_ = gz.Close()
	_ = handle.Close()
	return nil, fs.ErrNotExist
}

// topLevel returns the leading directory of a path when the bundle was written
// with one, so manifest.json is found whether it is at the root or under
// sonde-evidence-2026-Q3/.
func topLevel(name string) string {
	if index := strings.IndexByte(name, '/'); index >= 0 && !strings.HasSuffix(name[:index], ".json") {
		return name[:index+1]
	}
	return ""
}

// File returns one of the small files' bytes. runs.jsonl is not one of them —
// it is read with OpenFile, and holding it was the bug this reports.
func (b *Bundle) File(name string) ([]byte, bool) {
	content, ok := b.small[name]
	return content, ok
}

// OpenFile streams one file out of the bundle. The caller closes it.
func (b *Bundle) OpenFile(name string) (io.ReadCloser, error) {
	if content, ok := b.small[name]; ok {
		return io.NopCloser(bytes.NewReader(content)), nil
	}
	return b.open(name)
}

// Has reports whether the bundle contains a file.
func (b *Bundle) Has(name string) bool {
	if _, ok := b.small[name]; ok {
		return true
	}
	return slices.Contains(b.names, name)
}

// digest hashes a file without holding it, so the memory this costs is the
// same for a bundle of a week and a bundle of three years.
func (b *Bundle) digest(name string) (string, error) {
	reader, err := b.OpenFile(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, reader); err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// Verify checks a bundle end to end: the manifest's file digests, every
// signature, and every link in every probe's chain.
//
// It needs nothing but the bundle. No network, no control plane, no account —
// an auditor runs it on a laptop, and the party being audited cannot influence
// the result.
func Verify(bundle *Bundle) (Report, error) {
	report := Report{Chains: map[string]string{}}

	raw, ok := bundle.File(ManifestFile)
	if !ok {
		return report, fmt.Errorf("this is not a bundle: no %s", ManifestFile)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return report, fmt.Errorf("read %s: %w", ManifestFile, err)
	}
	report.Manifest = manifest
	if manifest.Version != BundleVersion {
		return report, fmt.Errorf("bundle format %q is not one this build understands (expected %s)",
			manifest.Version, BundleVersion)
	}

	// The manifest's digests come first: everything after this checks content
	// that the digests prove has not been edited since export. Named in sorted
	// order, so two runs over the same broken bundle report the same problems
	// in the same order.
	names := make([]string, 0, len(manifest.Files))
	for name := range manifest.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		want := manifest.Files[name]
		got, err := bundle.digest(name)
		if errors.Is(err, fs.ErrNotExist) {
			report.Problems = append(report.Problems, fmt.Sprintf("%s is listed in the manifest and missing from the bundle", name))
			continue
		}
		if err != nil {
			return report, err
		}
		if got != want {
			report.Problems = append(report.Problems,
				fmt.Sprintf("%s has been changed since export (digest %s, manifest says %s)", name, got, want))
		}
	}

	keys := map[string]ed25519.PublicKey{}
	tails := map[string]string{}
	for _, probe := range manifest.Probes {
		key, err := publicKeyFrom(probe.PublicKey)
		if err != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("probe %s: %s", probe.ID, err))
			continue
		}
		keys[probe.ID] = key
		tails[probe.ID] = probe.ChainStart
	}

	if !bundle.Has(RunsFile) {
		return report, fmt.Errorf("this is not a bundle: no %s", RunsFile)
	}
	runs, err := bundle.OpenFile(RunsFile)
	if err != nil {
		return report, fmt.Errorf("read %s: %w", RunsFile, err)
	}
	defer func() { _ = runs.Close() }()

	// One line at a time. The whole file is never held, so verifying three
	// years of history costs what verifying a week does.
	read := 0
	index := 0
	scanner := bufio.NewScanner(runs)
	scanner.Buffer(make([]byte, 0, 64<<10), maxRunLine)
	for scanner.Scan() {
		index++
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var run BundleRun
		if err := json.Unmarshal(line, &run); err != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("%s line %d: %s", RunsFile, index, err))
			continue
		}
		read++

		key, known := keys[run.ProbeID]
		if !known {
			report.Problems = append(report.Problems,
				fmt.Sprintf("%s line %d: signed by probe %s, which the manifest does not list", RunsFile, index, run.ProbeID))
			continue
		}
		if expected := tails[run.ProbeID]; run.Entry.PrevHash != expected {
			// A gap here is the signature of a removed result: the chain is
			// how a deletion becomes visible.
			report.Problems = append(report.Problems,
				fmt.Sprintf("%s line %d: probe %s follows %q, expected %q — a result is missing or out of order",
					RunsFile, index, run.ProbeID, short(run.Entry.PrevHash), short(expected)))
		}
		if err := VerifyEntry(key, run.ProbeID, run.Result, run.Entry); err != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("%s line %d: %s", RunsFile, index, err))
		} else {
			report.Verified++
		}
		if !run.Result.RanAt.IsZero() &&
			(run.Result.RanAt.Before(manifest.From) || run.Result.RanAt.After(manifest.To)) {
			report.Problems = append(report.Problems,
				fmt.Sprintf("%s line %d: ran at %s, outside the period the manifest claims",
					RunsFile, index, run.Result.RanAt.Format(time.RFC3339)))
		}
		tails[run.ProbeID] = run.Entry.SelfHash
	}

	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return report, fmt.Errorf("%s line %d is longer than %d bytes, which no result is", RunsFile, index+1, maxRunLine)
		}
		return report, fmt.Errorf("read %s: %w", RunsFile, err)
	}

	for probe, tail := range tails {
		report.Chains[probe] = tail
	}
	// A manifest that disagrees with the file it describes is worth reporting
	// even when everything else holds: it means the two were produced from
	// different data.
	if manifest.Counts.Runs != read {
		report.Problems = append(report.Problems,
			fmt.Sprintf("the manifest claims %d runs and %s holds %d", manifest.Counts.Runs, RunsFile, read))
	}
	return report, nil
}

// VerifyEntry is Verify's per-result check, exported for callers that hold the
// pieces already.
func VerifyEntry(pub ed25519.PublicKey, probeID string, result model.Result, entry Entry) error {
	return verify(pub, probeID, result, entry)
}

func publicKeyFrom(pemText string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, fmt.Errorf("public key is not PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("public key is unreadable: %w", err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, want ed25519", parsed)
	}
	return key, nil
}

// short trims a hash for a message, where the first bytes are enough to tell
// two chains apart. An empty hash is the genesis and says so.
func short(hash string) string {
	if hash == "" {
		return "the start of the chain"
	}
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

// SortRuns orders runs the way a chain must be verified: by probe, then in the
// order the probe wrote them.
func SortRuns(runs []BundleRun) {
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].ProbeID != runs[j].ProbeID {
			return runs[i].ProbeID < runs[j].ProbeID
		}
		return runs[i].Result.RanAt.Before(runs[j].Result.RanAt)
	})
}
