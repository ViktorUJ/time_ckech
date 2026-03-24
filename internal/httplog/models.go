package httplog

import (
	"encoding/json"

	"parental-control-service/internal/logger"
)

// StatusResponse — ответ GET /status.
type StatusResponse struct {
	Mode                 string   `json:"mode"`                    // "inside_window", "outside_window", "sleep_time"
	EntertainmentMinutes int      `json:"entertainment_minutes"`
	LimitMinutes         int      `json:"limit_minutes"`
	MinutesRemaining     int      `json:"minutes_remaining"`
	ActiveWindow         *string  `json:"active_window,omitempty"` // "17:00-21:00" или nil
	SleepTime            *string  `json:"sleep_time,omitempty"`
	ActiveProcesses      []string `json:"active_processes"`
	ConfigLastUpdated    string   `json:"config_last_updated"`
	Paused               bool     `json:"paused"`
	PauseUntil           string   `json:"pause_until,omitempty"` // RFC3339
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
	Minutes  int    `json:"minutes"` // на сколько минут поставить паузу
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
