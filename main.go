package main

import (
	"os"

	"github.com/scaffoldly/kubectl-add/v1alpha1/cmd/add"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// configFlags holds the standard kubectl connection flags (--namespace,
// --context, --kubeconfig, ...) and builds REST configs from them.
var configFlags = genericclioptions.NewConfigFlags(true)

func main() {
	rootCmd := add.New().WithConfigFlags(configFlags).IntoCobra()
	rootCmd.PersistentFlags().Bool("debug", false, "enable debug output")
	configFlags.AddFlags(rootCmd.PersistentFlags())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
