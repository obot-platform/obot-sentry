package cmd

import (
	"fmt"

	"github.com/obot-platform/obot-sentry/pkg/version"
	"github.com/spf13/cobra"
)

type Version struct{}

func (v *Version) Customize(cmd *cobra.Command) {
	cmd.Use = "version"
	cmd.Short = "Print the obot-sentry version"
	cmd.Args = cobra.NoArgs
}

func (v *Version) Run(*cobra.Command, []string) error {
	fmt.Println("Version: ", version.Get())
	return nil
}
