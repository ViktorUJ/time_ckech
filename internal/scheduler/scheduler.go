// Package scheduler determines the current operating mode based on the schedule.
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"parental-control-service/internal/config"
)

// Scheduler determines the current operating mode based on the configured schedule.
type Scheduler struct {
	schedule config.ScheduleConfig
}

// NewScheduler creates a new Scheduler with the given schedule configuration.
func NewScheduler(schedule config.ScheduleConfig) *Scheduler {
	return &Scheduler{schedule: schedule}
}

// UpdateSchedule replaces the current schedule configuration.
func (s *Scheduler) UpdateSchedule(schedule config.ScheduleConfig) {
	s.schedule = schedule
}

// CurrentState determines the current mode based on the given time.
// Priority: sleep time > entertainment window > outside window.
func (s *Scheduler) CurrentState(now time.Time) ScheduleState {
	// Highest priority: sleep time.
	if slot := s.activeSleepSlot(now); slot != nil {
		return ScheduleState{
			Mode:      ModeSleepTime,
			SleepTime: slot,
		}
	}

	// Check entertainment windows.
	if tw := s.ActiveWindow(now); tw != nil {
		return ScheduleState{
			Mode:          ModeInsideWindow,
			CurrentWindow: tw,
			LimitMinutes:  tw.LimitMinutes,
		}
	}

	return ScheduleState{
		Mode: ModeOutsideWindow,
	}
}

// IsSleepTime checks whether the given time falls within any configured sleep time slot.
func (s *Scheduler) IsSleepTime(now time.Time) bool {
	return s.activeSleepSlot(now) != nil
}

// activeSleepSlot returns the active sleep time slot for the given time, or nil.
func (s *Scheduler) activeSleepSlot(now time.Time) *config.SleepTimeSlot {
	for i := range s.schedule.SleepTimes {
		slot := &s.schedule.SleepTimes[i]
		if s.timeInSleepSlot(now, slot) {
			return slot
		}
	}
	return nil
}

// timeInSleepSlot checks if now falls within the given sleep slot,
// supporting midnight crossing (e.g., 22:00-07:00).
func (s *Scheduler) timeInSleepSlot(now time.Time, slot *config.SleepTimeSlot) bool {
	startH, startM := parseTime(slot.Start)
	endH, endM := parseTime(slot.End)

	nowH, nowM := now.Hour(), now.Minute()
	nowMinutes := nowH*60 + nowM
	startMinutes := startH*60 + startM
	endMinutes := endH*60 + endM

	if startMinutes <= endMinutes {
		// No midnight crossing: e.g., 13:00-15:00
		if nowMinutes >= startMinutes && nowMinutes < endMinutes {
			return dayMatches(now, slot.Days)
		}
		return false
	}

	// Midnight crossing: e.g., 22:00-07:00
	// After start on the start day, or before end on the next day.
	if nowMinutes >= startMinutes {
		// We're in the evening portion — check if today matches.
		return dayMatches(now, slot.Days)
	}
	if nowMinutes < endMinutes {
		// We're in the morning portion — check if yesterday matches.
		yesterday := now.AddDate(0, 0, -1)
		return dayMatches(yesterday, slot.Days)
	}
	return false
}

// ActiveWindow returns the currently active entertainment time window, or nil.
func (s *Scheduler) ActiveWindow(now time.Time) *config.TimeWindow {
	for i := range s.schedule.EntertainmentWindows {
		tw := &s.schedule.EntertainmentWindows[i]
		if timeInRange(now, tw.Start, tw.End) && dayMatches(now, tw.Days) {
			return tw
		}
	}
	return nil
}

// ShouldWarnEntertainment returns true when the remaining entertainment time
// is at or below the configured warning_before_minutes threshold.
func (s *Scheduler) ShouldWarnEntertainment(entertainmentSeconds int, limitMinutes int) bool {
	if limitMinutes <= 0 {
		return false
	}
	warningMin := s.schedule.WarningBeforeMinutes
	if warningMin <= 0 {
		return false
	}
	usedMinutes := entertainmentSeconds / 60
	remaining := limitMinutes - usedMinutes
	return remaining > 0 && remaining <= warningMin
}

// ShouldWarnSleep checks whether sleep time is approaching within
// sleep_warning_before_minutes. Returns true and the minutes remaining
// until sleep starts, or false and 0.
func (s *Scheduler) ShouldWarnSleep(now time.Time) (bool, int) {
	warningMin := s.schedule.SleepWarningBeforeMin
	if warningMin <= 0 {
		return false, 0
	}

	for i := range s.schedule.SleepTimes {
		slot := &s.schedule.SleepTimes[i]
		mins := s.minutesUntilSleep(now, slot)
		if mins >= 0 && mins <= warningMin {
			return true, mins
		}
	}
	return false, 0
}

// minutesUntilSleep calculates minutes from now until the given sleep slot starts.
// Returns -1 if the slot doesn't apply today or already started.
func (s *Scheduler) minutesUntilSleep(now time.Time, slot *config.SleepTimeSlot) int {
	if !dayMatches(now, slot.Days) {
		return -1
	}

	startH, startM := parseTime(slot.Start)
	nowMinutes := now.Hour()*60 + now.Minute()
	startMinutes := startH*60 + startM

	diff := startMinutes - nowMinutes
	if diff < 0 {
		// Sleep already started or passed for today.
		return -1
	}
	return diff
}

// --- Helpers ---

// parseTime parses a "HH:MM" string into hour and minute.
// Returns (0, 0) on invalid input.
func parseTime(s string) (hour, minute int) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0
	}
	return h, m
}

// dayMatches checks if the given time's weekday matches any of the specified days.
// Days are lowercase English weekday names: "monday", "tuesday", etc.
func dayMatches(t time.Time, days []string) bool {
	weekday := strings.ToLower(t.Weekday().String())
	for _, d := range days {
		if strings.ToLower(d) == weekday {
			return true
		}
	}
	return false
}

// timeInRange checks if now's time-of-day falls within [start, end).
// Does NOT support midnight crossing — entertainment windows are expected
// to not cross midnight. For midnight-crossing logic, see timeInSleepSlot.
func timeInRange(now time.Time, start, end string) bool {
	startH, startM := parseTime(start)
	endH, endM := parseTime(end)

	nowMinutes := now.Hour()*60 + now.Minute()
	startMinutes := startH*60 + startM
	endMinutes := endH*60 + endM

	return nowMinutes >= startMinutes && nowMinutes < endMinutes
}

// String returns a human-readable representation of the Mode.
func (m Mode) String() string {
	switch m {
	case ModeOutsideWindow:
		return "outside_window"
	case ModeInsideWindow:
		return "inside_window"
	case ModeSleepTime:
		return "sleep_time"
	default:
		return fmt.Sprintf("unknown(%d)", int(m))
	}
}
