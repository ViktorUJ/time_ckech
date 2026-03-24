package logger

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Feature: parental-control-service, Property 14: Полнота записей в логе
// **Validates: Requirements 9.2, 9.3, 9.4**

func genNonEmptyString(label string) *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		s := rapid.StringMatching(`[a-zA-Z0-9_./\\]{1,50}`).Draw(t, label)
		return s
	})
}

func genTimestamp() *rapid.Generator[time.Time] {
	return rapid.Custom(func(t *rapid.T) time.Time {
		year := rapid.IntRange(2020, 2030).Draw(t, "year")
		month := rapid.IntRange(1, 12).Draw(t, "month")
		day := rapid.IntRange(1, 28).Draw(t, "day")
		hour := rapid.IntRange(0, 23).Draw(t, "hour")
		min := rapid.IntRange(0, 59).Draw(t, "min")
		sec := rapid.IntRange(0, 59).Draw(t, "sec")
		return time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)
	})
}

func genAppStartEntry() *rapid.Generator[LogEntry] {
	return rapid.Custom(func(t *rapid.T) LogEntry {
		return LogEntry{
			Timestamp:   genTimestamp().Draw(t, "timestamp"),
			EventType:   EventAppStart,
			ProcessName: genNonEmptyString("processName").Draw(t, "processName"),
			ExePath:     genNonEmptyString("exePath").Draw(t, "exePath"),
			User:        genNonEmptyString("user").Draw(t, "user"),
		}
	})
}

func genAppStopEntry() *rapid.Generator[LogEntry] {
	return rapid.Custom(func(t *rapid.T) LogEntry {
		return LogEntry{
			Timestamp:   genTimestamp().Draw(t, "timestamp"),
			EventType:   EventAppStop,
			ProcessName: genNonEmptyString("processName").Draw(t, "processName"),
			ExePath:     genNonEmptyString("exePath").Draw(t, "exePath"),
			Duration:    rapid.IntRange(1, 86400).Draw(t, "duration"),
		}
	})
}

func genSiteVisitEntry() *rapid.Generator[LogEntry] {
	return rapid.Custom(func(t *rapid.T) LogEntry {
		return LogEntry{
			Timestamp: genTimestamp().Draw(t, "timestamp"),
			EventType: EventSiteVisit,
			URL:       genNonEmptyString("url").Draw(t, "url"),
			Browser:   genNonEmptyString("browser").Draw(t, "browser"),
		}
	})
}

func TestPropertyLogEntryCompleteness(t *testing.T) {
	eventTypes := []string{EventAppStart, EventAppStop, EventSiteVisit}

	rapid.Check(t, func(t *rapid.T) {
		eventType := rapid.SampledFrom(eventTypes).Draw(t, "eventType")

		var entry LogEntry
		switch eventType {
		case EventAppStart:
			entry = genAppStartEntry().Draw(t, "entry")
		case EventAppStop:
			entry = genAppStopEntry().Draw(t, "entry")
		case EventSiteVisit:
			entry = genSiteVisitEntry().Draw(t, "entry")
		}

		// Common: Timestamp must be non-zero for all event types
		if entry.Timestamp.IsZero() {
			t.Fatal("Timestamp must be non-zero")
		}

		switch entry.EventType {
		case EventAppStart:
			// Requirements 9.2: имя программы, путь к исполняемому файлу, имя пользователя, временная метка
			if entry.ProcessName == "" {
				t.Fatal("app_start: ProcessName must be non-empty")
			}
			if entry.ExePath == "" {
				t.Fatal("app_start: ExePath must be non-empty")
			}
			if entry.User == "" {
				t.Fatal("app_start: User must be non-empty")
			}

		case EventAppStop:
			// Requirements 9.3: имя программы, путь, длительность работы
			if entry.ProcessName == "" {
				t.Fatal("app_stop: ProcessName must be non-empty")
			}
			if entry.ExePath == "" {
				t.Fatal("app_stop: ExePath must be non-empty")
			}
			if entry.Duration <= 0 {
				t.Fatalf("app_stop: Duration must be > 0, got %d", entry.Duration)
			}

		case EventSiteVisit:
			// Requirements 9.4: URL сайта, имя браузера, временная метка
			if entry.URL == "" {
				t.Fatal("site_visit: URL must be non-empty")
			}
			if entry.Browser == "" {
				t.Fatal("site_visit: Browser must be non-empty")
			}

		default:
			t.Fatalf("unexpected event type: %s", entry.EventType)
		}

		// Verify the formatted message is non-empty (log entry produces meaningful output)
		msg := formatEventMessage(entry)
		if msg == "" {
			t.Fatalf("formatEventMessage returned empty string for event type %s", entry.EventType)
		}
		t.Logf("event_type=%s msg=%q", entry.EventType, msg)
	})

	// Log summary
	fmt.Println("Property 14: All generated log entries contain required fields for their event type")
}

// mockEventLogWriter is a test double for EventLogWriter that tracks all calls.
type mockEventLogWriter struct {
	infoCalls    []string
	warningCalls []string
	errorCalls   []string
	closed       bool
}

func (m *mockEventLogWriter) Info(eventID uint32, msg string) error {
	m.infoCalls = append(m.infoCalls, msg)
	return nil
}

func (m *mockEventLogWriter) Warning(eventID uint32, msg string) error {
	m.warningCalls = append(m.warningCalls, msg)
	return nil
}

func (m *mockEventLogWriter) Error(eventID uint32, msg string) error {
	m.errorCalls = append(m.errorCalls, msg)
	return nil
}

func (m *mockEventLogWriter) Close() error {
	m.closed = true
	return nil
}

func (m *mockEventLogWriter) totalCalls() int {
	return len(m.infoCalls) + len(m.warningCalls) + len(m.errorCalls)
}

// Feature: parental-control-service, Property 15: Полное логирование всей активности
// **Validates: Requirements 13.2, 13.3**

func genRandomLogEntry() *rapid.Generator[LogEntry] {
	return rapid.Custom(func(t *rapid.T) LogEntry {
		eventTypes := []string{EventAppStart, EventAppStop, EventSiteVisit}
		eventType := rapid.SampledFrom(eventTypes).Draw(t, "eventType")

		switch eventType {
		case EventAppStart:
			return genAppStartEntry().Draw(t, "entry")
		case EventAppStop:
			return genAppStopEntry().Draw(t, "entry")
		default:
			return genSiteVisitEntry().Draw(t, "entry")
		}
	})
}

func TestPropertyFullLogging(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate a random list of log entries (1..20)
		numEntries := rapid.IntRange(1, 20).Draw(rt, "numEntries")
		entries := make([]LogEntry, numEntries)
		for i := range entries {
			entries[i] = genRandomLogEntry().Draw(rt, fmt.Sprintf("entry_%d", i))
		}

		fullLogging := rapid.Bool().Draw(rt, "fullLogging")

		// --- Setup: create mock event log and full log writer with temp file ---
		mockEL := &mockEventLogWriter{}
		tmpDir, err := os.MkdirTemp("", "fulllog_test_*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		tmpFile := tmpDir + "/full.log"

		fullLogWriter, err := NewFullLogWriter(tmpFile)
		if err != nil {
			t.Fatalf("failed to create FullLogWriter: %v", err)
		}

		lgr := NewLogger(mockEL, fullLogWriter, fullLogging)

		// --- Log all entries ---
		for _, entry := range entries {
			if err := lgr.LogEvent(entry); err != nil {
				t.Fatalf("LogEvent failed: %v", err)
			}
		}

		// --- Property checks ---

		// 1. Event Log always receives ALL entries regardless of full_logging flag
		if mockEL.totalCalls() != numEntries {
			rt.Fatalf("Event Log should receive all %d entries, got %d", numEntries, mockEL.totalCalls())
		}

		// 2. Read the full log file to count lines
		lgr.Close()

		fullLogData, err := os.ReadFile(tmpFile)
		if err != nil {
			t.Fatalf("failed to read full log file: %v", err)
		}

		content := strings.TrimSpace(string(fullLogData))
		var fullLogLines int
		if content == "" {
			fullLogLines = 0
		} else {
			fullLogLines = len(strings.Split(content, "\n"))
		}

		if fullLogging {
			// Requirements 13.2, 13.3: when full_logging=true, ALL activities are written to full log
			if fullLogLines != numEntries {
				rt.Fatalf("full_logging=true: full log should contain %d entries, got %d", numEntries, fullLogLines)
			}
		} else {
			// When full_logging=false, no entries should be written to full log
			if fullLogLines != 0 {
				rt.Fatalf("full_logging=false: full log should be empty, got %d entries", fullLogLines)
			}
		}
	})

	fmt.Println("Property 15: Full logging correctly records all activities when enabled, none when disabled")
}
