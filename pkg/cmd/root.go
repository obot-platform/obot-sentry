// Package cmd wires the obocop CLI. Each subcommand is a struct with a Run
// method (and usage-tagged fields for flags), mirroring obot's pkg/cli.
package cmd

import (
	obotcmd "github.com/obot-platform/cmd"
	"github.com/spf13/cobra"
)

// Obocop is the root command.
type Obocop struct{}

// New builds the root command with its subcommands.
func New() *cobra.Command {
	return obotcmd.Command(&Obocop{},
		&Scan{},
		&Enroll{},
		&Version{},
	)
}

func (a *Obocop) Run(c *cobra.Command, _ []string) error {
	return c.Help()
}
