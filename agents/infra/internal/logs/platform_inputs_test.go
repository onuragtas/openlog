package logs

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

func attrMap(rec *logspb.LogRecord) map[string]any {
	m := map[string]any{}
	for _, kv := range rec.Attributes {
		switch v := kv.Value.Value.(type) {
		case *commonpb.AnyValue_StringValue:
			m[kv.Key] = v.StringValue
		case *commonpb.AnyValue_IntValue:
			m[kv.Key] = v.IntValue
		case *commonpb.AnyValue_BoolValue:
			m[kv.Key] = v.BoolValue
		}
	}
	return m
}

func TestUnifiedLogArgs(t *testing.T) {
	got := UnifiedLogArgs(config.UnifiedLogInput{Level: "info", Predicate: `subsystem == "com.example.app"`})
	want := []string{"stream", "--style", "ndjson", "--level", "info", "--predicate", `subsystem == "com.example.app"`}
	if !slices.Equal(got, want) {
		t.Errorf("args = %q", got)
	}
}

func TestUnifiedLogRecord(t *testing.T) {
	obs := time.Unix(1_800_000_000, 0)
	line := `{"traceID":1,"eventMessage":"connection refused password=hunter2","eventType":"logEvent","subsystem":"com.example.app","category":"network",` +
		`"processImagePath":"/Applications/Example.app/Contents/MacOS/Example","processID":4242,"timestamp":"2026-09-14 20:49:41.775325+0300","messageType":"Error"}`
	rec := UnifiedLogRecord([]byte(line), 1024, obs, true)
	if rec == nil {
		t.Fatal("nil record")
	}
	if rec.SeverityNumber != logspb.SeverityNumber_SEVERITY_NUMBER_ERROR || rec.SeverityText != "ERROR" {
		t.Errorf("severity = %v %s", rec.SeverityNumber, rec.SeverityText)
	}
	want := time.Date(2026, 9, 14, 17, 49, 41, 775325000, time.UTC)
	if rec.TimeUnixNano != uint64(want.UnixNano()) {
		t.Errorf("time = %d, want %d", rec.TimeUnixNano, want.UnixNano())
	}
	if body := rec.Body.GetStringValue(); strings.Contains(body, "hunter2") {
		t.Errorf("secret not masked: %q", body)
	}
	a := attrMap(rec)
	if a[AttrSource] != SourceUnifiedLog || a[AttrMacOSSubsystem] != "com.example.app" || a[AttrMacOSCategory] != "network" ||
		a["process.pid"] != int64(4242) || a["process.command"] != "Example" {
		t.Errorf("attributes = %v", a)
	}
	for _, skip := range []string{`Filtering the log data using "subsystem == x"`, `{"eventType":"activityCreateEvent","eventMessage":"x"}`, `{bad json`, ``} {
		if UnifiedLogRecord([]byte(skip), 1024, obs, false) != nil {
			t.Errorf("record for %q", skip)
		}
	}
	fault := UnifiedLogRecord([]byte(`{"eventType":"logEvent","messageType":"Fault","eventMessage":"x"}`), 1024, obs, false)
	if fault.SeverityNumber != logspb.SeverityNumber_SEVERITY_NUMBER_FATAL {
		t.Errorf("fault severity = %v", fault.SeverityNumber)
	}
}

func TestEventLogQuery(t *testing.T) {
	for _, c := range []struct {
		ch   config.EventLogChannel
		want string
	}{
		{config.EventLogChannel{Name: "System"}, "*"},
		{config.EventLogChannel{Name: "System", Levels: []string{"warning", "critical", "error"}}, "*[System[(Level=1 or Level=2 or Level=3)]]"},
		{config.EventLogChannel{Name: "Security", EventIDs: []int{4624, 4625}}, "*[System[(EventID=4624 or EventID=4625)]]"},
		{config.EventLogChannel{Name: "Application", Levels: []string{"information"}, EventIDs: []int{1000}}, "*[System[(Level=0 or Level=4) and (EventID=1000)]]"},
		{config.EventLogChannel{Name: "Setup", Query: "*[System[Provider[@Name='x']]]"}, "*[System[Provider[@Name='x']]]"},
	} {
		if got := EventLogQuery(c.ch); got != c.want {
			t.Errorf("EventLogQuery(%+v) = %q, want %q", c.ch, got, c.want)
		}
	}
}

const serviceEventXML = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Service Control Manager' Guid='{555908d1-a6d7-4695-8e1e-26931d2012f4}' EventSourceName='Service Control Manager'/><EventID Qualifiers='16384'>7036</EventID><Version>0</Version><Level>4</Level><Task>0</Task><Opcode>0</Opcode><Keywords>0x8080000000000000</Keywords><TimeCreated SystemTime='2026-09-14T17:49:41.7753250Z'/><EventRecordID>123456</EventRecordID><Correlation/><Execution ProcessID='708' ThreadID='9100'/><Channel>System</Channel><Computer>WIN-HOST</Computer><Security/></System><EventData><Data Name='param1'>Windows Update</Data><Data Name='param2'>running</Data></EventData><RenderingInfo Culture='en-US'><Message>The Windows Update service entered the running state.</Message><Level>Information</Level></RenderingInfo></Event>`

func TestWindowsEventRecord(t *testing.T) {
	ev, err := ParseWindowsEvent([]byte(serviceEventXML))
	if err != nil {
		t.Fatal(err)
	}
	obs := time.Unix(1_800_000_000, 0)
	rec := WindowsEventRecord(ev, "system", 1024, obs, false)
	if rec.Body.GetStringValue() != "The Windows Update service entered the running state." {
		t.Errorf("body = %q", rec.Body.GetStringValue())
	}
	if rec.SeverityNumber != logspb.SeverityNumber_SEVERITY_NUMBER_INFO {
		t.Errorf("severity = %v", rec.SeverityNumber)
	}
	if want := time.Date(2026, 9, 14, 17, 49, 41, 775325000, time.UTC); rec.TimeUnixNano != uint64(want.UnixNano()) {
		t.Errorf("time = %d", rec.TimeUnixNano)
	}
	a := attrMap(rec)
	if a[AttrSource] != SourceWindowsEventLog || a[AttrWindowsChannel] != "System" || a[AttrWindowsEventID] != int64(7036) ||
		a[AttrWindowsProvider] != "Service Control Manager" || a[AttrWindowsRecordID] != int64(123456) || a["process.pid"] != int64(708) {
		t.Errorf("attributes = %v", a)
	}

	// Without publisher metadata the event data becomes the body.
	ev.RenderingInfo.Message = ""
	ev.System.Level = 2
	rec = WindowsEventRecord(ev, "System", 1024, obs, false)
	if rec.Body.GetStringValue() != "param1=Windows Update param2=running" || rec.SeverityText != "ERROR" {
		t.Errorf("fallback body = %q severity %s", rec.Body.GetStringValue(), rec.SeverityText)
	}
	if _, err := ParseWindowsEvent([]byte("<Event><System>")); err == nil {
		t.Error("truncated XML accepted")
	}
}

func TestEventLogCursorsPersist(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{Config: config.DefaultFor("linux").Logs, StateDir: dir, Emit: func(*logspb.LogsData, int, func()) {}})
	b := newBatch()
	b.cursors[eventLogCursorKey("System")] = "<BookmarkList><Bookmark Channel='System' RecordId='42'/></BookmarkList>"
	m.ack(b)
	if err := m.SaveState(); err != nil {
		t.Fatal(err)
	}
	m2 := New(Options{Config: config.DefaultFor("linux").Logs, StateDir: dir, Emit: func(*logspb.LogsData, int, func()) {}})
	m2.loadState()
	if got := m2.persistedEventCursor("event_log:system"); !strings.Contains(got, "RecordId='42'") {
		t.Errorf("cursor after reload = %q", got)
	}
}
