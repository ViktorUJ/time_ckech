// Package stats tracks per-process and per-site usage statistics.
package stats

// AppUsage — статистика использования одного приложения/сайта за день.
type AppUsage struct {
	Name          string `json:"name"`           // имя процесса или домен сайта
	Type          string `json:"type"`            // "app" или "site"
	IsRestricted  bool   `json:"is_restricted"`   // развлекательное (restricted)
	TotalSeconds  int    `json:"total_seconds"`
}

// DayStats — статистика за один день.
type DayStats struct {
	Date                 string     `json:"date"` // "2026-03-22"
	EntertainmentSeconds int        `json:"entertainment_seconds"`
	Apps                 []AppUsage `json:"apps"`
}

// WeekStats — статистика за неделю (для API).
type WeekStats struct {
	Days []DayStats `json:"days"`
}
