package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/scaffoldly/kubectl-add/v1alpha1/cmd/add"
	"github.com/scaffoldly/kubectl-add/v1alpha1/version"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// configFlags holds the standard kubectl connection flags (--namespace,
// --context, --kubeconfig, ...) and builds REST configs from them.
var configFlags = genericclioptions.NewConfigFlags(true)

func main() {
	// IntoCobra binds the add-specific flags and the positional <resource> via
	// reeflective/flags; the kubectl connection flags are added on top here.
	rootCmd := add.New().WithConfigFlags(configFlags).IntoCobra()
	rootCmd.Version = version.String()
	configFlags.AddFlags(rootCmd.PersistentFlags())

	// Cancel the run on interrupt/termination; a PersistentPreRunE threads this
	// context into the builder via cmd.Context().
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
