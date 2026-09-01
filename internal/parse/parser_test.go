package parse

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HamZeus95/sonde/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files instead of comparing against them")

// TestValid parses every runbook under testdata/valid and compares the JSON it
// produces against a golden file. The golden files are the contract: a diff
// here is a diff the control plane and every stored content hash will see.
func TestValid(t *testing.T) {
	for _, path := range mdFiles(t, "testdata/valid") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			rb, err := File(path)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if rb == nil {
				t.Fatal("expected a runbook, got a skipped file")
			}
			got, err := json.MarshalIndent(rb, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			compareGolden(t, replaceExt(path, ".json"), string(got)+"\n")
		})
	}
}

// TestInvalid checks that broken runbooks fail with every problem reported, not
// just the first: a user fixing a runbook should need one pass, not five.
func TestInvalid(t *testing.T) {
	for _, path := range mdFiles(t, "testdata/invalid") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			rb, err := File(path)
			if err == nil {
				t.Fatal("expected parse errors, got none")
			}
			if rb != nil {
				t.Fatal("a runbook that failed to parse must not be returned")
			}
			var parseErrs Errors
			if !errors.As(err, &parseErrs) {
				t.Fatalf("expected parse.Errors, got %T: %v", err, err)
			}
			compareGolden(t, replaceExt(path, ".errors"), parseErrs.Error()+"\n")
		})
	}
}

// TestSkipped covers the files Sonde must ignore without saying anything. A
// repository adopting Sonde points it at a documentation tree where almost
// nothing is a runbook; warning about those files would bury the real output.
func TestSkipped(t *testing.T) {
	for _, path := range mdFiles(t, "testdata/skipped") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			rb, err := File(path)
			if err != nil {
				t.Fatalf("a non-runbook must not produce an error: %v", err)
			}
			if rb != nil {
				t.Fatalf("expected the file to be skipped, got runbook %q", rb.Meta.ID)
			}
		})
	}
}

// TestWalkIsDeterministic pins the ordering guarantee the content hashes and
// golden files depend on.
func TestWalkIsDeterministic(t *testing.T) {
	first, err := Walk([]string{"testdata/valid"})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("no runbooks found")
	}
	second, err := Walk([]string{"testdata/valid"})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for i := range first {
		if first[i].Path != second[i].Path {
			t.Fatalf("walk order changed between runs: %q then %q", first[i].Path, second[i].Path)
		}
		if i > 0 && first[i-1].Path >= first[i].Path {
			t.Fatalf("walk is not sorted: %q before %q", first[i-1].Path, first[i].Path)
		}
	}
}

// TestWalkReportsEveryFilesErrors checks that one broken runbook does not hide
// the others.
func TestWalkReportsEveryFilesErrors(t *testing.T) {
	_, err := Walk([]string{"testdata/invalid"})
	var parseErrs Errors
	if !errors.As(err, &parseErrs) {
		t.Fatalf("expected parse.Errors, got %T", err)
	}
	files := map[string]bool{}
	for _, e := range parseErrs {
		files[e.Path] = true
	}
	if len(files) < len(mdFiles(t, "testdata/invalid")) {
		t.Fatalf("only %d of %d broken files reported", len(files), len(mdFiles(t, "testdata/invalid")))
	}
}

// TestContentHashTracksContent guards the field the control plane uses to skip
// re-indexing unchanged runbooks.
func TestContentHashTracksContent(t *testing.T) {
	src := []byte("---\nsonde:\n  version: 1\n  id: hash\n---\n")
	first, err := Source("hash.md", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	second, err := Source("hash.md", append(src, '\n'))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if first.ContentHash == second.ContentHash {
		t.Fatal("content hash did not change when the content did")
	}
	again, err := Source("elsewhere.md", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if first.ContentHash != again.ContentHash {
		t.Fatal("content hash changed with the path, which is not part of the content")
	}
}

// TestDefaults pins the behaviour of a block that sets only the required fields.
func TestDefaults(t *testing.T) {
	rb, err := Source("defaults.md", []byte(strings.Join([]string{
		"---", "sonde:", "  version: 1", "  id: defaults", "---", "",
		"```sonde", "id: c", "kind: http", "check: status_is",
		"url: https://example.com", "```", "",
	}, "\n")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := rb.Checks[0]
	if !c.Enabled {
		t.Error("checks are enabled unless the runbook says otherwise")
	}
	if c.Timeout != nil {
		t.Error("an unset timeout stays unset so the runner can apply its own default")
	}
	if c.Verbose {
		t.Error("results are minimal by default")
	}
	spec, ok := c.Spec.(*model.StatusIsSpec)
	if !ok {
		t.Fatalf("spec is %T", c.Spec)
	}
	if got := spec.ExpectedStatus(); got != 200 {
		t.Errorf("default expected status = %d, want 200", got)
	}
}

func mdFiles(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	if len(paths) == 0 {
		t.Fatalf("no testdata in %s", dir)
	}
	return paths
}

func replaceExt(path, ext string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + ext
}

func compareGolden(t *testing.T, golden, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run: go test ./internal/parse -update): %v", err)
	}
	if string(want) != got {
		t.Errorf("output does not match %s\n--- want ---\n%s\n--- got ---\n%s", golden, want, got)
	}
}
