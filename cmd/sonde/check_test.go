package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HamZeus95/sonde/internal/model"
)

// writeRunbook puts a runbook in a temp directory. The URLs are substituted at
// write time because an httptest server's port is only known at run time.
func writeRunbook(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "runbook.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write runbook: %v", err)
	}
	return dir
}

func httpRunbook(t *testing.T, url string, expect int) string {
	t.Helper()
	return writeRunbook(t, fmt.Sprintf(`---
sonde:
  version: 1
  id: dashboards
  environment: prod-eu-1
---

# Dashboards

`+"```sonde"+`
id: dashboard
kind: http
check: status_is
url: %s
expect: %d
`+"```"+`
`, url, expect))
}

func liveServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestCheckExitCodes is the contract CI branches on, end to end.
func TestCheckExitCodes(t *testing.T) {
	server := liveServer(t)

	t.Run("a true runbook exits 0", func(t *testing.T) {
		dir := httpRunbook(t, server.URL, 200)
		if _, code := run(t, "check", dir); code != exitOK {
			t.Errorf("exit code = %d, want %d", code, exitOK)
		}
	})

	t.Run("a wrong runbook exits 1", func(t *testing.T) {
		dir := httpRunbook(t, server.URL, 404)
		out, code := run(t, "check", dir)
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d\n%s", code, exitFailed, out)
		}
		if !strings.Contains(out, "runbook wrong: dashboards") {
			t.Errorf("output should name the wrong runbook:\n%s", out)
		}
	})

	t.Run("an unreachable cluster exits 4, not 1", func(t *testing.T) {
		dir := writeRunbook(t, `---
sonde:
  version: 1
  id: payments
  environment: prod-eu-1
---

`+"```sonde"+`
id: deploy-exists
kind: kubernetes
check: resource_exists
resource: deployment/payments-api
namespace: payments
`+"```"+`
`)
		out, code := run(t, "check", "--kubeconfig", filepath.Join(t.TempDir(), "absent"), dir)
		if code != exitExecution {
			t.Errorf("exit code = %d, want %d\n%s", code, exitExecution, out)
		}
		if strings.Contains(out, "runbook wrong") {
			t.Errorf("a cluster Sonde cannot reach must not be reported as a wrong runbook:\n%s", out)
		}
	})

	t.Run("a broken runbook exits 3", func(t *testing.T) {
		dir := writeRunbook(t, `---
sonde:
  version: 1
  id: broken
---

`+"```sonde"+`
id: nope
kind: kubernetes
check: does_not_exist
`+"```"+`
`)
		if _, code := run(t, "check", dir); code != exitParse {
			t.Errorf("exit code = %d, want %d", code, exitParse)
		}
	})

	t.Run("an unknown format is a usage error", func(t *testing.T) {
		dir := httpRunbook(t, server.URL, 200)
		if _, code := run(t, "check", "--format", "yaml", dir); code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
	})
}

func TestCheckFormats(t *testing.T) {
	server := liveServer(t)
	dir := httpRunbook(t, server.URL, 200)

	t.Run("json", func(t *testing.T) {
		out, code := run(t, "check", "--format", "json", dir)
		if code != exitOK {
			t.Fatalf("exit code = %d\n%s", code, out)
		}
		var parsed model.Run
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			t.Fatalf("output is not valid JSON: %v\n%s", err, out)
		}
		if parsed.SchemaVersion != model.SchemaVersion {
			t.Errorf("schema_version = %d", parsed.SchemaVersion)
		}
		if len(parsed.Results) != 1 || parsed.Results[0].Status != model.StatusPass {
			t.Errorf("results = %+v", parsed.Results)
		}
	})

	t.Run("junit", func(t *testing.T) {
		out, code := run(t, "check", "--format", "junit", dir)
		if code != exitOK {
			t.Fatalf("exit code = %d\n%s", code, out)
		}
		var doc struct {
			Tests int `xml:"tests,attr"`
		}
		if err := xml.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("output is not valid XML: %v\n%s", err, out)
		}
		if doc.Tests != 1 {
			t.Errorf("tests = %d", doc.Tests)
		}
	})

	t.Run("tap", func(t *testing.T) {
		out, code := run(t, "check", "--format", "tap", dir)
		if code != exitOK {
			t.Fatalf("exit code = %d\n%s", code, out)
		}
		if !strings.HasPrefix(out, "TAP version 13\n1..1\n") {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("output to a file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "report.xml")
		out, code := run(t, "check", "--format", "junit", "--output", path, dir)
		if code != exitOK {
			t.Fatalf("exit code = %d\n%s", code, out)
		}
		written, err := os.ReadFile(path) //nolint:gosec // the path is the test's own
		if err != nil {
			t.Fatalf("read report: %v", err)
		}
		if !strings.Contains(string(written), "<testsuites") {
			t.Errorf("report = %s", written)
		}
		if strings.Contains(out, "<testsuites") {
			t.Error("with --output the report must not also go to stdout")
		}
	})
}
