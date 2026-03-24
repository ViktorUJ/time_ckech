package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// settingsFileName is the name of the service settings file.
const settingsFileName = "settings.json"

// ServiceSettings holds installation-time settings for the service.
type ServiceSettings struct {
	ConfigURL    string `json:"config_url,omitempty"`    // single combined config URL (preferred)
	AppsURL      string `json:"apps_url,omitempty"`      // legacy: separate URLs
	SitesURL     string `json:"sites_url,omitempty"`     // legacy
	ScheduleURL  string `json:"schedule_url,omitempty"`  // legacy
	HTTPPort     int    `json:"http_port"`
	PasswordHash string `json:"password_hash,omitempty"` // bcrypt hash пароля для паузы
}

// LoadSettings reads the settings file from the given directory.
func LoadSettings(dir string) (*ServiceSettings, error) {
	path := dir + `\` + settingsFileName
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}

	var s ServiceSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse settings: %w", err)
	}

	if s.ConfigURL == "" && (s.AppsURL == "" || s.SitesURL == "" || s.ScheduleURL == "") {
		return nil, fmt.Errorf("settings: config_url or all three separate URLs must be specified")
	}

	return &s, nil
}

// SaveSettings writes the settings file to the given directory.
func SaveSettings(dir string, s *ServiceSettings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	path := dir + `\` + settingsFileName
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}
