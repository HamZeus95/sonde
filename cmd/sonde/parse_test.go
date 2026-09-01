package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/HamZeus95/sonde/internal/model"
)

// run executes the CLI in process and reports what a shell would see.
func run(t *testing.T, args ...string) (stdout string, code int) {
	t.Helper()
	var out bytes.Buffer
	root := newRootCommand()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)

	err := root.Execute()
	if err == nil {
		return out.String(), exitOK
	}
	var exitErr *exitError
	if errors.As(err, &exitErr) {
		return out.String(), exitErr.code
	}
	return out.String(), exitUsage
}

// TestExitCodes pins the numbers CI pipelines branch on. Changing one silently
// turns a red build green somewhere.
func TestExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"clean parse", []string{"parse", "../../examples/runbooks"}, exitOK},
		{"a tree with no runbooks is not a failure", []string{"parse", "../../internal/report"}, exitOK},
		{"broken runbook", []string{"parse", "../../internal/parse/testdata/invalid"}, exitParse},
		{"unknown format", []string{"parse", "--format", "junit", "../../examples/runbooks"}, exitUsage},
		{"missing path", []string{"parse", "./does-not-exist"}, exitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, code := run(t, tt.args...); code != tt.want {
				t.Errorf("exit code = %d, want %d", code, tt.want)
			}
		})
	}
}

// TestJSONOutputIsTheContract checks the shape sonde-cloud's zod schemas
// mirror, and that it carries its own version.
func TestJSONOutputIsTheContract(t *testing.T) {
	stdout, code := run(t, "parse", "--format", "json", "../../examples/runbooks")
	if code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, stdout)
	}
	var doc model.Document
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if doc.SchemaVersion != model.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", doc.SchemaVersion, model.SchemaVersion)
	}
	if len(doc.Runbooks) == 0 {
		t.Fatal("no runbooks in the example directory")
	}
	for _, rb := range doc.Runbooks {
		if rb.ContentHash == "" || rb.Meta.ID == "" {
			t.Errorf("%s: incomplete runbook %+v", rb.Path, rb.Meta)
		}
	}
}

// TestParseErrorsGoToStderr keeps stdout parseable: a consumer piping JSON into
// jq must not have error text spliced into it.
func TestParseErrorsGoToStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := newRootCommand()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"parse", "--format", "json", "../../internal/parse/testdata/invalid"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected an error")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on a parse failure, got:\n%s", stdout.String())
	}
}

// TestSkippedFilesAreSilent covers the adoption case: pointing Sonde at a
// documentation tree where almost nothing is a runbook.
func TestSkippedFilesAreSilent(t *testing.T) {
	stdout, code := run(t, "parse", "../../internal/parse/testdata/skipped")
	if code != exitOK {
		t.Fatalf("exit code = %d", code)
	}
	if strings.Contains(stdout, "warning") {
		t.Errorf("non-runbooks must not warn:\n%s", stdout)
	}
}
