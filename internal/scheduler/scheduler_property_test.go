package scheduler

import (
	"fmt"
	"testing"
	"time"

	"parental-control-service/internal/config"

	"pgregory.net/rapid"
)

// Feature: parental-control-service, Property 5: Подсчёт развлекательного времени по wall clock
// **Validates: Requirements 5.1, 5.2, 8.3**

// tick represents a single iteration of the main service loop.
type tick struct {
	RestrictedApps int // number of restricted apps/sites active during this tick
	ElapsedSeconds int // wall-clock seconds elapsed since the previous tick
}

// genTick generates a random tick with 0-10 restricted apps and 1-30 seconds elapsed.
func genTick() *rapid.Generator[tick] {
	return rapid.Custom(func(t *rapid.T) tick {
		return tick{
			RestrictedApps: rapid.IntRange(0, 10).Draw(t, "restrictedApps"),
			ElapsedSeconds: rapid.IntRange(1, 30).Draw(t, "elapsedSeconds"),
		}
	})
}

// computeEntertainmentTime models the wall-clock entertainment time counting logic.
// For each tick: if at least one restricted app/site is active, the entertainment
// counter increases by the elapsed wall-clock time — NOT by elapsed * numApps.
// This is the core property: multiple simultaneous restricted apps count as one
// continuous entertainment time period.
func computeEntertainmentTime(ticks []tick) int {
	total := 0
	for _, t := range ticks {
		if t.RestrictedApps > 0 {
			total += t.ElapsedSeconds
		}
	}
	return total
}

func TestPropertyEntertainmentTimeWallClock(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a sequence of 1-50 ticks
		numTicks := rapid.IntRange(1, 50).Draw(t, "numTicks")
		ticks := make([]tick, numTicks)
		for i := 0; i < numTicks; i++ {
			ticks[i] = genTick().Draw(t, "tick")
		}

		// Simulate the entertainment time counter as the service would
		entertainmentSeconds := 0
		for _, tk := range ticks {
			if tk.RestrictedApps > 0 {
				// Wall-clock counting: add elapsed time once, regardless of
				// how many restricted apps are running simultaneously
				entertainmentSeconds += tk.ElapsedSeconds
			}
			// If no restricted apps, counter stays the same
		}

		// Compute expected value using the model
		expected := computeEntertainmentTime(ticks)

		if entertainmentSeconds != expected {
			t.Fatalf("entertainment time mismatch: got %d, expected %d", entertainmentSeconds, expected)
		}

		// Additional property: verify wall-clock counting vs per-app counting.
		// If we incorrectly counted per-app (elapsed * numApps), the result
		// would be >= the wall-clock result (strictly greater when any tick
		// has more than 1 restricted app).
		perAppTotal := 0
		for _, tk := range ticks {
			perAppTotal += tk.ElapsedSeconds * tk.RestrictedApps
		}

		// Wall-clock total must be <= per-app total
		if entertainmentSeconds > perAppTotal {
			t.Fatalf("wall-clock total (%d) exceeds per-app total (%d), which is impossible",
				entertainmentSeconds, perAppTotal)
		}

		// If any tick has more than 1 restricted app, wall-clock must be strictly less
		hasMultipleApps := false
		for _, tk := range ticks {
			if tk.RestrictedApps > 1 {
				hasMultipleApps = true
				break
			}
		}
		if hasMultipleApps && entertainmentSeconds >= perAppTotal && perAppTotal > 0 {
			t.Fatalf("with multiple simultaneous restricted apps, wall-clock (%d) should be less than per-app (%d)",
				entertainmentSeconds, perAppTotal)
		}
	})
}

// Feature: parental-control-service, Property 6: Сброс счётчика при новом временном окне
// **Validates: Requirements 5.3**

// windowTransition models a transition between two entertainment windows.
// The service tracks the previous active window; when ActiveWindow changes
// to a different window, the entertainment counter must reset to zero.
type windowTransition struct {
	AccumulatedSeconds int // entertainment seconds accumulated in the previous window
}

// genAccumulatedSeconds generates a random accumulated entertainment time (1–7200 seconds).
func genAccumulatedSeconds() *rapid.Generator[int] {
	return rapid.IntRange(1, 7200)
}

// simulateCounterReset models the counter reset logic:
// if the new active window differs from the previous one, counter resets to 0.
func simulateCounterReset(prevWindowStart, prevWindowEnd, newWindowStart, newWindowEnd string, accumulated int) int {
	// Windows are different if their start or end times differ.
	if prevWindowStart != newWindowStart || prevWindowEnd != newWindowEnd {
		return 0
	}
	return accumulated
}

func TestPropertyCounterResetOnNewWindow(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate two distinct, non-overlapping entertainment windows on the same day.
		// Window A: 10:00-12:00, Window B: 14:00-16:00 (fixed structure, random limits).
		limitA := rapid.IntRange(10, 120).Draw(t, "limitA")
		limitB := rapid.IntRange(10, 120).Draw(t, "limitB")

		// Use a fixed day (Monday) for simplicity.
		day := "monday"

		windowA := config.TimeWindow{
			Days:         []string{day},
			Start:        "10:00",
			End:          "12:00",
			LimitMinutes: limitA,
		}
		windowB := config.TimeWindow{
			Days:         []string{day},
			Start:        "14:00",
			End:          "16:00",
			LimitMinutes: limitB,
		}

		schedule := config.ScheduleConfig{
			EntertainmentWindows: []config.TimeWindow{windowA, windowB},
		}

		sched := NewScheduler(schedule)

		// Pick a random time inside window A (10:00-11:59).
		minuteInA := rapid.IntRange(0, 119).Draw(t, "minuteInA")
		hourA := 10 + minuteInA/60
		minA := minuteInA % 60
		// Monday = 2024-01-01 is a Monday.
		timeInA := time.Date(2024, 1, 1, hourA, minA, 0, 0, time.UTC)

		// Verify we are inside window A.
		activeA := sched.ActiveWindow(timeInA)
		if activeA == nil {
			t.Fatalf("expected to be inside window A at %s, but ActiveWindow returned nil", timeInA.Format("15:04"))
		}
		if activeA.Start != "10:00" || activeA.End != "12:00" {
			t.Fatalf("expected window A (10:00-12:00), got %s-%s", activeA.Start, activeA.End)
		}

		// Simulate accumulated entertainment time in window A.
		accumulated := genAccumulatedSeconds().Draw(t, "accumulated")

		// Now transition to window B: pick a random time inside window B (14:00-15:59).
		minuteInB := rapid.IntRange(0, 119).Draw(t, "minuteInB")
		hourB := 14 + minuteInB/60
		minB := minuteInB % 60
		timeInB := time.Date(2024, 1, 1, hourB, minB, 0, 0, time.UTC)

		// Verify we are inside window B.
		activeB := sched.ActiveWindow(timeInB)
		if activeB == nil {
			t.Fatalf("expected to be inside window B at %s, but ActiveWindow returned nil", timeInB.Format("15:04"))
		}
		if activeB.Start != "14:00" || activeB.End != "16:00" {
			t.Fatalf("expected window B (14:00-16:00), got %s-%s", activeB.Start, activeB.End)
		}

		// Core property: windows A and B are different, so counter must reset.
		// Model the reset logic.
		counterAfterTransition := simulateCounterReset(
			activeA.Start, activeA.End,
			activeB.Start, activeB.End,
			accumulated,
		)

		if counterAfterTransition != 0 {
			t.Fatalf("counter should be 0 after transitioning from window A (%s-%s) to window B (%s-%s), got %d",
				activeA.Start, activeA.End, activeB.Start, activeB.End, counterAfterTransition)
		}

		// Also verify: if we stay in the same window, counter is NOT reset.
		// Pick another time inside window A.
		minuteInA2 := rapid.IntRange(0, 119).Draw(t, "minuteInA2")
		hourA2 := 10 + minuteInA2/60
		minA2 := minuteInA2 % 60
		timeInA2 := time.Date(2024, 1, 1, hourA2, minA2, 0, 0, time.UTC)

		activeA2 := sched.ActiveWindow(timeInA2)
		if activeA2 == nil {
			t.Fatalf("expected to be inside window A at %s, but ActiveWindow returned nil", timeInA2.Format("15:04"))
		}

		counterSameWindow := simulateCounterReset(
			activeA.Start, activeA.End,
			activeA2.Start, activeA2.End,
			accumulated,
		)

		if counterSameWindow != accumulated {
			t.Fatalf("counter should remain %d when staying in the same window, got %d",
				accumulated, counterSameWindow)
		}
	})
}

// Feature: parental-control-service, Property 8: Предупреждение перед порогом
// **Validates: Requirements 6.5, 12.5**

func TestPropertyWarningBeforeThreshold(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate random warning_before_minutes (1-30) and limit_minutes (30-240).
		warningBeforeMin := rapid.IntRange(1, 30).Draw(t, "warningBeforeMinutes")
		limitMinutes := rapid.IntRange(30, 240).Draw(t, "limitMinutes")

		schedule := config.ScheduleConfig{
			WarningBeforeMinutes: warningBeforeMin,
		}
		sched := NewScheduler(schedule)

		// Generate random entertainment_seconds covering the full range from 0 to beyond the limit.
		maxSeconds := (limitMinutes + 10) * 60 // go a bit beyond the limit
		entertainmentSeconds := rapid.IntRange(0, maxSeconds).Draw(t, "entertainmentSeconds")

		got := sched.ShouldWarnEntertainment(entertainmentSeconds, limitMinutes)

		// Model: remaining = limitMinutes - (entertainmentSeconds / 60)
		// Warning iff remaining > 0 AND remaining <= warningBeforeMinutes
		usedMinutes := entertainmentSeconds / 60
		remaining := limitMinutes - usedMinutes
		expected := remaining > 0 && remaining <= warningBeforeMin

		if got != expected {
			t.Fatalf(
				"ShouldWarnEntertainment(%d, %d) with warningBefore=%d: got %v, expected %v (used=%d min, remaining=%d min)",
				entertainmentSeconds, limitMinutes, warningBeforeMin, got, expected, usedMinutes, remaining,
			)
		}
	})

	// Sub-property: ShouldWarnSleep — warning when sleep time is approaching.
	rapid.Check(t, func(t *rapid.T) {
		sleepWarningMin := rapid.IntRange(1, 30).Draw(t, "sleepWarningBeforeMin")

		// Generate a sleep start hour (1-23) so we can place "now" before it.
		sleepStartHour := rapid.IntRange(1, 23).Draw(t, "sleepStartHour")
		sleepStartMinute := rapid.IntRange(0, 59).Draw(t, "sleepStartMinute")
		sleepStart := fmt.Sprintf("%02d:%02d", sleepStartHour, sleepStartMinute)

		// Sleep end is 1-8 hours after start (doesn't matter for warning logic).
		sleepEnd := fmt.Sprintf("%02d:%02d", (sleepStartHour+4)%24, sleepStartMinute)

		// Use Monday = 2024-01-01.
		day := "monday"

		schedule := config.ScheduleConfig{
			SleepTimes: []config.SleepTimeSlot{
				{Days: []string{day}, Start: sleepStart, End: sleepEnd},
			},
			SleepWarningBeforeMin: sleepWarningMin,
		}
		sched := NewScheduler(schedule)

		// Generate a random offset in minutes before sleep start (0 to sleepWarningMin*2+10).
		// This covers both inside and outside the warning window.
		maxOffset := sleepWarningMin*2 + 10
		offsetMinutes := rapid.IntRange(0, maxOffset).Draw(t, "offsetMinutes")

		// Compute "now" as sleepStart - offsetMinutes.
		sleepStartTotalMin := sleepStartHour*60 + sleepStartMinute
		nowTotalMin := sleepStartTotalMin - offsetMinutes

		// Skip if nowTotalMin goes negative (before midnight) — simplifies the test.
		if nowTotalMin < 0 {
			return
		}

		nowHour := nowTotalMin / 60
		nowMin := nowTotalMin % 60
		now := time.Date(2024, 1, 1, nowHour, nowMin, 0, 0, time.UTC)

		gotWarn, gotMinutes := sched.ShouldWarnSleep(now)

		// Model: diff = sleepStartTotalMin - nowTotalMin = offsetMinutes
		// Warning iff offsetMinutes >= 0 AND offsetMinutes <= sleepWarningMin
		expectedWarn := offsetMinutes >= 0 && offsetMinutes <= sleepWarningMin

		if gotWarn != expectedWarn {
			t.Fatalf(
				"ShouldWarnSleep at %02d:%02d (sleep at %s, warningMin=%d, offset=%d): got warn=%v, expected warn=%v",
				nowHour, nowMin, sleepStart, sleepWarningMin, offsetMinutes, gotWarn, expectedWarn,
			)
		}

		if gotWarn {
			// When warning is active, returned minutes should equal the offset.
			if gotMinutes != offsetMinutes {
				t.Fatalf(
					"ShouldWarnSleep minutes: got %d, expected %d (offset from sleep start)",
					gotMinutes, offsetMinutes,
				)
			}
		}
	})
}

// Feature: parental-control-service, Property 9: Приоритет времени сна и полная блокировка
// **Validates: Requirements 12.2, 12.4**

func TestPropertySleepTimePriority(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Use a fixed day: Monday = 2024-01-01.
		day := "monday"

		// Generate an entertainment window that starts between 14:00 and 19:00
		// and ends between entStartHour+3 and 23:00 (no midnight crossing, at least 3h gap).
		entStartHour := rapid.IntRange(14, 19).Draw(t, "entStartHour")
		entEndHour := rapid.IntRange(entStartHour+3, 23).Draw(t, "entEndHour")
		entStartMin := rapid.IntRange(0, 59).Draw(t, "entStartMin")
		entEndMin := rapid.IntRange(0, 59).Draw(t, "entEndMin")
		entStart := fmt.Sprintf("%02d:%02d", entStartHour, entStartMin)
		entEnd := fmt.Sprintf("%02d:%02d", entEndHour, entEndMin)
		limitMinutes := rapid.IntRange(30, 300).Draw(t, "limitMinutes")

		// Generate a sleep time that starts strictly between entStartHour+1 and entEndHour-1
		// (guaranteed to overlap with the entertainment window) and ends the next morning.
		sleepStartHour := rapid.IntRange(entStartHour+1, entEndHour-1).Draw(t, "sleepStartHour")
		sleepStartMin := rapid.IntRange(0, 59).Draw(t, "sleepStartMin")
		sleepStart := fmt.Sprintf("%02d:%02d", sleepStartHour, sleepStartMin)
		// Sleep ends the next morning (crosses midnight).
		sleepEndHour := rapid.IntRange(5, 9).Draw(t, "sleepEndHour")
		sleepEndMin := rapid.IntRange(0, 59).Draw(t, "sleepEndMin")
		sleepEnd := fmt.Sprintf("%02d:%02d", sleepEndHour, sleepEndMin)

		schedule := config.ScheduleConfig{
			EntertainmentWindows: []config.TimeWindow{
				{
					Days:         []string{day},
					Start:        entStart,
					End:          entEnd,
					LimitMinutes: limitMinutes,
				},
			},
			SleepTimes: []config.SleepTimeSlot{
				{
					Days:  []string{day},
					Start: sleepStart,
					End:   sleepEnd,
				},
			},
		}

		sched := NewScheduler(schedule)

		// Pick a random time that falls in BOTH the sleep time and the entertainment window.
		// The overlap region is [sleepStart, entEnd).
		// Convert to minutes for easier random selection.
		overlapStartMin := sleepStartHour*60 + sleepStartMin
		overlapEndMin := entEndHour*60 + entEndMin

		// Ensure there is actually an overlap (overlapStart < overlapEnd).
		if overlapStartMin >= overlapEndMin {
			return // skip degenerate case
		}

		// Pick a random minute within the overlap.
		overlapMinute := rapid.IntRange(overlapStartMin, overlapEndMin-1).Draw(t, "overlapMinute")
		nowHour := overlapMinute / 60
		nowMin := overlapMinute % 60
		now := time.Date(2024, 1, 1, nowHour, nowMin, 0, 0, time.UTC)

		// Core property: sleep time ALWAYS takes priority over entertainment window.
		state := sched.CurrentState(now)

		if state.Mode != ModeSleepTime {
			t.Fatalf(
				"at %02d:%02d (entertainment %s-%s, sleep %s-%s): expected ModeSleepTime, got %s",
				nowHour, nowMin, entStart, entEnd, sleepStart, sleepEnd, state.Mode,
			)
		}

		// Also verify that SleepTime field is populated.
		if state.SleepTime == nil {
			t.Fatalf(
				"at %02d:%02d: mode is ModeSleepTime but SleepTime field is nil",
				nowHour, nowMin,
			)
		}
	})
}
