package monitor

import (
	"path/filepath"
	"strings"

	"parental-control-service/internal/config"
)

// systemPathPrefixes — префиксы путей системных процессов Windows.
var systemPathPrefixes = []string{
	`c:\windows\`,
	`c:\program files\`,
	`c:\program files (x86)\`,
	`c:\programdata\`,
}

// entertainmentApps — известные развлекательные приложения (медиаплееры, игры).
// Эти процессы НЕ считаются системными, даже если установлены в Program Files.
// Проверяется по имени исполняемого файла (case-insensitive).
var entertainmentApps = map[string]bool{
	// Медиаплееры
	"mpc-hc64.exe":          true,
	"mpc-hc.exe":            true,
	"mpc-be64.exe":          true,
	"mpc-be.exe":            true,
	"vlc.exe":               true,
	"wmplayer.exe":          true, // Windows Media Player
	"movies & tv.exe":       true,
	"video.ui.exe":          true, // Windows 11 Media Player
	"potplayer.exe":         true,
	"potplayermini.exe":     true,
	"potplayermini64.exe":   true,
	"potplayer64.exe":       true,
	"kmplayer.exe":          true,
	"kmplayer64.exe":        true,
	"gom.exe":               true, // GOM Player
	"smplayer.exe":          true,
	"mpv.exe":               true,
	"bsplayer.exe":          true,
	"daum potplayer.exe":    true,
	// Игровые платформы и лаунчеры
	"steam.exe":             true,
	"steamwebhelper.exe":    true,
	"epicgameslauncher.exe": true,
	"gog galaxy.exe":        true,
	"galaxyclient.exe":      true,
	"origin.exe":            true,
	"ea.exe":                true,
	"ubisoft connect.exe":   true,
	"upc.exe":               true,
	"battle.net.exe":        true,
	// Стриминг
	"spotify.exe":           true,
}

// DefaultClassifier реализует ProcessClassifier с поддержкой
// системных процессов, списка разрешённых программ и подписей Microsoft.
type DefaultClassifier struct {
	allowedApps        []config.AllowedApp
	sigChecker         SignatureChecker
	extraEntertainment map[string]bool // доп. entertainment apps из конфига
}

// NewDefaultClassifier создаёт новый классификатор процессов.
// extraEntertainmentApps — дополнительные exe-файлы развлекательных приложений из конфига.
func NewDefaultClassifier(allowedApps []config.AllowedApp, sigChecker SignatureChecker, extraEntertainmentApps []string) *DefaultClassifier {
	extra := make(map[string]bool, len(extraEntertainmentApps))
	for _, name := range extraEntertainmentApps {
		extra[strings.ToLower(name)] = true
	}
	return &DefaultClassifier{
		allowedApps:        allowedApps,
		sigChecker:         sigChecker,
		extraEntertainment: extra,
	}
}

// UpdateAllowedApps обновляет список разрешённых приложений.
// Вызывается при загрузке нового конфига с GitHub.
func (c *DefaultClassifier) UpdateAllowedApps(apps []config.AllowedApp) {
	c.allowedApps = apps
}

// Classify классифицирует процесс и возвращает ProcessInfo.
func (c *DefaultClassifier) Classify(pid uint32, name string, exePath string) ProcessInfo {
	info := ProcessInfo{
		PID:     pid,
		Name:    name,
		ExePath: exePath,
	}

	if c.isSystemProcess(exePath) {
		info.IsSystem = true
		return info
	}

	if c.isAllowedProcess(name, exePath) {
		info.IsAllowed = true
		return info
	}

	// Неразрешённый процесс: IsSystem=false, IsAllowed=false.
	return info
}

// isSystemProcess проверяет, является ли процесс системным.
func (c *DefaultClassifier) isSystemProcess(exePath string) bool {
	if exePath == "" {
		return true
	}

	lowerPath := strings.ToLower(exePath)
	lowerName := strings.ToLower(filepath.Base(exePath))

	// Развлекательные приложения НЕ системные, даже если в Program Files.
	if entertainmentApps[lowerName] {
		return false
	}
	if c.extraEntertainment[lowerName] {
		return false
	}

	for _, prefix := range systemPathPrefixes {
		if strings.HasPrefix(lowerPath, prefix) {
			return true
		}
	}

	if c.sigChecker != nil && c.sigChecker.IsMicrosoftSigned(exePath) {
		return true
	}

	return false
}

// isAllowedProcess проверяет, находится ли процесс в списке разрешённых.
func (c *DefaultClassifier) isAllowedProcess(name string, exePath string) bool {
	for _, app := range c.allowedApps {
		if !strings.EqualFold(name, app.Executable) {
			continue
		}

		// Имя совпало. Если путь в записи не указан — процесс разрешён.
		if app.Path == "" {
			return true
		}

		// Путь указан — проверяем совпадение с wildcard через filepath.Match.
		if exePath == "" {
			continue
		}

		matched, err := filepath.Match(strings.ToLower(app.Path), strings.ToLower(exePath))
		if err == nil && matched {
			return true
		}
	}

	return false
}
