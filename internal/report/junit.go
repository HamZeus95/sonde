package report

import (
	"encoding/xml"
	"fmt"
	"io"

	"github.com/HamZeus95/sonde/internal/model"
)

// JUnit is the format CI systems render as a test report. It is the only output
// format that expresses Sonde's central distinction natively: <failure> means
// the assertion did not hold, <error> means it could not be evaluated, and every
// CI system already knows the difference.

type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Errors   int          `xml:"errors,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Time     string       `xml:"time,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Errors   int         `xml:"errors,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitOutcome `xml:"failure,omitempty"`
	Error     *junitOutcome `xml:"error,omitempty"`
	Skipped   *junitSkipped `xml:"skipped,omitempty"`
}

type junitOutcome struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

type junitSkipped struct {
	Message string `xml:"message,attr"`
}

// JUnitRun writes a run as a JUnit XML report.
func JUnitRun(w io.Writer, run model.Run) error {
	doc := junitSuites{
		Name:     "sonde",
		Tests:    run.Summary.Total,
		Failures: run.Summary.Failed,
		Errors:   run.Summary.Errored,
		Skipped:  run.Summary.Skipped,
		Time:     seconds(run.FinishedAt.Sub(run.StartedAt).Milliseconds()),
	}

	// Results arrive grouped by runbook already, and one suite per runbook is
	// what makes a CI report read as "which documents are wrong".
	index := map[string]int{}
	suiteMillis := map[string]int64{}
	for _, r := range run.Results {
		i, ok := index[r.RunbookPath]
		if !ok {
			doc.Suites = append(doc.Suites, junitSuite{Name: r.RunbookPath})
			i = len(doc.Suites) - 1
			index[r.RunbookPath] = i
		}
		suite := &doc.Suites[i]
		suite.Tests++
		suiteMillis[r.RunbookPath] += r.LatencyMS

		c := junitCase{
			Name:      r.CheckID,
			ClassName: r.RunbookID,
			Time:      seconds(r.LatencyMS),
		}
		switch r.Status {
		case model.StatusFail:
			suite.Failures++
			c.Failure = &junitOutcome{
				Message: r.Observed.Summary,
				Type:    string(r.Kind) + "/" + r.Check,
				Text:    location(r),
			}
		case model.StatusError:
			suite.Errors++
			c.Error = &junitOutcome{
				Message: r.Observed.Summary,
				Type:    string(r.Kind) + "/" + r.Check,
				Text:    location(r),
			}
		case model.StatusSkipped:
			suite.Skipped++
			c.Skipped = &junitSkipped{Message: r.Observed.Summary}
		}
		suite.Cases = append(suite.Cases, c)
	}

	for i := range doc.Suites {
		doc.Suites[i].Time = seconds(suiteMillis[doc.Suites[i].Name])
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return fmt.Errorf("write junit: %w", err)
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("write junit: %w", err)
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return fmt.Errorf("write junit: %w", err)
	}
	return nil
}

// location points at the assertion that produced the result, so that a CI
// report links back to the line in the document rather than to Sonde.
func location(r model.Result) string {
	return fmt.Sprintf("%s:%d %s/%s", r.RunbookPath, r.Line, r.Kind, r.Check)
}

func seconds(ms int64) string {
	return fmt.Sprintf("%.3f", float64(ms)/1000)
}
