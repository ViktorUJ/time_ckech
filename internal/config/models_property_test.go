package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

// Feature: parental-control-service, Property 1: Круговая сериализация расписания (Schedule round-trip)
// **Validates: Requirements 6.1, 12.1, 13.1, 14.1**

var validDays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

func genTimeString() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		h := rapid.IntRange(0, 23).Draw(t, "hour")
		m := rapid.IntRange(0, 59).Draw(t, "minute")
		return fmt.Sprintf("%02d:%02d", h, m)
	})
}

func genDays() *rapid.Generator[[]string] {
	return rapid.Custom(func(t *rapid.T) []string {
		n := rapid.IntRange(1, 7).Draw(t, "numDays")
		// Shuffle and pick n unique days
		perm := rapid.Permutation(validDays).Draw(t, "daysPerm")
		return perm[:n]
	})
}

func genTimeWindow() *rapid.Generator[TimeWindow] {
	return rapid.Custom(func(t *rapid.T) TimeWindow {
		return TimeWindow{
			Days:         genDays().Draw(t, "days"),
			Start:        genTimeString().Draw(t, "start"),
			End:          genTimeString().Draw(t, "end"),
			LimitMinutes: rapid.IntRange(1, 1440).Draw(t, "limitMinutes"),
		}
	})
}

func genSleepTimeSlot() *rapid.Generator[SleepTimeSlot] {
	return rapid.Custom(func(t *rapid.T) SleepTimeSlot {
		return SleepTimeSlot{
			Days:  genDays().Draw(t, "days"),
			Start: genTimeString().Draw(t, "start"),
			End:   genTimeString().Draw(t, "end"),
		}
	})
}

func genScheduleConfig() *rapid.Generator[ScheduleConfig] {
	return rapid.Custom(func(t *rapid.T) ScheduleConfig {
		numWindows := rapid.IntRange(0, 5).Draw(t, "numWindows")
		windows := make([]TimeWindow, numWindows)
		for i := range windows {
			windows[i] = genTimeWindow().Draw(t, fmt.Sprintf("window_%d", i))
		}

		numSleep := rapid.IntRange(0, 5).Draw(t, "numSleep")
		sleepTimes := make([]SleepTimeSlot, numSleep)
		for i := range sleepTimes {
			sleepTimes[i] = genSleepTimeSlot().Draw(t, fmt.Sprintf("sleep_%d", i))
		}

		return ScheduleConfig{
			EntertainmentWindows:  windows,
			SleepTimes:            sleepTimes,
			WarningBeforeMinutes:  rapid.IntRange(0, 60).Draw(t, "warningBefore"),
			SleepWarningBeforeMin: rapid.IntRange(0, 60).Draw(t, "sleepWarningBefore"),
			FullLogging:           rapid.Bool().Draw(t, "fullLogging"),
			HTTPLogEnabled:        rapid.Bool().Draw(t, "httpLogEnabled"),
			HTTPLogPort:           rapid.IntRange(1024, 65535).Draw(t, "httpLogPort"),
		}
	})
}

func TestPropertyScheduleConfigRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		original := genScheduleConfig().Draw(t, "scheduleConfig")

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}

		var decoded ScheduleConfig
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}

		if !reflect.DeepEqual(original, decoded) {
			t.Fatalf("round-trip mismatch:\noriginal: %+v\ndecoded:  %+v", original, decoded)
		}
	})
}
