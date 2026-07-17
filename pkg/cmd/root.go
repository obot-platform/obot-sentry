// Package cmd wires the obot-sentry CLI. Each subcommand is a struct with a Run
// method (and usage-tagged fields for flags), mirroring obot's pkg/cli.
package cmd

import (
	obotcmd "github.com/obot-platform/cmd"
	"github.com/obot-platform/obot-sentry/pkg/mdmconfig"
	"github.com/spf13/cobra"
)

// ObotSentry is the root command.
type ObotSentry struct{}

// New builds the root command with its subcommands, reading deployment
// configuration from the real platform MDM store.
func New() *cobra.Command {
	return newRoot(mdmconfig.Load)
}

// newRoot builds the root command, wiring loadMDM into every command that
// resolves deployment configuration. Tests pass a stub so they don't depend
// on the host's real MDM configuration.
func newRoot(loadMDM func() (mdmconfig.Config, error)) *cobra.Command {
	scan := &Scan{}
	enroll := &Enroll{}
	auditCmd, auditSubmit := newAuditCommand()
	for _, cf := range []*ConfigFlags{&scan.ConfigFlags, &enroll.ConfigFlags, &auditSubmit.ConfigFlags} {
		cf.loadMDMConfig = loadMDM
	}
	return obotcmd.Command(&ObotSentry{},
		scan,
		enroll,
		&Version{},
		auditCmd,
	)
}

func (a *ObotSentry) Run(c *cobra.Command, _ []string) error {
	return c.Help()
}
