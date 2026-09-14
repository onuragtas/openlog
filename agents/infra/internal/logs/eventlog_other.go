//go:build !windows

package logs

import "context"

// runEventLog is Windows-only; config.Warnings reports logs.windows_event_log elsewhere.
func (m *Manager) runEventLog(context.Context, chan<- journalEntry) {}
