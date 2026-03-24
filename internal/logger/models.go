package logger

import "time"

// Константы типов событий.
const (
	EventAppStart    = "app_start"
	EventAppStop     = "app_stop"
	EventSiteVisit   = "site_visit"
	EventServiceStart = "service_start"
	EventServiceStop  = "service_stop"
	EventWarning     = "warning"
	EventBlock       = "block"
	EventInfo        = "info"
)

// LogEntry — запись в полном логе.
type LogEntry struct {
	Timestamp   time.Time `json:"timestamp"`
	EventType   string    `json:"event_type"`              // "app_start", "app_stop", "site_visit", "service_start", "service_stop", "warning", "block"
	ProcessName string    `json:"process_name,omitempty"`
	ExePath     string    `json:"exe_path,omitempty"`
	URL         string    `json:"url,omitempty"`
	Browser     string    `json:"browser,omitempty"`
	User        string    `json:"user,omitempty"`
	Duration    int       `json:"duration_seconds,omitempty"`
	Message     string    `json:"message,omitempty"`
}
