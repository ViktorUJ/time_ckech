// Package logger wraps Windows Event Log and full logging functionality.
package logger

import "fmt"

// Event IDs for Windows Event Log entries.
const (
	EventIDAppStart     uint32 = 1000
	EventIDAppStop      uint32 = 1001
	EventIDSiteVisit    uint32 = 1002
	EventIDServiceStart uint32 = 1003
	EventIDServiceStop  uint32 = 1004
	EventIDWarning      uint32 = 1005
	EventIDBlock        uint32 = 1006
)

// Logger writes events to the Windows Event Log and optionally to a full log file.
type Logger struct {
	eventLog    EventLogWriter
	fullLog     *FullLogWriter
	fullEnabled bool
}

// NewLogger creates a Logger. If fullLog is nil or fullEnabled is false,
// full logging is skipped.
func NewLogger(eventLog EventLogWriter, fullLog *FullLogWriter, fullEnabled bool) *Logger {
	return &Logger{
		eventLog:    eventLog,
		fullLog:     fullLog,
		fullEnabled: fullEnabled,
	}
}

// SetFullLogging toggles full logging on or off at runtime.
func (l *Logger) SetFullLogging(enabled bool) {
	l.fullEnabled = enabled
}

// LogEvent writes the entry to the Windows Event Log (always) and to the
// full log file (when full logging is enabled).
func (l *Logger) LogEvent(entry LogEntry) error {
	// Build a human-readable message for the Event Log.
	msg := formatEventMessage(entry)

	// Choose Event Log level and ID based on event type.
	eventID, isWarning := eventIDForType(entry.EventType)

	if isWarning {
		_ = l.eventLog.Warning(eventID, msg)
	} else {
		_ = l.eventLog.Info(eventID, msg)
	}

	// Write to full log if enabled (independently of Event Log success).
	if l.fullEnabled && l.fullLog != nil {
		if err := l.fullLog.Write(entry); err != nil {
			return fmt.Errorf("full log write: %w", err)
		}
	}

	return nil
}

// Close releases resources held by the logger.
func (l *Logger) Close() error {
	var firstErr error
	if l.eventLog != nil {
		if err := l.eventLog.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if l.fullLog != nil {
		if err := l.fullLog.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// eventIDForType maps an event type string to a numeric Event ID and whether
// it should be logged at Warning level.
func eventIDForType(eventType string) (uint32, bool) {
	switch eventType {
	case EventWarning:
		return EventIDWarning, true
	case EventBlock:
		return EventIDBlock, true
	case EventAppStart:
		return EventIDAppStart, false
	case EventAppStop:
		return EventIDAppStop, false
	case EventSiteVisit:
		return EventIDSiteVisit, false
	case EventServiceStart:
		return EventIDServiceStart, false
	case EventServiceStop:
		return EventIDServiceStop, false
	default:
		return EventIDWarning, false
	}
}

// formatEventMessage builds a concise string for the Windows Event Log.
// Includes timestamp for readability.
func formatEventMessage(e LogEntry) string {
	ts := e.Timestamp.Format("2006-01-02 15:04:05")
	switch e.EventType {
	case EventAppStart:
		return fmt.Sprintf("[%s] App started: %s (%s) user=%s", ts, e.ProcessName, e.ExePath, e.User)
	case EventAppStop:
		return fmt.Sprintf("[%s] App stopped: %s (%s) duration=%ds", ts, e.ProcessName, e.ExePath, e.Duration)
	case EventSiteVisit:
		return fmt.Sprintf("[%s] Site visit: %s browser=%s", ts, e.URL, e.Browser)
	case EventServiceStart:
		return fmt.Sprintf("[%s] ParentalControlService started", ts)
	case EventServiceStop:
		return fmt.Sprintf("[%s] ParentalControlService stopped", ts)
	case EventWarning:
		return fmt.Sprintf("[%s] Warning: %s", ts, e.Message)
	case EventBlock:
		return fmt.Sprintf("[%s] Blocked: %s", ts, e.Message)
	case EventInfo:
		return fmt.Sprintf("[%s] %s", ts, e.Message)
	default:
		return fmt.Sprintf("[%s] %s", ts, e.Message)
	}
}
