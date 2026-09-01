package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/HamZeus95/sonde/internal/model"
	"github.com/HamZeus95/sonde/internal/parse"
	"github.com/HamZeus95/sonde/internal/report"
)

func newParseCommand() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "parse [path...]",
		Short: "Read runbooks and print the checks they declare",
		Long: "Parse reads Markdown runbooks and prints the assertions they declare.\n\n" +
			"It executes nothing: no checks are run, no infrastructure is contacted and\n" +
			"no command in a runbook is invoked. It is safe to point at untrusted input.\n\n" +
			"Markdown files without a sonde: key in their frontmatter are skipped\n" +
			"silently, so pointing this at a whole documentation tree is fine.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f := report.Format(format)
			if !f.Valid(report.ParseFormats) {
				return exit(exitUsage, report.ErrUnknownFormat(f, report.ParseFormats))
			}
			roots := args
			if len(roots) == 0 {
				roots = []string{"."}
			}
			return runParse(cmd, roots, f)
		},
	}

	cmd.Flags().StringVar(&format, "format", string(report.FormatHuman),
		"output format ("+report.Describe(report.ParseFormats)+")")
	return cmd
}

func runParse(cmd *cobra.Command, roots []string, format report.Format) error {
	runbooks, err := parse.Walk(roots)
	if err != nil {
		var parseErrs parse.Errors
		if errors.As(err, &parseErrs) {
			for _, e := range parseErrs {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), e)
			}
			return exit(exitParse, fmt.Errorf("%s", plural(len(parseErrs), "parse error")))
		}
		return exit(exitUsage, err)
	}

	doc := model.Document{SchemaVersion: model.SchemaVersion, Runbooks: runbooks}
	out := cmd.OutOrStdout()
	switch format {
	case report.FormatJSON:
		err = report.JSONDocument(out, doc)
	default:
		err = report.HumanDocument(out, doc)
	}
	if err != nil {
		return exit(exitExecution, err)
	}
	return nil
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
