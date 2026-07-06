package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"text/tabwriter"
	"time"

	"github.com/obot-platform/obot/apiclient/types"
	"github.com/spf13/cobra"

	"github.com/obot-platform/obocop/pkg/agent"
	"github.com/obot-platform/obocop/pkg/datadir"
	"github.com/obot-platform/obocop/pkg/mdmconfig"
	"github.com/obot-platform/obocop/pkg/version"
)

type Scan struct {
	ConfigFlags
	JSON    bool `usage:"Print the scan result as JSON"`
	Quiet   bool `usage:"Suppress the result output" short:"q"`
	Submit  bool `usage:"Submit the scan to the configured Obot server, enrolling first if needed" env:"OBOCOP_SCAN_SUBMIT"`
	Timeout int  `usage:"Number of seconds to wait for the scan to complete" default:"300" env:"OBOCOP_SCAN_TIMEOUT"`
}

func (s *Scan) Customize(cmd *cobra.Command) {
	cmd.Use = "scan"
	cmd.Short = "Inventory local AI client configuration"
	cmd.Args = cobra.NoArgs
}

func (s *Scan) Run(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if s.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(s.Timeout)*time.Second)
		defer cancel()
	}

	cfg, err := s.resolve()
	if err != nil {
		return NewConfigError(err)
	}
	if s.Submit && cfg.ServerURL == "" {
		return NewConfigError(fmt.Errorf("--submit requires a server URL (flag, env, or MDM configuration)"))
	}

	manifest, err := collectScanManifest(ctx, cfg)
	if err != nil {
		return err
	}

	if s.JSON {
		if err := writeJSON(cmd, manifest); err != nil {
			return err
		}
	} else if !s.Quiet {
		if err := writeScanTable(cmd, manifest); err != nil {
			return err
		}
	}

	if !s.Submit {
		return nil
	}

	dir, err := datadir.Dir()
	if err != nil {
		return err
	}
	idDir, err := datadir.IdentityDir()
	if err != nil {
		return err
	}
	a := agent.New(dir, idDir, cfg)
	id, st, err := a.EnsureEnrolled(ctx)
	if err != nil {
		return err
	}
	manifest.DeviceID = id.DeviceID

	scan, err := a.SubmitScan(ctx, id, st, manifest)
	if err != nil {
		return fmt.Errorf("submit scan: %w", err)
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Submitted scan (received_at=%s)\n", scan.ReceivedAt.GetTime().Format(time.RFC3339))
	return nil
}

// collectScanManifest fills the manifest envelope (device metadata) for
// the current user.
//
// Inventory collection (MCP servers, skills, plugins, client presence)
// is stubbed out for now: the manifest ships with empty observation
// lists so the enrollment and submission flow is exercised end to end.
// The scan engine lands in a follow-up.
func collectScanManifest(_ context.Context, cfg mdmconfig.Config) (types.DeviceScanManifest, error) {
	hostname := cfg.DeviceName
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	username := cfg.Username
	if username == "" {
		if u, err := user.Current(); err == nil {
			username = u.Username
		}
	}

	return types.DeviceScanManifest{
		ScannerVersion: version.Get().String(),
		ScannedAt:      types.Time{Time: time.Now().UTC()},
		Hostname:       hostname,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		Username:       username,
		Files:          []types.DeviceScanFile{},
		MCPServers:     []types.DeviceScanMCPServer{},
		Skills:         []types.DeviceScanSkill{},
		Plugins:        []types.DeviceScanPlugin{},
		Clients:        []types.DeviceScanClient{},
	}, nil
}

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeScanTable(cmd *cobra.Command, manifest types.DeviceScanManifest) error {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "Device:    %s (%s/%s)\n", tableCell(manifest.Hostname), tableCell(manifest.OS), tableCell(manifest.Arch))
	if manifest.Username != "" {
		_, _ = fmt.Fprintf(out, "User:      %s\n", tableCell(manifest.Username))
	}
	if manifest.DeviceID != "" {
		_, _ = fmt.Fprintf(out, "Device ID: %s\n", tableCell(manifest.DeviceID))
	}
	_, _ = fmt.Fprintf(out, "Scanned:   %s\n", manifest.ScannedAt.GetTime().Format(time.RFC3339))
	_, _ = fmt.Fprintf(out, "Found:     %d clients, %d MCP servers, %d skills, %d plugins, %d files\n\n",
		len(manifest.Clients), len(manifest.MCPServers), len(manifest.Skills), len(manifest.Plugins), len(manifest.Files))

	if len(manifest.Clients) == 0 {
		_, _ = fmt.Fprintln(out, "No clients found")
		return nil
	}

	mcpCounts := map[string]int{}
	for _, server := range manifest.MCPServers {
		mcpCounts[server.Client]++
	}
	skillCounts := map[string]int{}
	for _, skill := range manifest.Skills {
		skillCounts[skill.Client]++
	}
	pluginCounts := map[string]int{}
	for _, plugin := range manifest.Plugins {
		pluginCounts[plugin.Client]++
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "CLIENT\tMCP SERVERS\tSKILLS\tPLUGINS\tCONFIG PATH")
	for _, client := range manifest.Clients {
		_, _ = fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\n",
			tableCell(client.Name),
			mcpCounts[client.Name],
			skillCounts[client.Name],
			pluginCounts[client.Name],
			tableCell(client.ConfigPath),
		)
	}
	return w.Flush()
}

// tableCell renders an empty value as a placeholder dash.
func tableCell(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
