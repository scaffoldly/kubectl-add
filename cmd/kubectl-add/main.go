package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/scaffoldly/kubectl-add/v1alpha1/cmd/add"
	"github.com/scaffoldly/kubectl-add/v1alpha1/httpclient"
	"github.com/scaffoldly/kubectl-add/v1alpha1/selfupdate"
	"github.com/scaffoldly/kubectl-add/v1alpha1/version"
	"github.com/spf13/cobra"
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

	// cobra answers --version inside Execute, before any PersistentPreRunE runs,
	// so the update check the other commands get cannot fire for it. Detect the
	// flag here and report after Execute: the version prints immediately, and
	// the notice goes to stderr so a script parsing --version sees only stdout.
	askedVersion, token := versionRequest(rootCmd)

	err := rootCmd.ExecuteContext(ctx)
	if askedVersion && err == nil {
		if notice := selfupdate.UpdateNotice(ctx, version.String(), token, httpclient.Default()); notice != "" {
			fmt.Fprintln(os.Stderr, notice)
		}
	}
	if err != nil {
		os.Exit(1)
	}
}

// versionRequest reports whether this invocation is a bare `--version` on the
// root command, along with the GitHub token to authenticate the release check.
// It uses cobra's own parsing rather than scanning os.Args, so shorthands,
// clusters and `--` are handled exactly as the real run would; parse errors are
// ignored, since Execute reports them properly a moment later.
func versionRequest(rootCmd *cobra.Command) (asked bool, token string) {
	cmd, _, err := rootCmd.Find(os.Args[1:])
	if err != nil || cmd != rootCmd {
		return false, "" // a subcommand: --version belongs to the root only
	}
	rootCmd.InitDefaultVersionFlag()
	_ = rootCmd.ParseFlags(os.Args[1:])
	if asked, _ = rootCmd.Flags().GetBool("version"); !asked {
		return false, ""
	}
	token, _ = rootCmd.Flags().GetString("github-token")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	return true, token
}
