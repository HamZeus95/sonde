package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/HamZeus95/sonde/internal/evidence"
)

func newVerifyCommand() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "verify <bundle>",
		Short: "Check a signed evidence bundle, offline",
		Long: "Verify checks an evidence bundle: every signature against the key the probe\n" +
			"enrolled with, and every link in every chain.\n\n" +
			"It needs nothing but the bundle — no network, no control plane, no account.\n" +
			"That is the point. An auditor runs this on a laptop, and the party being\n" +
			"audited cannot influence the result.\n\n" +
			"Exporting a bundle is a control plane feature. Verifying one is free and\n" +
			"open source, because evidence nobody outside the vendor can check is not\n" +
			"evidence.\n\n" +
			"The bundle may be a directory or a .tar.gz.\n\n" +
			"Exit codes: 0 the bundle verified, 1 it did not, 2 usage, 4 it could not be read.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "human" && format != "json" {
				return exit(exitUsage, fmt.Errorf("unknown format %q: valid formats are human, json", format))
			}
			bundle, err := evidence.Open(args[0])
			if err != nil {
				return exit(exitExecution, err)
			}
			report, err := evidence.Verify(bundle)
			if err != nil {
				return exit(exitExecution, err)
			}

			out := cmd.OutOrStdout()
			if format == "json" {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil {
					return exit(exitExecution, fmt.Errorf("write json: %w", err))
				}
			} else {
				writeVerifyHuman(out, report)
			}

			if !report.OK() {
				return exit(exitFailed, fmt.Errorf("%s", plural(len(report.Problems), "problem")))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "human", "output format (human|json)")
	return cmd
}

func writeVerifyHuman(w io.Writer, report evidence.Report) {
	m := report.Manifest
	_, _ = fmt.Fprintf(w, "%s\n", m.Version)
	_, _ = fmt.Fprintf(w, "  period      %s to %s\n", m.From.Format("2006-01-02"), m.To.Format("2006-01-02"))
	_, _ = fmt.Fprintf(w, "  exported    %s\n", m.GeneratedAt.Format("2006-01-02 15:04:05 MST"))
	_, _ = fmt.Fprintf(w, "  runbooks    %d, %s\n", m.Counts.Runbooks, plural(m.Counts.Checks, "check"))
	_, _ = fmt.Fprintf(w, "  results     %d passed, %d failed, %d could not be checked, %d skipped\n",
		m.Counts.Passed, m.Counts.Failed, m.Counts.Errored, m.Counts.Skipped)

	for _, probe := range m.Probes {
		_, _ = fmt.Fprintf(w, "  probe       %s (%s)\n", probe.Name, probe.Environment)
	}

	_, _ = fmt.Fprintln(w)
	if report.OK() {
		_, _ = fmt.Fprintf(w, "%s verified. Every signature holds and no result is missing from any chain.\n",
			plural(report.Verified, "result"))
		return
	}
	for _, problem := range report.Problems {
		_, _ = fmt.Fprintf(w, "  %s\n", problem)
	}
	_, _ = fmt.Fprintf(w, "\n%s verified, %s.\n", plural(report.Verified, "result"), plural(len(report.Problems), "problem"))
}
