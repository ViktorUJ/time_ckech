package scheduler

import "parental-control-service/internal/config"

// Mode — текущий режим работы сервиса.
type Mode int

const (
	ModeOutsideWindow Mode = iota // Вне временного окна
	ModeInsideWindow              // Внутри временного окна
	ModeSleepTime                 // Время сна
)

// ScheduleState — текущее состояние расписания.
type ScheduleState struct {
	Mode             Mode                 `json:"mode"`
	CurrentWindow    *config.TimeWindow   `json:"current_window,omitempty"`    // nil если вне окна
	SleepTime        *config.SleepTimeSlot `json:"sleep_time,omitempty"`       // nil если не время сна
	MinutesRemaining int                  `json:"minutes_remaining"`           // минут до конца окна/лимита
	LimitMinutes     int                  `json:"limit_minutes"`              // лимит текущего окна
}
