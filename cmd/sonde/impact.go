package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/HamZeus95/sonde/internal/impact"
)

func newImpactCommand() *cobra.Command {
	var (
		before  string
		after   string
		plan    string
		cluster string
		format  string
	)

	cmd := &cobra.Command{
		Use:   "impact",
		Short: "Report the infrastructure resources a change removes",
		Long: "Impact reads a change and prints the resources it deletes, as canonical\n" +
			"resource URIs.\n\n" +
			"It is the other half of the reverse index: `sonde parse` turns runbooks into\n" +
			"the same URIs, so joining the two answers the question worth asking before a\n" +
			"merge — which documented procedures does this change break?\n\n" +
			"Give it either a pair of manifest trees (--before and --after) or an OpenTofu\n" +
			"plan in JSON form (--plan, from `tofu show -json tfplan`).\n\n" +
			"It reads files and nothing else: no cluster, no cloud API, no network.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch {
			case plan != "" && (before != "" || after != ""):
				return exit(exitUsage, fmt.Errorf("give either --plan or --before/--after, not both"))
			case plan == "" && before == "" && after == "":
				return exit(exitUsage, fmt.Errorf("give either --plan, or --before and --after"))
			}

			var (
				report impact.Report
				err    error
			)
			if plan != "" {
				report, err = impact.Plan(plan, cluster)
			} else {
				report, err = impact.Manifests(before, after, cluster)
			}
			if err != nil {
				return exit(exitUsage, err)
			}

			out := cmd.OutOrStdout()
			if format == "json" {
				return writeImpactJSON(out, report)
			}
			return writeImpactHuman(out, report)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&before, "before", "", "manifests as they are before the change (a file or a directory)")
	flags.StringVar(&after, "after", "", "manifests as they are after the change")
	flags.StringVar(&plan, "plan", "", "an OpenTofu or Terraform plan in JSON form")
	flags.StringVar(&cluster, "cluster", "",
		"the cluster these manifests deploy to; without it, removals carry their parts rather than a full URI")
	flags.StringVar(&format, "format", "human", "output format (human|json)")
	return cmd
}

func writeImpactJSON(w io.Writer, report impact.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return exit(exitExecution, fmt.Errorf("write json: %w", err))
	}
	return nil
}

func writeImpactHuman(w io.Writer, report impact.Report) error {
	if len(report.Removed) == 0 && len(report.Unmappable) == 0 {
		_, err := fmt.Fprintln(w, "This change removes no infrastructure resources.")
		return err
	}
	for _, removal := range report.Removed {
		identifier := removal.Canonical
		if identifier == "" {
			// Without a cluster there is no full URI, so show the parts the
			// index will actually be queried on.
			identifier = fmt.Sprintf("%s %s/%s in %s", removal.Provider, removal.Type, removal.Name, removal.Namespace)
		}
		if _, err := fmt.Fprintf(w, "removed  %-64s %s\n", identifier, removal.Source); err != nil {
			return err
		}
	}
	for _, u := range report.Unmappable {
		if _, err := fmt.Fprintf(w, "skipped  %-64s %s\n", u.Source, u.Reason); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "\n%s removed, %s not mappable to a resource URI\n",
		plural(len(report.Removed), "resource"), plural(len(report.Unmappable), "change"))
	return err
}
