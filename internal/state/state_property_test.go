package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Feature: parental-control-service, Property 11: Восстановление состояния после перезагрузки
// **Validates: Requirements 10.1, 10.2**

func TestPropertyStateRestoreAfterReboot(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a base date so all times are in the same day.
		year := rapid.IntRange(2020, 2030).Draw(t, "year")
		month := time.Month(rapid.IntRange(1, 12).Draw(t, "month"))
		day := rapid.IntRange(1, 28).Draw(t, "day")

		// Generate window start hour/minute and ensure window has positive duration.
		startHour := rapid.IntRange(0, 22).Draw(t, "startHour")
		startMin := rapid.IntRange(0, 59).Draw(t, "startMin")
		// Window duration between 1 and remaining minutes in the day (at least 1 minute).
		maxDuration := (23-startHour)*60 + (59 - startMin)
		if maxDuration < 1 {
			maxDuration = 1
		}
		durationMin := rapid.IntRange(1, maxDuration).Draw(t, "durationMin")

		windowStart := time.Date(year, month, day, startHour, startMin, 0, 0, time.UTC)
		windowEnd := windowStart.Add(time.Duration(durationMin) * time.Minute)

		// Generate entertainment seconds (0 to 10 hours).
		entertainmentSeconds := rapid.IntRange(0, 36000).Draw(t, "entertainmentSeconds")

		savedState := &ServiceState{
			EntertainmentSeconds: entertainmentSeconds,
			WindowStart:          windowStart,
			WindowEnd:            windowEnd,
			LastSaveTime:         windowStart.Add(time.Duration(rapid.IntRange(0, durationMin*60).Draw(t, "saveOffset")) * time.Second),
		}

		// Create a temp directory for the state file.
		tmpDir, err := os.MkdirTemp("", "state-property-test-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		statePath := filepath.Join(tmpDir, "state.json")
		sm := NewStateManager(statePath)

		// Save the state to disk.
		if err := sm.Save(savedState); err != nil {
			t.Fatalf("Save failed: %v", err)
		}

		// Generate "now" time: either inside or outside the window.
		insideWindow := rapid.Bool().Draw(t, "insideWindow")

		var now time.Time
		if insideWindow {
			// now in [WindowStart, WindowEnd)
			offsetSec := rapid.IntRange(0, int(windowEnd.Sub(windowStart).Seconds())-1).Draw(t, "insideOffset")
			now = windowStart.Add(time.Duration(offsetSec) * time.Second)
		} else {
			// now outside the window: either before WindowStart or at/after WindowEnd.
			before := rapid.Bool().Draw(t, "beforeWindow")
			if before {
				offsetSec := rapid.IntRange(1, 86400).Draw(t, "beforeOffset")
				now = windowStart.Add(-time.Duration(offsetSec) * time.Second)
			} else {
				offsetSec := rapid.IntRange(0, 86400).Draw(t, "afterOffset")
				now = windowEnd.Add(time.Duration(offsetSec) * time.Second)
			}
		}

		// Call Restore and verify.
		restored := sm.Restore(now)

		if insideWindow {
			// Same window → entertainment seconds must be preserved.
			if restored.EntertainmentSeconds != entertainmentSeconds {
				t.Fatalf(
					"inside window: expected EntertainmentSeconds=%d, got %d (now=%v, window=[%v, %v))",
					entertainmentSeconds, restored.EntertainmentSeconds, now, windowStart, windowEnd,
				)
			}
			if restored.WindowStart.IsZero() || restored.WindowEnd.IsZero() {
				t.Fatalf("inside window: expected non-zero WindowStart/WindowEnd in restored state")
			}
		} else {
			// Different window → counter must be zero.
			if restored.EntertainmentSeconds != 0 {
				t.Fatalf(
					"outside window: expected EntertainmentSeconds=0, got %d (now=%v, window=[%v, %v))",
					restored.EntertainmentSeconds, now, windowStart, windowEnd,
				)
			}
		}
	})
}
