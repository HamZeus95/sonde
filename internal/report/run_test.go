package report

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/HamZeus95/sonde/internal/model"
)

func sampleRun() model.Run {
	start := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	results := []model.Result{
		{RunbookPath: "runbooks/db-failover.md", RunbookID: "db-failover", CheckID: "record", Line: 12,
			Kind: model.KindDNS, Check: "record_exists", Status: model.StatusPass,
			Observed: model.Observed{Summary: "db-eu-1a.internal."}, LatencyMS: 12, RanAt: start},
		{RunbookPath: "runbooks/db-failover.md", RunbookID: "db-failover", CheckID: "dashboard", Line: 24,
			Kind: model.KindHTTP, Check: "status_is", Status: model.StatusFail,
			Observed: model.Observed{Summary: "404 Not Found, want 200"}, LatencyMS: 30, RanAt: start},
		{RunbookPath: "runbooks/db-failover.md", RunbookID: "db-failover", CheckID: "cluster", Line: 30,
			Kind: model.KindKubernetes, Check: "resource_exists", Status: model.StatusError,
			Observed: model.Observed{Summary: "timed out after 30s"}, LatencyMS: 30000, RanAt: start},
		{RunbookPath: "runbooks/scale.md", RunbookID: "scale", CheckID: "retired", Line: 8,
			Kind: model.KindHTTP, Check: "status_is", Status: model.StatusSkipped,
			Observed: model.Observed{Summary: "disabled in the runbook"}, RanAt: start},
	}
	return model.Run{
		SchemaVersion: model.SchemaVersion,
		StartedAt:     start,
		FinishedAt:    start.Add(31 * time.Second),
		Results:       results,
		Summary:       model.Summarise(results),
	}
}

// TestJUnitKeepsFailureAndErrorApart is why JUnit is the format worth having:
// CI already knows the difference, and Sonde must not throw it away.
func TestJUnitKeepsFailureAndErrorApart(t *testing.T) {
	var out bytes.Buffer
	if err := JUnitRun(&out, sampleRun()); err != nil {
		t.Fatalf("JUnitRun: %v", err)
	}

	var doc struct {
		Tests    int `xml:"tests,attr"`
		Failures int `xml:"failures,attr"`
		Errors   int `xml:"errors,attr"`
		Skipped  int `xml:"skipped,attr"`
		Suites   []struct {
			Name  string `xml:"name,attr"`
			Cases []struct {
				Name    string `xml:"name,attr"`
				Failure *struct {
					Message string `xml:"message,attr"`
				} `xml:"failure"`
				Error *struct {
					Message string `xml:"message,attr"`
				} `xml:"error"`
				Skipped *struct{} `xml:"skipped"`
			} `xml:"testcase"`
		} `xml:"testsuite"`
	}
	if err := xml.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("output is not valid XML: %v\n%s", err, out.String())
	}
	if doc.Tests != 4 || doc.Failures != 1 || doc.Errors != 1 || doc.Skipped != 1 {
		t.Fatalf("counts = tests %d failures %d errors %d skipped %d", doc.Tests, doc.Failures, doc.Errors, doc.Skipped)
	}
	if len(doc.Suites) != 2 {
		t.Fatalf("want one suite per runbook, got %d", len(doc.Suites))
	}

	byName := map[string]bool{}
	for _, suite := range doc.Suites {
		for _, c := range suite.Cases {
			byName[c.Name] = true
			switch c.Name {
			case "dashboard":
				if c.Failure == nil || c.Error != nil {
					t.Error("a failing assertion must be <failure>")
				}
			case "cluster":
				if c.Error == nil || c.Failure != nil {
					t.Error("an undeterminable check must be <error>, never <failure>")
				}
			case "retired":
				if c.Skipped == nil {
					t.Error("a disabled check must be <skipped>")
				}
			}
		}
	}
	if !byName["record"] {
		t.Error("the passing check is missing from the report")
	}
}

func TestTAP(t *testing.T) {
	var out bytes.Buffer
	if err := TAPRun(&out, sampleRun()); err != nil {
		t.Fatalf("TAPRun: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"TAP version 13",
		"1..4",
		"ok 1 - db-failover/record",
		"not ok 2 - db-failover/dashboard",
		"severity: fail",
		"not ok 3 - db-failover/cluster",
		"severity: error",
		"ok 4 - scale/retired # SKIP",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
}

func TestHumanRunNamesTheWrongRunbooks(t *testing.T) {
	var out bytes.Buffer
	if err := HumanRun(&out, sampleRun()); err != nil {
		t.Fatalf("HumanRun: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"PASS", "FAIL", "ERROR", "SKIP",
		"404 Not Found, want 200",
		"1 passed, 1 failed, 1 could not be checked, 1 skipped",
		"1 runbook wrong: db-failover",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
}

// TestErrorsAloneMeanNoRunbookIsWrong pins the reporting side of the fail/error
// split: nothing was determined, so nothing is accused.
func TestErrorsAloneMeanNoRunbookIsWrong(t *testing.T) {
	run := model.Run{Results: []model.Result{{
		RunbookID: "db-failover", CheckID: "c", Status: model.StatusError,
		Observed: model.Observed{Summary: "no route to host"},
	}}}
	if got := WrongRunbooks(run); len(got) != 0 {
		t.Errorf("WrongRunbooks() = %v, want none", got)
	}
}
