package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obot-platform/obot-sentry/pkg/state"
)

// A scheduled scan that can't start because of its configuration must still
// leave a record in scan.json and the scan log, since nobody sees its stderr.
func TestScanSubmitRecordsConfigError(t *testing.T) {
	root := withoutAuditDeploymentConfig(t)
	cacheDir := filepath.Join(auditCacheDir(t), "obot", "obot-sentry")

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"scan", "--submit", "--quiet"})

	err := root.Execute()
	exitErr, ok := errors.AsType[*ExitCodeError](err)
	if !ok || exitErr.Code != 2 {
		t.Fatalf("expected a config error (exit 2), got %v", err)
	}

	scanState, err := state.LoadScanState(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if scanState.LastStatus != "error" || !strings.Contains(scanState.LastError, "requires a server URL") {
		t.Fatalf("scan.json should record the config error, got %+v", scanState)
	}
	if scanState.LastScanAt == nil {
		t.Error("scan.json should timestamp the failed attempt")
	}
	if scanState.LastSubmitAt != nil {
		t.Error("a config error must not count as a submission")
	}

	logs, err := os.ReadDir(filepath.Join(cacheDir, "scan-logs"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("expected one scan log record, got %d (err=%v)", len(logs), err)
	}
}
