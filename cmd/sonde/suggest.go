package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/HamZeus95/sonde/internal/suggest"
)

func newSuggestCommand() *cobra.Command {
	var (
		format  string
		write   bool
		timeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "suggest <runbook.md>...",
		Short: "Draft assertion blocks for a runbook, for a human to review",
		Long: "Suggest reads a runbook's prose and drafts assertion blocks for the claims it\n" +
			"makes about live infrastructure. It prints a diff; you decide what to keep.\n\n" +
			"This is the only command that uses a language model, and the only place in\n" +
			"Sonde where one is permitted. It drafts; it does not verify. Every block it\n" +
			"proposes is run through the real parser before you see it, and anything that\n" +
			"would not parse is reported as rejected rather than shown as a suggestion.\n\n" +
			"Nothing is executed and no infrastructure is contacted: the model sees the\n" +
			"document's prose and the check catalogue, and nothing else. Your runbook text\n" +
			"does leave the machine — it is sent to the Claude API — so point this at\n" +
			"documents you are willing to send.\n\n" +
			"Needs ANTHROPIC_API_KEY in the environment.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "diff" && format != "json" {
				return exit(exitUsage, fmt.Errorf("unknown format %q: valid formats are diff, json", format))
			}
			drafter, err := suggest.NewAnthropic()
			if err != nil {
				return exit(exitUsage, err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			return runSuggest(ctx, cmd, args, drafter, format, write)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&format, "format", "diff", "output format (diff|json)")
	flags.BoolVar(&write, "write", false, "apply the suggestions to the file instead of printing a diff")
	flags.DurationVar(&timeout, "timeout", 5*time.Minute, "how long to wait for the model")
	return cmd
}

func runSuggest(
	ctx context.Context,
	cmd *cobra.Command,
	paths []string,
	drafter suggest.Drafter,
	format string,
	write bool,
) error {
	out := cmd.OutOrStdout()
	var results []suggest.Result

	for _, path := range paths {
		source, err := os.ReadFile(path) //nolint:gosec // the path the user named
		if err != nil {
			return exit(exitUsage, fmt.Errorf("read %s: %w", path, err))
		}

		request, err := suggest.Analyse(filepath.ToSlash(path), source)
		if err != nil {
			return exit(exitParse, err)
		}
		if request.Meta.ID == "" {
			// Without frontmatter the document is not a runbook, and a
			// suggested block would sit in a file the parser skips.
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
				"%s: add sonde frontmatter first, or the assertions will be skipped:\n---\nsonde:\n  version: 1\n  id: %s\n---\n",
				path, slugOf(path))
		}

		drafts, err := drafter.Draft(ctx, request)
		if err != nil {
			return exit(exitExecution, err)
		}
		result := suggest.Validate(request, source, drafts)
		results = append(results, result)

		switch {
		case format == "json":
			// Accumulated and printed together below.
		case write:
			if len(result.Suggestions) == 0 {
				_, _ = fmt.Fprintf(out, "%s: nothing to add\n", path)
				break
			}
			updated := suggest.Apply(source, result.Suggestions)
			if err := os.WriteFile(path, updated, 0o600); err != nil { //nolint:gosec // the path the user named on the command line
				return exit(exitExecution, fmt.Errorf("write %s: %w", path, err))
			}
			_, _ = fmt.Fprintf(out, "%s: added %s\n", path, plural(len(result.Suggestions), "assertion"))
		default:
			if diff := suggest.Diff(filepath.ToSlash(path), source, result.Suggestions); diff != "" {
				_, _ = fmt.Fprint(out, diff)
			}
			writeRationales(out, result)
		}
	}

	if format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return exit(exitExecution, fmt.Errorf("write json: %w", err))
		}
	}
	return nil
}

// writeRationales prints why each block was proposed, and why anything was
// rejected. The reviewer is the point of this command, so both halves matter.
func writeRationales(w io.Writer, result suggest.Result) {
	if len(result.Suggestions) == 0 && len(result.Rejected) == 0 {
		_, _ = fmt.Fprintf(w, "%s: the prose makes no testable claim this build can express.\n", result.Path)
		return
	}
	if len(result.Suggestions) > 0 {
		_, _ = fmt.Fprintln(w)
		for _, suggestion := range result.Suggestions {
			_, _ = fmt.Fprintf(w, "  %-24s %s\n", suggestion.Draft.ID, suggestion.Draft.Rationale)
		}
	}
	for _, rejected := range result.Rejected {
		_, _ = fmt.Fprintf(w, "  rejected %-15s %s\n", rejected.Draft.ID, rejected.Reason)
	}
}

// slugOf suggests a runbook id from a filename, for the frontmatter hint.
func slugOf(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(filepath.Ext(base))]
}
