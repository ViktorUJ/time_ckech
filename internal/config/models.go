package config

// AllowedAppsConfig — список разрешённых программ.
type AllowedAppsConfig struct {
	Apps []AllowedApp `json:"apps"`
}

// AllowedApp — запись о разрешённой программе.
type AllowedApp struct {
	Name       string `json:"name"`       // Человекочитаемое имя
	Executable string `json:"executable"` // Имя .exe (регистронезависимое)
	Path       string `json:"path"`       // Полный путь (опционально, wildcard *)
}

// AllowedSitesConfig — список разрешённых сайтов.
type AllowedSitesConfig struct {
	Sites []AllowedSite `json:"sites"`
}

// AllowedSite — запись о разрешённом сайте.
// Если AllowedPaths пуст — разрешён весь домен.
// Если AllowedPaths задан — разрешены только URL, путь которых начинается с одного из указанных префиксов.
type AllowedSite struct {
	Domain            string   `json:"domain"`
	IncludeSubdomains bool     `json:"include_subdomains"` // default: true
	AllowedPaths      []string `json:"allowed_paths"`      // например ["/edu", "/learning/"] — пустой = весь домен
}

// ScheduleConfig — расписание.
type ScheduleConfig struct {
	EntertainmentWindows  []TimeWindow    `json:"entertainment_windows"`
	SleepTimes            []SleepTimeSlot `json:"sleep_times"`
	WarningBeforeMinutes  int             `json:"warning_before_minutes"`       // default: 10
	SleepWarningBeforeMin int             `json:"sleep_warning_before_minutes"` // default: 15
	FullLogging           bool            `json:"full_logging"`                 // default: false
	HTTPLogEnabled        bool            `json:"http_log_enabled"`             // default: false
	HTTPLogPort           int             `json:"http_log_port"`                // default: 8080
	EntertainmentApps     []string        `json:"entertainment_apps"`           // доп. exe-файлы развлекательных приложений
}

// TimeWindow — временное окно для развлекательного контента.
type TimeWindow struct {
	Days         []string `json:"days"`          // "monday"..."sunday"
	Start        string   `json:"start"`         // "HH:MM"
	End          string   `json:"end"`           // "HH:MM"
	LimitMinutes int      `json:"limit_minutes"`
}

// SleepTimeSlot — период времени сна.
type SleepTimeSlot struct {
	Days  []string `json:"days"`
	Start string   `json:"start"` // "HH:MM"
	End   string   `json:"end"`   // "HH:MM" (поддержка перехода через полночь)
}

// Config — полная конфигурация сервиса.
type Config struct {
	AllowedApps  AllowedAppsConfig  `json:"allowed_apps"`
	AllowedSites AllowedSitesConfig `json:"allowed_sites"`
	Schedule     ScheduleConfig     `json:"schedule"`
}
