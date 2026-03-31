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
// Category: "system" — всегда доступен, не считается развлечением; "" или "allowed" — обычный разрешённый.
type AllowedSite struct {
	Domain            string   `json:"domain"`
	IncludeSubdomains bool     `json:"include_subdomains"`        // default: true
	AllowedPaths      []string `json:"allowed_paths,omitempty"`   // например ["/edu", "/learning/"] — пустой = весь домен
	Category          string   `json:"category,omitempty"`        // "system" или "" (обычный)
}

// ScheduleConfig — расписание.
type ScheduleConfig struct {
	EntertainmentWindows  []TimeWindow    `json:"entertainment_windows"`
	SleepTimes            []SleepTimeSlot `json:"sleep_times"`
	Holidays              []Holiday       `json:"holidays,omitempty"`               // праздники с датой и опциональным названием
	HolidayWindows        []TimeWindow    `json:"holiday_windows,omitempty"`        // окна развлечений для праздников
	HolidaySleepTimes     []SleepTimeSlot `json:"holiday_sleep_times,omitempty"`    // время сна для праздников
	WarningBeforeMinutes  int             `json:"warning_before_minutes"`           // default: 10
	SleepWarningBeforeMin int             `json:"sleep_warning_before_minutes"`     // default: 15
	FullLogging           bool            `json:"full_logging"`                     // default: false
	HTTPLogEnabled        bool            `json:"http_log_enabled"`                 // default: false
	HTTPLogPort           int             `json:"http_log_port"`                    // default: 8080
	EntertainmentApps     []string        `json:"entertainment_apps"`               // доп. exe-файлы развлекательных приложений
	TotalComputerMinutes  int             `json:"total_computer_minutes,omitempty"` // лимит общего времени за компьютером (0 = без ограничений)
	Vacations             []Vacation      `json:"vacations,omitempty"`              // каникулы с отдельным планом
}

// Holiday — праздничный день.
type Holiday struct {
	Date string `json:"date"`           // "2026-01-01"
	Name string `json:"name,omitempty"` // опциональное название праздника
}

// Vacation — каникулы (диапазон дат с отдельным планом).
type Vacation struct {
	Name        string          `json:"name,omitempty"`
	StartDate   string          `json:"start_date"`                // "2026-06-01"
	EndDate     string          `json:"end_date"`                  // "2026-08-31"
	Windows     []TimeWindow    `json:"windows"`                   // окна развлечений на каникулах
	SleepTimes  []SleepTimeSlot `json:"sleep_times,omitempty"`     // время сна (если не задано — обычное)
	PreDayStart string          `json:"pre_day_start,omitempty"`   // начало окна за день до каникул, по умолчанию "16:00"
	PreDayEnd   string          `json:"pre_day_end,omitempty"`     // конец окна за день до каникул, по умолчанию "22:30"
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
