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
	rootCmd := add.New().WithConfigFlags(configFlags).IntoCobra()
	rootCmd.Version = version.String()
	rootCmd.PersistentFlags().Bool("debug", false, "enable debug output")
	rootCmd.PersistentFlags().Bool("verbose", false, "enable verbose output")
	rootCmd.Flags().Bool("remove", false, "remove the resource (kubectl delete) instead of adding it")
	rootCmd.Flags().Bool("no-edit", false, "skip the interactive edit of an install's editable inputs (e.g. helm values)")
	configFlags.AddFlags(rootCmd.PersistentFlags())

	// Cancel the run on interrupt/termination; WithCobra threads this into
	// the builder via cmd.Context().
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
