package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/HamZeus95/sonde/internal/probe"
)

type probeOptions struct {
	runnerOptions
	controlPlane    string
	stateDir        string
	name            string
	environment     string
	pollWait        time.Duration
	concurrency     int
	allowChainReset bool
	caFile          string
	logFormat       string
}

func newProbeCommand() *cobra.Command {
	var opts probeOptions

	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Run as a daemon, executing checks a control plane schedules",
		Long: "Probe runs Sonde inside your network as a long-lived daemon.\n\n" +
			"It makes only outbound connections: it long-polls the control plane for\n" +
			"work, executes the checks read-only with the credentials on this machine,\n" +
			"and returns signed results. It listens on no port, and no credential it\n" +
			"holds is ever sent anywhere.\n\n" +
			"On first start it exchanges an enrolment token for a client certificate and\n" +
			"generates a signing key, which is written to the state directory and never\n" +
			"leaves it. The token is single use; after enrolment it is worthless.\n\n" +
			"The enrolment token is read from SONDE_ENROLMENT_TOKEN.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProbe(cmd, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.controlPlane, "control-plane", "", "base URL of the control plane (required)")
	flags.StringVar(&opts.stateDir, "state-dir", defaultStateDir(), "where the probe keeps its key, certificate and chain position")
	flags.StringVar(&opts.name, "name", "", "name for this probe, recorded at enrolment")
	flags.StringVar(&opts.environment, "environment", "", "environment this probe covers; also the cluster its checks resolve against")
	flags.DurationVar(&opts.pollWait, "poll-wait", probe.DefaultPollWait, "how long each long poll may block")
	flags.IntVar(&opts.concurrency, "concurrency", 0, "how many leased checks to run at once")
	flags.BoolVar(&opts.allowChainReset, "allow-chain-reset", false,
		"adopt the control plane's chain position when it disagrees with this probe's — for a control plane restored from a backup, and nothing else")
	flags.StringVar(&opts.caFile, "ca-file", "", "certificate authority to trust for the control plane (default: the system roots)")
	flags.StringVar(&opts.logFormat, "log-format", "text", "log format (text|json)")
	opts.bind(flags)
	if err := cmd.MarkFlagRequired("control-plane"); err != nil {
		panic(err)
	}
	return cmd
}

func runProbe(cmd *cobra.Command, opts probeOptions) error {
	logger, err := newLogger(cmd.ErrOrStderr(), opts.logFormat)
	if err != nil {
		return exit(exitUsage, err)
	}

	registry, err := buildRegistry(opts.runnerOptions)
	if err != nil {
		return exit(exitUsage, err)
	}

	// Signals are handled here rather than in the daemon so that a probe told
	// to stop finishes the batch it is holding instead of abandoning leases.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	daemon, err := probe.New(ctx, probe.Options{
		ControlPlane:    opts.controlPlane,
		StateDir:        opts.stateDir,
		EnrolmentToken:  os.Getenv("SONDE_ENROLMENT_TOKEN"),
		CAFile:          opts.caFile,
		Name:            opts.name,
		Environment:     opts.environment,
		Registry:        registry,
		PollWait:        opts.pollWait,
		Concurrency:     opts.concurrency,
		AllowChainReset: opts.allowChainReset,
		Version:         version(),
		Logger:          logger,
	})
	if err != nil {
		return exit(exitExecution, err)
	}

	if err := daemon.Run(ctx); err != nil {
		if errors.Is(err, probe.ErrChainDiverged) {
			// Not an execution hiccup: either the control plane was restored
			// from a backup, or this probe's history was rewritten. A human
			// decides which.
			return exit(exitExecution, err)
		}
		return exit(exitExecution, err)
	}
	logger.Info("probe stopped")
	return nil
}

func newLogger(w interface{ Write([]byte) (int, error) }, format string) (*slog.Logger, error) {
	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(w, nil)), nil
	case "text", "":
		return slog.New(slog.NewTextHandler(w, nil)), nil
	default:
		return nil, fmt.Errorf("unknown log format %q: valid formats are text, json", format)
	}
}

// defaultStateDir prefers the path a container gets from the Helm chart, and
// falls back to a per-user directory when running outside one.
func defaultStateDir() string {
	const inCluster = "/var/lib/sonde"
	if info, err := os.Stat(inCluster); err == nil && info.IsDir() {
		return inCluster
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "sonde", "probe")
	}
	return ".sonde"
}
