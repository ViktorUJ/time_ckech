package httplog

import (
	"encoding/json"

	"parental-control-service/internal/logger"
)

// StatusResponse — ответ GET /status.
type StatusResponse struct {
	Mode                 string   `json:"mode"`                    // "inside_window", "outside_window", "sleep_time"
	ServiceMode          string   `json:"service_mode"`            // "normal", "filter_paused", "entertainment_paused", "learning"
	DayType              string   `json:"day_type"`                // "workday", "weekend", "holiday"
	HolidayName          string   `json:"holiday_name,omitempty"`
	VacationName         string   `json:"vacation_name,omitempty"`  // название праздника (если day_type == "holiday")
	EntertainmentMinutes int      `json:"entertainment_minutes"`
	LimitMinutes         int      `json:"limit_minutes"`
	MinutesRemaining     int      `json:"minutes_remaining"`
	BonusMinutes         int      `json:"bonus_minutes"`
	ComputerMinutes      int      `json:"computer_minutes"`
	ComputerLimitMinutes int      `json:"computer_limit_minutes"`
	ActiveWindow         *string  `json:"active_window,omitempty"`
	NextWindow           string   `json:"next_window,omitempty"` // "17:00-21:00" или nil
	SleepTime            *string  `json:"sleep_time,omitempty"`
	SleepWindow          string   `json:"sleep_window,omitempty"`  // "22:00-07:00" — расписание сна на сегодня
	SleepOverride        string   `json:"sleep_override,omitempty"` // "23:00-07:30" — ручная корректировка
	ActiveProcesses      []string `json:"active_processes"`
	ConfigLastUpdated    string   `json:"config_last_updated"`
	Paused               bool     `json:"paused"`                  // deprecated: use service_mode
	PauseUntil           string   `json:"pause_until,omitempty"`   // RFC3339
}

// LogsResponse — ответ GET /logs.
type LogsResponse struct {
	Entries []logger.LogEntry `json:"entries"`
	Total   int               `json:"total"`
}

// ConfigResponse — ответ GET /config.
type ConfigResponse struct {
	Apps     json.RawMessage `json:"apps"`
	Sites    json.RawMessage `json:"sites"`
	Schedule json.RawMessage `json:"schedule"`
}

// PauseRequest — запрос POST /pause.
type PauseRequest struct {
	Password string `json:"password"`
	Minutes  int    `json:"minutes"` // на сколько минут
}

// PauseResponse — ответ POST /pause и POST /unpause.
type PauseResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// UnpauseRequest — запрос POST /unpause.
type UnpauseRequest struct {
	Password string `json:"password"`
}

// SetModeRequest — запрос POST /set-mode.
type SetModeRequest struct {
	Password string `json:"password"`
	Mode     string `json:"mode"`    // "normal", "filter_paused", "entertainment_paused", "learning"
	Minutes  int    `json:"minutes"` // 0 = бессрочно (до ручной отмены)
}

// AddUsageRequest — добавить использованное время развлечений (играл на другом компьютере).
type AddUsageRequest struct {
	Password string `json:"password"`
	Minutes  int    `json:"minutes"` // сколько минут использовано
	Reason   string `json:"reason"`  // причина / описание
}

// AdjustBonusRequest — добавить/убрать бонусное время развлечений.
type AdjustBonusRequest struct {
	Password string `json:"password"`
	Minutes  int    `json:"minutes"` // положительное = добавить, отрицательное = убрать
	Reason   string `json:"reason"`
}

// AdjustSleepRequest — изменить время сна на сегодня.
type AdjustSleepRequest struct {
	Password  string `json:"password"`
	NewStart  string `json:"new_start"`  // "HH:MM" или "" (не менять)
	NewEnd    string `json:"new_end"`    // "HH:MM" или "" (не менять)
	Reason    string `json:"reason"`
}

// Notification — уведомление для показа в tray.
type Notification struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}
