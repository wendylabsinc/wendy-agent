package commands

import (
	"testing"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

func TestDeviceCmd_HasCrashes(t *testing.T) {
	cmd := newDeviceCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "crashes" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected `device crashes` subcommand to be registered")
	}
}

func TestDeviceLogsCmd_HasPreviousAndSince(t *testing.T) {
	cmd := newDeviceLogsCmd()
	for _, name := range []string{"previous", "since"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("expected `device logs --%s` flag", name)
		}
	}
}

// exitScopeLogs builds a ResourceLogs carrying a single exit event at ts.
func exitScopeLogs(ts uint64, body string) *otelpb.ResourceLogs {
	return &otelpb.ResourceLogs{
		ScopeLogs: []*otelpb.ScopeLogs{
			{
				Scope:      &otelpb.InstrumentationScope{Name: containerExitScope},
				LogRecords: []*otelpb.LogRecord{{TimeUnixNano: ts, Body: strBody(body)}},
			},
		},
	}
}

func appScopeLogs(ts uint64, body string) *otelpb.ResourceLogs {
	return &otelpb.ResourceLogs{
		ScopeLogs: []*otelpb.ScopeLogs{
			{
				Scope:      &otelpb.InstrumentationScope{Name: "wendy.container"},
				LogRecords: []*otelpb.LogRecord{{TimeUnixNano: ts, Body: strBody(body)}},
			},
		},
	}
}

func TestPreviousRunLogs_CutsAtLastExit(t *testing.T) {
	history := []*otelpb.ResourceLogs{
		appScopeLogs(10, "old run line"),
		exitScopeLogs(20, "crashed"),    // previous run ends here
		appScopeLogs(30, "current run"), // belongs to the current run
	}
	prev := previousRunLogs(history)
	if len(prev) != 2 {
		t.Fatalf("expected previous run to include 2 entries (up to last exit), got %d", len(prev))
	}
	// The last kept entry must be the exit boundary.
	if prev[len(prev)-1].GetScopeLogs()[0].GetScope().GetName() != containerExitScope {
		t.Error("previous run should end at the exit event")
	}
}

func TestPreviousRunLogs_NoExitReturnsAll(t *testing.T) {
	history := []*otelpb.ResourceLogs{appScopeLogs(10, "a"), appScopeLogs(20, "b")}
	if got := previousRunLogs(history); len(got) != 2 {
		t.Errorf("with no exit boundary, expected all %d entries, got %d", 2, len(got))
	}
}

func TestFilterResourceLogsAfter_DropsOldRecords(t *testing.T) {
	logs := &otelpb.ExportLogsServiceRequest{
		ResourceLogs: []*otelpb.ResourceLogs{
			{
				ScopeLogs: []*otelpb.ScopeLogs{
					{LogRecords: []*otelpb.LogRecord{
						{TimeUnixNano: 100, Body: strBody("old")},
						{TimeUnixNano: 200, Body: strBody("new")},
					}},
				},
			},
		},
	}
	out := filterResourceLogsAfter(logs, 150)
	if out == nil {
		t.Fatal("expected at least one record to survive the cutoff")
	}
	recs := out.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()
	if len(recs) != 1 || recs[0].GetBody().GetStringValue() != "new" {
		t.Errorf("expected only the newer record, got %d records", len(recs))
	}
	if filterResourceLogsAfter(logs, 1000) != nil {
		t.Error("expected nil when all records are older than the cutoff")
	}
}

func TestCrashRowFromRecord_ParsesAttrs(t *testing.T) {
	lr := &otelpb.LogRecord{
		TimeUnixNano: 42,
		Body:         strBody("container \"x\" crashed with exit code 1"),
		Attributes: []*otelpb.KeyValue{
			{Key: containerExitAttrCode, Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_IntValue{IntValue: 1}}},
			{Key: containerExitAttrCrash, Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_BoolValue{BoolValue: true}}},
		},
	}
	row := crashRowFromRecord("x", lr)
	if row.app != "x" || !row.crash || !row.hasCode || row.exitCode != 1 || row.whenNano != 42 {
		t.Errorf("unexpected row: %+v", row)
	}
}
