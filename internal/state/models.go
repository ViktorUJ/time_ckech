package state

import "time"

// ServiceState — персистентное состояние, сохраняемое на диск каждые 30 сек.
type ServiceState struct {
	EntertainmentSeconds int       `json:"entertainment_seconds"`
	BonusSeconds         int       `json:"bonus_seconds"`
	ComputerSeconds      int       `json:"computer_seconds"`
	WindowStart          time.Time `json:"window_start"`
	WindowEnd            time.Time `json:"window_end"`
	LastSaveTime         time.Time `json:"last_save_time"`
	LastTickTime         time.Time `json:"last_tick_time"`
	SleepOverrideDate    string    `json:"sleep_override_date,omitempty"`
	SleepOverrideStart   string    `json:"sleep_override_start,omitempty"`
	SleepOverrideEnd     string    `json:"sleep_override_end,omitempty"`
	ServiceMode          string    `json:"service_mode,omitempty"`
	ModeUntil            time.Time `json:"mode_until,omitempty"`
}
