package logs

// Windows Event Log input (logs.windows_event_log, D-104): platform-independent parts (query, XML parsing, record
// conversion). The subscription itself is in eventlog_windows.go.

import (
	"encoding/xml"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// EventLogQuery builds the XPath query of a channel: `*` or `*[System[(Level=1 or Level=2) and (EventID=7036)]]`.
func EventLogQuery(ch config.EventLogChannel) string {
	if ch.Query != "" {
		return ch.Query
	}
	var parts []string
	if len(ch.Levels) > 0 {
		nums := make([]int, 0, len(ch.Levels))
		for _, l := range ch.Levels {
			nums = append(nums, config.EventLogLevels[l])
			if l == "information" {
				nums = append(nums, 0) // LogAlways (e.g. Security audit events) renders as Information
			}
		}
		sort.Ints(nums)
		var or []string
		for i, n := range nums {
			if i == 0 || n != nums[i-1] {
				or = append(or, "Level="+strconv.Itoa(n))
			}
		}
		parts = append(parts, "("+strings.Join(or, " or ")+")")
	}
	if len(ch.EventIDs) > 0 {
		var or []string
		for _, id := range ch.EventIDs {
			or = append(or, "EventID="+strconv.Itoa(id))
		}
		parts = append(parts, "("+strings.Join(or, " or ")+")")
	}
	if len(parts) == 0 {
		return "*"
	}
	return "*[System[" + strings.Join(parts, " and ") + "]]"
}

// WindowsEvent is the part of a rendered event the agent uses.
type WindowsEvent struct {
	System struct {
		Provider struct {
			Name string `xml:"Name,attr"`
		} `xml:"Provider"`
		EventID     int    `xml:"EventID"`
		Level       int    `xml:"Level"`
		EventRecord uint64 `xml:"EventRecordID"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
		Execution struct {
			ProcessID int64 `xml:"ProcessID,attr"`
		} `xml:"Execution"`
		Channel  string `xml:"Channel"`
		Computer string `xml:"Computer"`
	} `xml:"System"`
	EventData struct {
		Data []struct {
			Name  string `xml:"Name,attr"`
			Value string `xml:",chardata"`
		} `xml:"Data"`
	} `xml:"EventData"`
	UserData struct {
		Inner string `xml:",innerxml"`
	} `xml:"UserData"`
	RenderingInfo struct {
		Message string `xml:"Message"`
	} `xml:"RenderingInfo"`
}

// ParseWindowsEvent parses event XML (EvtRender / EvtFormatMessage XML).
func ParseWindowsEvent(data []byte) (*WindowsEvent, error) {
	var ev WindowsEvent
	if err := xml.Unmarshal(data, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

var eventLevelSeverity = map[int]struct {
	num  logspb.SeverityNumber
	text string
}{
	1: {logspb.SeverityNumber_SEVERITY_NUMBER_FATAL, "FATAL"},
	2: {logspb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR"},
	3: {logspb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN"},
	4: {logspb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"},
	0: {logspb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"},
	5: {logspb.SeverityNumber_SEVERITY_NUMBER_DEBUG, "DEBUG"},
}

// WindowsEventRecord converts an event to a log record. The body is the rendered message; without one (no
// publisher metadata) the event data as "name=value" pairs.
func WindowsEventRecord(ev *WindowsEvent, channel string, maxBytes int, observed time.Time, maskSecrets bool) *logspb.LogRecord {
	rec := &logspb.LogRecord{ObservedTimeUnixNano: uint64(observed.UnixNano())}
	if t, err := time.Parse(time.RFC3339Nano, ev.System.TimeCreated.SystemTime); err == nil {
		rec.TimeUnixNano = uint64(t.UnixNano())
	}
	if s, ok := eventLevelSeverity[ev.System.Level]; ok {
		rec.SeverityNumber, rec.SeverityText = s.num, s.text
	}
	msg := strings.TrimSpace(ev.RenderingInfo.Message)
	if msg == "" {
		var pairs []string
		for i, d := range ev.EventData.Data {
			name := d.Name
			if name == "" {
				name = "param" + strconv.Itoa(i+1)
			}
			pairs = append(pairs, name+"="+strings.TrimSpace(d.Value))
		}
		msg = strings.Join(pairs, " ")
		if msg == "" {
			msg = strings.TrimSpace(ev.UserData.Inner)
		}
	}
	body, truncated := sanitize(msg, maxBytes, maskSecrets)
	rec.Body = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}}
	if ev.System.Channel != "" {
		channel = ev.System.Channel
	}
	attrs := []*commonpb.KeyValue{
		otlputil.Str(AttrSource, SourceWindowsEventLog),
		otlputil.Str(AttrWindowsChannel, channel),
		otlputil.Int(AttrWindowsEventID, int64(ev.System.EventID)),
	}
	if ev.System.Provider.Name != "" {
		attrs = append(attrs, otlputil.Str(AttrWindowsProvider, ev.System.Provider.Name))
	}
	if ev.System.EventRecord > 0 {
		attrs = append(attrs, otlputil.Int(AttrWindowsRecordID, int64(ev.System.EventRecord)))
	}
	if ev.System.Execution.ProcessID > 0 {
		attrs = append(attrs, otlputil.Int("process.pid", ev.System.Execution.ProcessID))
	}
	if truncated {
		attrs = append(attrs, otlputil.Bool(AttrTruncated, true))
	}
	rec.Attributes = attrs
	return rec
}

// eventLogCursorKey is the log state key of a channel's bookmark.
func eventLogCursorKey(channel string) string { return "event_log:" + strings.ToLower(channel) }
