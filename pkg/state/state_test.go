package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanState_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Missing file reads as the zero state.
	got, err := LoadScanState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != (ScanState{}) {
		t.Errorf("missing file should load zero state, got %+v", got)
	}

	now := time.Now().UTC().Truncate(time.Second)
	want := ScanState{
		LastScanAt:     &now,
		LastSubmitAt:   &now,
		LastStatus:     "ok",
		DeviceID:       "dev-1",
		ScannerVersion: "v1.2.3",
	}
	// Save creates a missing directory.
	nested := filepath.Join(dir, "cache")
	if err := want.Save(nested); err != nil {
		t.Fatal(err)
	}
	got, err = LoadScanState(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastSubmitAt.Equal(*want.LastSubmitAt) || got.LastStatus != want.LastStatus || got.DeviceID != want.DeviceID {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, want)
	}
}

// A corrupt scan.json shouldn't brick the agent; it reads as unenrolled
// state and throttling simply allows the next scan.
func TestLoadScanState_Corrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, scanStateFile), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadScanState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != (ScanState{}) {
		t.Errorf("corrupt file should load zero state, got %+v", got)
	}
}

func TestScanState_SubmittedWithin(t *testing.T) {
	now := time.Now().UTC()
	interval := time.Hour

	if (ScanState{}).SubmittedWithin(interval, now) {
		t.Error("zero state should never be within the interval")
	}

	fresh := now.Add(-30 * time.Minute)
	if !(ScanState{LastSubmitAt: &fresh}).SubmittedWithin(interval, now) {
		t.Error("submission 30m ago should be within a 1h interval")
	}

	stale := now.Add(-2 * time.Hour)
	if (ScanState{LastSubmitAt: &stale}).SubmittedWithin(interval, now) {
		t.Error("submission 2h ago should not be within a 1h interval")
	}

	boundary := now.Add(-interval)
	if (ScanState{LastSubmitAt: &boundary}).SubmittedWithin(interval, now) {
		t.Error("submission exactly one interval ago should be due")
	}
}

func TestAppendScanLog_WritesSortableRecords(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC()

	for i, outcome := range []string{"submitted", "skipped", "error"} {
		record := ScanLogRecord{
			StartedAt:  base.Add(time.Duration(i) * time.Minute),
			FinishedAt: base.Add(time.Duration(i)*time.Minute + time.Second),
			Outcome:    outcome,
		}
		if outcome == "error" {
			record.Error = "boom"
		}
		if err := AppendScanLog(dir, record); err != nil {
			t.Fatal(err)
		}
	}

	names := scanLogFileNames(t, dir)
	if len(names) != 3 {
		t.Fatalf("want 3 records, got %v", names)
	}
	// Lexically sorted names must read chronologically: the first file
	// holds the first outcome.
	var first ScanLogRecord
	readScanLogRecord(t, filepath.Join(dir, scanLogDirName, names[0]), &first)
	if first.Outcome != "submitted" {
		t.Errorf("oldest record outcome = %q, want submitted", first.Outcome)
	}
	var last ScanLogRecord
	readScanLogRecord(t, filepath.Join(dir, scanLogDirName, names[2]), &last)
	if last.Outcome != "error" || last.Error != "boom" {
		t.Errorf("newest record = %+v, want error/boom", last)
	}
}

// Age pruning: records older than the cap disappear on the next write.
func TestAppendScanLog_PrunesByAge(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, scanLogDirName)

	old := ScanLogRecord{StartedAt: time.Now().UTC().Add(-30 * 24 * time.Hour), Outcome: "submitted"}
	if err := AppendScanLog(dir, old); err != nil {
		t.Fatal(err)
	}
	// Retention prunes on ModTime; backdate the file to match its record.
	names := scanLogFileNames(t, dir)
	expired := time.Now().Add(-scanLogMaxAge - time.Hour)
	if err := os.Chtimes(filepath.Join(logDir, names[0]), expired, expired); err != nil {
		t.Fatal(err)
	}

	if err := AppendScanLog(dir, ScanLogRecord{StartedAt: time.Now().UTC(), Outcome: "submitted"}); err != nil {
		t.Fatal(err)
	}
	names = scanLogFileNames(t, dir)
	if len(names) != 1 {
		t.Fatalf("expired record should be pruned, got %v", names)
	}
}

// Size pruning: oldest records go first once the directory exceeds the
// byte cap.
func TestPruneScanLogs_BySize(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, scanLogDirName)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Three 6 MiB files against the 10 MiB cap: the oldest must go.
	payload := make([]byte, 6*1024*1024)
	for _, name := range []string{"00000000000000000001-aa.json", "00000000000000000002-bb.json", "00000000000000000003-cc.json"} {
		if err := os.WriteFile(filepath.Join(logDir, name), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneScanLogs(logDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	names := scanLogFileNames(t, dir)
	if len(names) != 1 || names[0] != "00000000000000000003-cc.json" {
		t.Errorf("size pruning should keep only the newest record, got %v", names)
	}
}

func scanLogFileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, scanLogDirName))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	return names
}

func readScanLogRecord(t *testing.T, path string, into *ScanLogRecord) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatal(err)
	}
}
