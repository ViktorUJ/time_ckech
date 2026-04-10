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
	Timestamp          time.Time `json:"timestamp"`
	EventType          string    `json:"event_type"`
	ProcessName        string    `json:"process_name,omitempty"`
	ExePath            string    `json:"exe_path,omitempty"`
	URL                string    `json:"url,omitempty"`
	Browser            string    `json:"browser,omitempty"`
	User               string    `json:"user,omitempty"`
	Duration           int       `json:"duration_seconds,omitempty"`
	Message            string    `json:"message,omitempty"`
	EntertainmentUsed  int       `json:"ent_used,omitempty"`
	EntertainmentLimit int       `json:"ent_limit,omitempty"`
	EntertainmentLeft  int       `json:"ent_left,omitempty"`
	BonusMinutes       int       `json:"bonus,omitempty"`
	ComputerMinutes    int       `json:"computer,omitempty"`
	CPUPercent         float64   `json:"cpu_pct,omitempty"`
	GPUPercent         float64   `json:"gpu_pct,omitempty"`
	MemoryPercent      float64   `json:"mem_pct,omitempty"`
	NetMBps            float64   `json:"net_mbps,omitempty"`
}
