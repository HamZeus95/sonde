package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/checks/command"
	"github.com/HamZeus95/sonde/internal/checks/dns"
	httpchecks "github.com/HamZeus95/sonde/internal/checks/http"
	"github.com/HamZeus95/sonde/internal/checks/identity"
	"github.com/HamZeus95/sonde/internal/checks/kubernetes"
	"github.com/HamZeus95/sonde/internal/model"
	"github.com/HamZeus95/sonde/internal/parse"
	"github.com/HamZeus95/sonde/internal/report"
)

// runnerOptions are the flags that decide which infrastructure the runners can
// reach. They are shared by `check` and `probe`, which differ in where their
// work comes from, not in how a check is executed.
type runnerOptions struct {
	kubeconfig        string
	kubeContext       string
	keycloakURL       string
	keycloakAuthRealm string
}

func (o *runnerOptions) bind(flags *pflag.FlagSet) {
	flags.StringVar(&o.kubeconfig, "kubeconfig", "", "path to a kubeconfig (default: $KUBECONFIG, then ~/.kube/config)")
	flags.StringVar(&o.kubeContext, "context", "",
		"run every kubernetes check against this kubeconfig context, whatever cluster its runbook names")
	flags.StringVar(&o.keycloakURL, "keycloak-url", "", "base URL of a Keycloak instance, for identity checks")
	flags.StringVar(&o.keycloakAuthRealm, "keycloak-auth-realm", "master", "realm holding the Keycloak client credentials")
}

type checkOptions struct {
	runnerOptions
	format      string
	colour      string
	output      string
	concurrency int
}

func newCheckCommand() *cobra.Command {
	var opts checkOptions

	cmd := &cobra.Command{
		Use:   "check [path...]",
		Short: "Execute the assertions in runbooks against live infrastructure",
		Long: "Check executes the assertions in your runbooks read-only and reports which\n" +
			"ones have stopped being true.\n\n" +
			"It needs no account and no control plane: it reads your runbooks, uses the\n" +
			"credentials already on this machine, and prints the answer.\n\n" +
			"Exit codes: 0 all passed, 1 one or more assertions failed (a runbook is\n" +
			"wrong), 3 a runbook could not be parsed, 4 nothing failed but something\n" +
			"could not be checked at all.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			format := report.Format(opts.format)
			if !format.Valid(report.RunFormats) {
				return exit(exitUsage, report.ErrUnknownFormat(format, report.RunFormats))
			}
			if !report.ValidColourMode(report.ColourMode(opts.colour)) {
				return exit(exitUsage, fmt.Errorf("unknown color %q: valid values are auto, always, never", opts.colour))
			}
			roots := args
			if len(roots) == 0 {
				roots = []string{"."}
			}
			return runCheck(cmd.Context(), cmd, roots, format, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.format, "format", string(report.FormatHuman),
		"output format ("+report.Describe(report.RunFormats)+")")
	flags.StringVar(&opts.colour, "color", string(report.ColourAuto),
		"colour the status words (auto|always|never); auto means when stdout is a terminal")
	flags.StringVarP(&opts.output, "output", "o", "", "write the report to a file instead of stdout")
	flags.IntVar(&opts.concurrency, "concurrency", checks.DefaultConcurrency, "how many checks to run at once")
	opts.bind(flags)
	return cmd
}

func runCheck(ctx context.Context, cmd *cobra.Command, roots []string, format report.Format, opts checkOptions) error {
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

	registry, err := buildRegistry(opts.runnerOptions)
	if err != nil {
		return exit(exitUsage, err)
	}

	engine := checks.NewEngine(registry, checks.WithConcurrency(opts.concurrency))
	run := engine.Run(ctx, runbooks)

	out, closeOut, err := openOutput(cmd, opts.output)
	if err != nil {
		return exit(exitExecution, err)
	}
	if err := writeRun(out, format, run, report.NewColour(out, report.ColourMode(opts.colour))); err != nil {
		_ = closeOut()
		return exit(exitExecution, err)
	}
	if err := closeOut(); err != nil {
		return exit(exitExecution, err)
	}

	// A failed assertion outranks an error: the runbook is wrong whether or not
	// something else could not be checked. An error alone is exit 4, never 1,
	// so that a CI job can tell "your documentation is wrong" from "Sonde could
	// not reach the cluster".
	switch {
	case run.Summary.Failed > 0:
		return exit(exitFailed, fmt.Errorf("%s failed", plural(run.Summary.Failed, "check")))
	case run.Summary.Errored > 0:
		return exit(exitExecution, fmt.Errorf("%s could not be checked", plural(run.Summary.Errored, "check")))
	default:
		return nil
	}
}

// writeRun renders the run. Colour reaches only the human writer: the other
// three are contracts something else parses, and an escape sequence in a JUnit
// attribute is a broken report.
func writeRun(w io.Writer, format report.Format, run model.Run, colour report.Colour) error {
	switch format {
	case report.FormatJSON:
		return report.JSONRun(w, run)
	case report.FormatJUnit:
		return report.JUnitRun(w, run)
	case report.FormatTAP:
		return report.TAPRun(w, run)
	default:
		return report.HumanRun(w, run, colour)
	}
}

func openOutput(cmd *cobra.Command, path string) (io.Writer, func() error, error) {
	if path == "" {
		return cmd.OutOrStdout(), func() error { return nil }, nil
	}
	file, err := os.Create(path) //nolint:gosec // the path is the one the user asked for
	if err != nil {
		return nil, nil, fmt.Errorf("create %s: %w", path, err)
	}
	return file, file.Close, nil
}

// buildRegistry wires the runners this invocation can support.
//
// Clients are built lazily, so a runbook of http checks needs no kubeconfig and
// a runbook with no identity checks needs no Keycloak credentials. A check with
// no runner reports error rather than failing, which is what keeps a partial
// configuration honest instead of alarming.
func buildRegistry(opts runnerOptions) (*checks.Registry, error) {
	registry := checks.NewRegistry()
	kube := kubernetes.NewClientSet(opts.kubeconfig, opts.kubeContext)

	if err := kubernetes.New(kube).Register(registry); err != nil {
		return nil, err
	}
	if err := httpchecks.New(nil).Register(registry); err != nil {
		return nil, err
	}
	if err := dns.New(nil).Register(registry); err != nil {
		return nil, err
	}
	if err := command.New(kube).Register(registry); err != nil {
		return nil, err
	}

	providers, err := identityProviders(opts)
	if err != nil {
		return nil, err
	}
	if err := identity.New(providers).Register(registry); err != nil {
		return nil, err
	}
	return registry, nil
}

// identityProviders reads identity credentials from the environment.
//
// From the environment and never from a flag: a client secret on a command line
// lands in shell history, in a process list and in CI logs.
func identityProviders(opts runnerOptions) (map[string]identity.Provider, error) {
	if opts.keycloakURL == "" {
		return nil, nil
	}
	clientID := os.Getenv("SONDE_KEYCLOAK_CLIENT_ID")
	clientSecret := os.Getenv("SONDE_KEYCLOAK_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("--keycloak-url needs SONDE_KEYCLOAK_CLIENT_ID and SONDE_KEYCLOAK_CLIENT_SECRET in the environment")
	}
	keycloak, err := identity.NewKeycloak(identity.KeycloakConfig{
		BaseURL:      opts.keycloakURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AuthRealm:    opts.keycloakAuthRealm,
	})
	if err != nil {
		return nil, err
	}
	return map[string]identity.Provider{"keycloak": keycloak}, nil
}
