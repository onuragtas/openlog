//go:build windows

package logs

import (
	"context"
	"errors"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
)

var (
	wevtapi                      = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtSubscribe             = wevtapi.NewProc("EvtSubscribe")
	procEvtNext                  = wevtapi.NewProc("EvtNext")
	procEvtRender                = wevtapi.NewProc("EvtRender")
	procEvtClose                 = wevtapi.NewProc("EvtClose")
	procEvtCreateBookmark        = wevtapi.NewProc("EvtCreateBookmark")
	procEvtUpdateBookmark        = wevtapi.NewProc("EvtUpdateBookmark")
	procEvtOpenPublisherMetadata = wevtapi.NewProc("EvtOpenPublisherMetadata")
	procEvtFormatMessage         = wevtapi.NewProc("EvtFormatMessage")
)

const (
	evtSubscribeToFutureEvents      = 1
	evtSubscribeStartAtOldestRecord = 2
	evtSubscribeStartAfterBookmark  = 3
	evtRenderEventXML               = 1
	evtRenderBookmark               = 2
	evtFormatMessageXML             = 9
	eventBatch                      = 64
)

type evtHandle uintptr

func evtClose(h evtHandle) {
	if h != 0 {
		procEvtClose.Call(uintptr(h))
	}
}

// runEventLog subscribes to every configured channel until ctx ends.
func (m *Manager) runEventLog(ctx context.Context, out chan<- journalEntry) {
	var wg sync.WaitGroup
	for _, ch := range m.cfg.WindowsEventLog.Channels {
		wg.Go(func() { m.runEventChannel(ctx, out, ch) })
	}
	wg.Wait()
}

func (m *Manager) runEventChannel(ctx context.Context, out chan<- journalEntry, ch config.EventLogChannel) {
	backoff := time.Second
	for ctx.Err() == nil {
		n, err := m.eventChannelOnce(ctx, out, ch)
		if ctx.Err() != nil {
			return
		}
		if n > 0 {
			backoff = time.Second
		}
		m.log.Warn("event log subscription ended; retrying", "channel", ch.Name, "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (m *Manager) eventChannelOnce(ctx context.Context, out chan<- journalEntry, ch config.EventLogChannel) (int, error) {
	if err := wevtapi.Load(); err != nil {
		return 0, err
	}
	key := eventLogCursorKey(ch.Name)
	signal, err := windows.CreateEvent(nil, 1, 1, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(signal)

	cursor := m.persistedEventCursor(key)
	var bookmark evtHandle
	flags := uintptr(evtSubscribeToFutureEvents)
	if m.cfg.StartAt == "beginning" {
		flags = evtSubscribeStartAtOldestRecord
	}
	if cursor != "" {
		p, _ := windows.UTF16PtrFromString(cursor)
		r, _, _ := procEvtCreateBookmark.Call(uintptr(unsafe.Pointer(p)))
		if r != 0 {
			bookmark, flags = evtHandle(r), evtSubscribeStartAfterBookmark
		}
	}
	if bookmark == 0 {
		r, _, err := procEvtCreateBookmark.Call(0)
		if r == 0 {
			return 0, err
		}
		bookmark = evtHandle(r)
	}
	defer evtClose(bookmark)

	chPtr, err := windows.UTF16PtrFromString(ch.Name)
	if err != nil {
		return 0, err
	}
	qPtr, err := windows.UTF16PtrFromString(EventLogQuery(ch))
	if err != nil {
		return 0, err
	}
	bm := uintptr(0)
	if flags == evtSubscribeStartAfterBookmark {
		bm = uintptr(bookmark)
	}
	r, _, err := procEvtSubscribe.Call(0, uintptr(signal), uintptr(unsafe.Pointer(chPtr)), uintptr(unsafe.Pointer(qPtr)), bm, 0, 0, flags)
	if r == 0 {
		return 0, err
	}
	sub := evtHandle(r)
	defer evtClose(sub)
	m.log.Info("reading windows event log", "channel", ch.Name, "query", EventLogQuery(ch), "resume", flags == evtSubscribeStartAfterBookmark)

	publishers := map[string]evtHandle{}
	defer func() {
		for _, h := range publishers {
			evtClose(h)
		}
	}()
	n := 0
	events := make([]evtHandle, eventBatch)
	for ctx.Err() == nil {
		ev, _ := windows.WaitForSingleObject(signal, 1000)
		if ev != windows.WAIT_OBJECT_0 {
			continue
		}
		for ctx.Err() == nil {
			var returned uint32
			r, _, err := procEvtNext.Call(uintptr(sub), eventBatch, uintptr(unsafe.Pointer(&events[0])), 0, 0, uintptr(unsafe.Pointer(&returned)))
			if r == 0 {
				if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
					windows.ResetEvent(signal)
					break
				}
				return n, err
			}
			entries := make([]journalEntry, 0, returned)
			for i := uint32(0); i < returned; i++ {
				h := events[i]
				if xmlText, err := renderXML(h, evtRenderEventXML); err == nil {
					if parsed, err := ParseWindowsEvent([]byte(xmlText)); err == nil {
						if msg := formatMessage(publishers, parsed.System.Provider.Name, h); msg != "" {
							parsed.RenderingInfo.Message = msg
						}
						entries = append(entries, journalEntry{rec: WindowsEventRecord(parsed, ch.Name, m.cfg.MaxLineBytes, m.now(), m.cfg.MaskSecrets)})
					}
				}
				procEvtUpdateBookmark.Call(uintptr(bookmark), uintptr(h))
				evtClose(h)
			}
			if len(entries) == 0 {
				continue
			}
			if bmXML, err := renderXML(bookmark, evtRenderBookmark); err == nil {
				entries[len(entries)-1].cursor, entries[len(entries)-1].cursorKey = bmXML, key
			}
			for _, e := range entries {
				select {
				case out <- e:
					n++
				case <-ctx.Done():
					return n, ctx.Err()
				}
			}
		}
	}
	return n, ctx.Err()
}

// renderXML renders an event (evtRenderEventXML) or a bookmark (evtRenderBookmark) as a string.
func renderXML(h evtHandle, flags uintptr) (string, error) {
	var used, props uint32
	buf := make([]uint16, 4096)
	for attempt := 0; attempt < 2; attempt++ {
		r, _, err := procEvtRender.Call(0, uintptr(h), flags, uintptr(len(buf)*2), uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&props)))
		if r != 0 {
			return windows.UTF16ToString(buf), nil
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || used > 16<<20 {
			return "", err
		}
		buf = make([]uint16, used/2+1)
	}
	return "", windows.ERROR_INSUFFICIENT_BUFFER
}

// formatMessage returns the localized message of an event from its publisher's metadata ("" when unavailable).
func formatMessage(cache map[string]evtHandle, provider string, h evtHandle) string {
	if provider == "" {
		return ""
	}
	pm, ok := cache[provider]
	if !ok {
		p, err := windows.UTF16PtrFromString(provider)
		if err == nil {
			r, _, _ := procEvtOpenPublisherMetadata.Call(0, uintptr(unsafe.Pointer(p)), 0, 0, 0)
			pm = evtHandle(r)
		}
		cache[provider] = pm
	}
	if pm == 0 {
		return ""
	}
	var used uint32
	buf := make([]uint16, 4096)
	for attempt := 0; attempt < 2; attempt++ {
		r, _, err := procEvtFormatMessage.Call(uintptr(pm), uintptr(h), 0, 0, 0, evtFormatMessageXML, uintptr(len(buf)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&used)))
		if r != 0 {
			if ev, err := ParseWindowsEvent([]byte(windows.UTF16ToString(buf))); err == nil {
				return ev.RenderingInfo.Message
			}
			return ""
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || used > 8<<20 {
			return ""
		}
		buf = make([]uint16, used+1)
	}
	return ""
}
