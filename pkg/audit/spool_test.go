package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/obot-platform/obot/apiclient/types"
)

func TestSpoolStoresPlaintextAndDrainsOldestEntries(t *testing.T) {
	dir := t.TempDir()
	spool := &Spool{
		Dir:      dir + "/spool",
		MaxBytes: 50 * 1024 * 1024,
		MaxAge:   time.Hour,
		Now:      time.Now,
	}
	first := []types.LocalAgentToolCallAuditLogInput{validSpoolLog("first")}
	second := []types.LocalAgentToolCallAuditLogInput{validSpoolLog("second")}

	if err := spool.Enqueue(first); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := spool.Enqueue(second); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	files, err := os.ReadDir(spool.Dir)
	if err != nil {
		t.Fatalf("read spool dir: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("spool file count = %d, want 2", len(files))
	}
	var foundPlaintext bool
	for _, file := range files {
		data, err := os.ReadFile(spool.Dir + "/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(`"idempotencyKey": "first"`)) {
			foundPlaintext = true
		}
	}
	if !foundPlaintext {
		t.Fatal("spool files should store audit logs as readable plaintext JSON")
	}

	var drained []string
	count, err := spool.Drain(10, func(logs []types.LocalAgentToolCallAuditLogInput) error {
		drained = append(drained, logs[0].Details.Trace.IdempotencyKey)
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if count != 2 {
		t.Fatalf("drained count = %d, want 2", count)
	}
	if len(drained) != 2 || drained[0] != "first" || drained[1] != "second" {
		t.Fatalf("drained order = %#v, want oldest first", drained)
	}
	files, err = os.ReadDir(spool.Dir)
	if err != nil {
		t.Fatalf("read spool dir after drain: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("spool files remaining = %d, want 0", len(files))
	}
}

func TestSpoolDrainDiscardsClientErrors(t *testing.T) {
	dir := t.TempDir()
	spool := &Spool{Dir: dir + "/spool", MaxBytes: 1024 * 1024, MaxAge: time.Hour}
	if err := spool.Enqueue([]types.LocalAgentToolCallAuditLogInput{validSpoolLog("discard")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	clientErr := errors.New("bad request")
	count, err := spool.Drain(10, func([]types.LocalAgentToolCallAuditLogInput) error {
		return clientErr
	}, func(err error) bool {
		return errors.Is(err, clientErr)
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if count != 1 {
		t.Fatalf("drained count = %d, want discarded file counted", count)
	}
	files, err := os.ReadDir(spool.Dir)
	if err != nil {
		t.Fatalf("read spool dir: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("spool files remaining = %d, want 0", len(files))
	}
}

func TestSpoolDrainDiscardsUnreadableFilesAndContinues(t *testing.T) {
	dir := t.TempDir()
	spool := &Spool{Dir: dir + "/spool", MaxBytes: 1024 * 1024, MaxAge: time.Hour}
	if err := spool.Enqueue([]types.LocalAgentToolCallAuditLogInput{validSpoolLog("valid")}); err != nil {
		t.Fatalf("enqueue valid batch: %v", err)
	}

	corruptPath := spool.Dir + "/00000000000000000000-corrupt.json"
	if err := os.WriteFile(corruptPath, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write corrupt spool file: %v", err)
	}

	var submitted []string
	count, err := spool.Drain(10, func(logs []types.LocalAgentToolCallAuditLogInput) error {
		submitted = append(submitted, logs[0].Details.Trace.IdempotencyKey)
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if count != 2 {
		t.Fatalf("drained count = %d, want corrupt and valid files counted", count)
	}
	if len(submitted) != 1 || submitted[0] != "valid" {
		t.Fatalf("submitted batches = %v, want only the valid batch", submitted)
	}
	files, err := os.ReadDir(spool.Dir)
	if err != nil {
		t.Fatalf("read spool dir: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("spool files remaining = %d, want 0", len(files))
	}
}

func TestSpoolEvictsFilesOlderThanMaxAge(t *testing.T) {
	dir := t.TempDir()
	spool := &Spool{Dir: dir + "/spool", MaxBytes: 0, MaxAge: time.Minute}
	if err := spool.Enqueue([]types.LocalAgentToolCallAuditLogInput{validSpoolLog("aged")}); err != nil {
		t.Fatalf("enqueue aged: %v", err)
	}

	// Backdate the first file's mtime well past MaxAge, then enqueue a fresh
	// file. enforceLimits (run by Enqueue) should evict only the aged file.
	aged, err := spool.oldestFiles()
	if err != nil || len(aged) != 1 {
		t.Fatalf("expected 1 seeded file, got %d (err=%v)", len(aged), err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(aged[0], past, past); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if err := spool.Enqueue([]types.LocalAgentToolCallAuditLogInput{validSpoolLog("fresh")}); err != nil {
		t.Fatalf("enqueue fresh: %v", err)
	}

	files, err := spool.oldestFiles()
	if err != nil {
		t.Fatalf("oldest files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected the aged-out file to be evicted leaving 1, got %d", len(files))
	}
	logs, err := readSpoolFile(files[0])
	if err != nil {
		t.Fatalf("read remaining file: %v", err)
	}
	if logs[0].Details.Trace.IdempotencyKey != "fresh" {
		t.Fatalf("expected the newest file to survive age eviction, got %q", logs[0].Details.Trace.IdempotencyKey)
	}
}

func TestSpoolEvictsOldestWhenOverMaxBytes(t *testing.T) {
	dir := t.TempDir()
	// MaxBytes disabled while seeding so all three files persist. Equal-length
	// idempotency keys keep the spooled files the same size.
	spool := &Spool{Dir: dir + "/spool", MaxBytes: 0, MaxAge: time.Hour}
	for _, id := range []string{"id1", "id2", "id3"} {
		if err := spool.Enqueue([]types.LocalAgentToolCallAuditLogInput{validSpoolLog(id)}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	files, err := spool.oldestFiles()
	if err != nil || len(files) != 3 {
		t.Fatalf("expected 3 seeded files, got %d (err=%v)", len(files), err)
	}
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Cap at a single file's worth so only the newest survives, oldest first.
	spool.MaxBytes = info.Size()
	if err := spool.enforceLimits(); err != nil {
		t.Fatalf("enforce limits: %v", err)
	}
	remaining, err := spool.oldestFiles()
	if err != nil {
		t.Fatalf("oldest files: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected size eviction to keep 1 file, got %d", len(remaining))
	}
	logs, err := readSpoolFile(remaining[0])
	if err != nil {
		t.Fatalf("read remaining file: %v", err)
	}
	if logs[0].Details.Trace.IdempotencyKey != "id3" {
		t.Fatalf("expected newest file to survive size eviction, got %q", logs[0].Details.Trace.IdempotencyKey)
	}
}

func TestSpoolReplayPreservesPerEntryIdempotencyKeys(t *testing.T) {
	dir := t.TempDir()
	spool := &Spool{Dir: dir + "/spool", MaxBytes: 1024 * 1024, MaxAge: time.Hour}
	// A single spool file holding a small batch of distinct entries.
	if err := spool.Enqueue([]types.LocalAgentToolCallAuditLogInput{validSpoolLog("batch-a"), validSpoolLog("batch-b")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var replayed []string
	count, err := spool.Drain(10, func(logs []types.LocalAgentToolCallAuditLogInput) error {
		for _, l := range logs {
			replayed = append(replayed, l.Details.Trace.IdempotencyKey)
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one batch file drained, got %d", count)
	}
	if len(replayed) != 2 || replayed[0] != "batch-a" || replayed[1] != "batch-b" {
		t.Fatalf("expected per-entry idempotency keys preserved across replay, got %v", replayed)
	}
}

func TestSpoolEnqueueEmptyBatchIsNoOp(t *testing.T) {
	dir := t.TempDir()
	spool := &Spool{Dir: dir + "/spool", MaxBytes: 1024 * 1024, MaxAge: time.Hour}
	if err := spool.Enqueue(nil); err != nil {
		t.Fatalf("enqueue empty: %v", err)
	}
	if _, err := os.Stat(spool.Dir); !os.IsNotExist(err) {
		t.Fatalf("empty enqueue must not create the spool dir (err=%v)", err)
	}
}

func validSpoolLog(id string) types.LocalAgentToolCallAuditLogInput {
	return types.LocalAgentToolCallAuditLogInput{
		// A fixed, whole-second timestamp keeps every seeded log the same
		// serialized size. time.Now() would marshal as RFC3339 with trailing
		// zeros trimmed from the fractional seconds, so logs enqueued nanoseconds
		// apart would differ in length by a few bytes and make the byte-exact
		// size-eviction assertion flaky.
		OccurredAt: *types.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		Action:     types.LocalAgentToolCallAuditLogAction{Name: "Bash", Kind: "shell"},
		Target: types.LocalAgentToolCallAuditLogTarget{
			TargetType: types.AuditLogTargetTypeLocalTool,
			Name:       "Bash",
		},
		Outcome: types.LocalAgentToolCallAuditLogOutcome{Status: types.AuditLogOutcomeStatusSuccess},
		Details: types.LocalAgentToolCallAuditLogReportedDetails{
			Trace: types.LocalAgentToolCallAuditLogTrace{IdempotencyKey: id},
			Agent: types.LocalAgentToolCallAuditLogAgent{
				Provider:   types.LocalAgentProviderCodex,
				CLIVersion: "1.0.0",
			},
			Request:  types.LocalAgentToolCallAuditLogPayload{Body: json.RawMessage(`{"command":"echo hi"}`)},
			Response: types.LocalAgentToolCallAuditLogPayload{Body: json.RawMessage(`{"ok":true}`)},
			RawEvent: json.RawMessage(`{"native":true}`),
		},
	}
}
