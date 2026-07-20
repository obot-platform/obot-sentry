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

	"github.com/obot-platform/obot-sentry/pkg/agent"
	"github.com/obot-platform/obot-sentry/pkg/datadir"
	"github.com/obot-platform/obot-sentry/pkg/mdmconfig"
	"github.com/obot-platform/obot-sentry/pkg/scan"
	"github.com/obot-platform/obot-sentry/pkg/state"
	"github.com/obot-platform/obot-sentry/pkg/version"
)

type Scan struct {
	ConfigFlags
	JSON     bool `usage:"Print the scan result as JSON"`
	Quiet    bool `usage:"Suppress the result output" short:"q"`
	Submit   bool `usage:"Submit the scan to the configured Obot server, enrolling first if needed" env:"OBOT_SENTRY_SCAN_SUBMIT"`
	Timeout  int  `usage:"Number of seconds to wait for the scan to complete" default:"300" env:"OBOT_SENTRY_SCAN_TIMEOUT"`
	MaxDepth int  `usage:"Maximum path depth (in segments below each scan root) to crawl for project-scope configs and skills" default:"5" env:"OBOT_SENTRY_SCAN_MAX_DEPTH"`
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

	startedAt := time.Now().UTC()

	// The OS scheduler polls every few minutes; throttle real submissions
	// to the configured interval against the per-user scan state. Cache
	// problems must never block a scan, so they only degrade to warnings.
	cacheDir, cacheErr := datadir.CacheDir()
	if cacheErr != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: resolving cache directory: %v\n", cacheErr)
	}
	if s.Submit && cacheErr == nil {
		scanState, err := state.LoadScanState(cacheDir)
		if err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: reading scan state: %v\n", err)
		} else if interval := cfg.ScanInterval(); scanState.SubmittedWithin(interval, startedAt) {
			if !s.Quiet {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Last scan was submitted at %s, within the %s interval; skipping\n",
					scanState.LastSubmitAt.Format(time.RFC3339), interval)
			}
			s.recordScanOutcome(cmd, cacheDir, scanState, startedAt, "skipped", nil)
			return nil
		}
	}

	manifest, err := s.collectManifest(ctx)
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

	submitErr := s.submit(ctx, cmd, cfg, &manifest)
	if cacheErr == nil {
		scanState, _ := state.LoadScanState(cacheDir)
		now := time.Now().UTC()
		scanState.LastScanAt = &now
		scanState.DeviceID = manifest.DeviceID
		scanState.ScannerVersion = manifest.ScannerVersion
		outcome := "submitted"
		if submitErr != nil {
			outcome = "error"
			scanState.LastStatus = "error"
			scanState.LastError = submitErr.Error()
		} else {
			scanState.LastSubmitAt = &now
			scanState.LastStatus = "ok"
			scanState.LastError = ""
		}
		s.recordScanOutcome(cmd, cacheDir, scanState, startedAt, outcome, submitErr)
	}
	return submitErr
}

// submit enrolls (if needed) and uploads the manifest, filling in the
// device ID it enrolled as.
func (s *Scan) submit(ctx context.Context, cmd *cobra.Command, cfg mdmconfig.Config, manifest *types.DeviceScanManifest) error {
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

	submitted, err := a.SubmitScan(ctx, id, st, *manifest)
	if err != nil {
		return fmt.Errorf("submit scan: %w", err)
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Submitted scan (received_at=%s)\n", submitted.ReceivedAt.GetTime().Format(time.RFC3339))
	return nil
}

// recordScanOutcome persists the scan state and appends a scan log
// record. Recording must never fail a scan, so problems only warn.
func (s *Scan) recordScanOutcome(cmd *cobra.Command, cacheDir string, scanState state.ScanState, startedAt time.Time, outcome string, runErr error) {
	if outcome != "skipped" {
		if err := scanState.Save(cacheDir); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: writing scan state: %v\n", err)
		}
	}
	record := state.ScanLogRecord{
		StartedAt:      startedAt,
		FinishedAt:     time.Now().UTC(),
		Outcome:        outcome,
		DeviceID:       scanState.DeviceID,
		ScannerVersion: version.Get().String(),
	}
	if runErr != nil {
		record.Error = runErr.Error()
	}
	if err := state.AppendScanLog(cacheDir, record); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: writing scan log: %v\n", err)
	}
}

// collectManifest runs the scan engine over this machine's roots (the
// user's home, plus WSL homes on Windows) and fills the manifest
// envelope with device metadata.
func (s *Scan) collectManifest(ctx context.Context) (types.DeviceScanManifest, error) {
	roots, err := scan.DefaultRoots(ctx)
	if err != nil {
		return types.DeviceScanManifest{}, err
	}
	manifest, err := scan.Scan(ctx, scan.Options{Roots: roots, MaxDepth: s.MaxDepth})
	if err != nil {
		return types.DeviceScanManifest{}, fmt.Errorf("scan: %w", err)
	}

	manifest.ScannerVersion = version.Get().String()
	manifest.ScannedAt = types.Time{Time: time.Now().UTC()}
	manifest.OS = runtime.GOOS
	manifest.Arch = runtime.GOARCH
	manifest.Hostname, _ = os.Hostname()
	if u, err := user.Current(); err == nil {
		manifest.Username = u.Username
	}
	return manifest, nil
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

	// A skill discoverable by several clients appears once per client
	// in manifest.Skills; the total counts each skill once, while the
	// per-client column counts every skill the client can discover.
	var (
		distinctSkills     = make(map[string]struct{})
		unattributedSkills = make(map[string]struct{})
		skillCounts        = make(map[string]int)
	)
	for _, skill := range manifest.Skills {
		distinctSkills[skill.File] = struct{}{}
		if skill.Client == "" || skill.Client == scan.MultiClient {
			unattributedSkills[skill.File] = struct{}{}
			continue
		}
		skillCounts[skill.Client]++
	}

	_, _ = fmt.Fprintf(out, "Found:     %d clients, %d MCP servers, %d skills, %d plugins, %d files\n\n",
		len(manifest.Clients), len(manifest.MCPServers), len(distinctSkills), len(manifest.Plugins), len(manifest.Files))

	if len(manifest.Clients) == 0 {
		_, _ = fmt.Fprintln(out, "No clients found")
		return nil
	}

	var (
		mcpCounts    = make(map[string]int)
		pluginCounts = make(map[string]int)
	)
	for _, server := range manifest.MCPServers {
		mcpCounts[server.Client]++
	}
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
	if err := w.Flush(); err != nil {
		return err
	}

	if n := len(unattributedSkills); n > 0 {
		_, _ = fmt.Fprintf(out, "\n%d skills found outside any client's discovery paths\n", n)
	}
	return nil
}

// tableCell renders an empty value as a placeholder dash.
func tableCell(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
