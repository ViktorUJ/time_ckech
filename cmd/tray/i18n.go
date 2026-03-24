//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Lang — поддерживаемые языки.
type Lang string

const (
	LangEN Lang = "en"
	LangRU Lang = "ru"
)

var (
	currentLang Lang = LangRU // по умолчанию русский
	langMu      sync.RWMutex
)

// langFilePath возвращает путь к файлу с сохранённым языком.
func langFilePath() string {
	return filepath.Join(os.Getenv("ProgramData"), "ParentalControlService", "lang.txt")
}

// loadLang загружает язык из файла при старте.
func loadLang() {
	data, err := os.ReadFile(langFilePath())
	if err != nil {
		return
	}
	l := Lang(strings.TrimSpace(string(data)))
	if l == LangEN || l == LangRU {
		langMu.Lock()
		currentLang = l
		langMu.Unlock()
	}
}

func setLang(l Lang) {
	langMu.Lock()
	currentLang = l
	langMu.Unlock()
	// Сохраняем выбор на диск.
	_ = os.WriteFile(langFilePath(), []byte(string(l)), 0o644)
}

func getLang() Lang {
	langMu.RLock()
	defer langMu.RUnlock()
	return currentLang
}

// tr возвращает перевод по ключу для текущего языка.
func tr(key string) string {
	lang := getLang()
	if m, ok := translations[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	// Fallback to EN.
	if v, ok := translations[LangEN][key]; ok {
		return v
	}
	return key
}

var translations = map[Lang]map[string]string{
	LangEN: {
		"tray.title":           "Parental Control",
		"menu.status":          "Show Status",
		"menu.apps":            "Allowed Apps",
		"menu.sites":           "Allowed Sites",
		"menu.schedule":        "Schedule",
		"menu.config":          "Configuration",
		"menu.logs":            "Open Logs",
		"menu.stats":           "Statistics",
		"menu.lang":            "Переключить на русский",
		"menu.quit":            "Quit",
		"status.unavailable":   "Service unavailable",
		"status.sleep":         "Sleep time — computer unavailable",
		"status.outside":       "No entertainment window now",
		"status.no_limit":      "Entertainment: %d min (no limit)",
		"status.with_limit":    "Entertainment: %d of %d min. Remaining: %d min.",
		"status.default":       "Parental Control",
		"browser.warn":         "%s\n\nClose the tab within 30 seconds or the browser window will be closed.",
		"browser.default_reason": "Access to this site is currently blocked",
		"stats.title":           "Usage Statistics",
		"stats.today":           "Today",
		"stats.week":            "Week",
		"stats.entertainment":   "Entertainment: %d min",
		"stats.no_data":         "No data for this day",
		"stats.app":             "App",
		"stats.site":            "Site",
		"stats.restricted":      " [restricted]",
		"menu.pause":            "Pause",
		"menu.unpause":          "Unpause",
		"pause.enter_password":  "Enter password:",
		"pause.password_label":  "Password",
		"pause.enter_minutes":   "Pause duration (minutes):",
		"pause.minutes_label":   "Minutes",
		"pause.invalid_minutes": "Invalid number of minutes",
		"pause.cancel":          "Cancel",
		"pause.wrong_password":  "Wrong password",
		"pause.paused_ok":       "Paused for %d min.",
		"pause.unpaused_ok":     "Pause removed",
		"status.paused":         "PAUSED until %s",
		"menu.reload":           "Reload Config",
		"reload.ok":             "Configuration reloaded successfully",
		"reload.fail":           "Config reload failed",
		"menu.password":         "Change Password",
		"password.enter_old":    "Enter current password:",
		"password.enter_new":    "Enter new password:",
		"password.confirm_new":  "Confirm new password:",
		"password.mismatch":     "Passwords do not match",
		"password.changed_ok":   "Password changed successfully",
	},
	LangRU: {
		"tray.title":           "Родительский контроль",
		"menu.status":          "Показать статус",
		"menu.apps":            "Разрешённые программы",
		"menu.sites":           "Разрешённые сайты",
		"menu.schedule":        "Расписание",
		"menu.config":          "Конфигурация",
		"menu.logs":            "Открыть логи",
		"menu.stats":           "Статистика",
		"menu.lang":            "Switch to English",
		"menu.quit":            "Выход",
		"status.unavailable":   "Сервис недоступен",
		"status.sleep":         "Время сна — компьютер недоступен",
		"status.outside":       "Сейчас нет разрешённого окна",
		"status.no_limit":      "Развлечения: %d мин. (без лимита)",
		"status.with_limit":    "Развлечения: %d из %d мин. Осталось: %d мин.",
		"status.default":       "Родительский контроль",
		"browser.warn":         "%s\n\nЗакройте вкладку в течение 30 секунд, иначе окно браузера будет закрыто.",
		"browser.default_reason": "Доступ к этому сайту сейчас заблокирован",
		"stats.title":           "Статистика использования",
		"stats.today":           "Сегодня",
		"stats.week":            "Неделя",
		"stats.entertainment":   "Развлечения: %d мин",
		"stats.no_data":         "Нет данных за этот день",
		"stats.app":             "Прог",
		"stats.site":            "Сайт",
		"stats.restricted":      " [развлечение]",
		"menu.pause":            "Пауза",
		"menu.unpause":          "Снять паузу",
		"pause.enter_password":  "Введите пароль:",
		"pause.password_label":  "Пароль",
		"pause.enter_minutes":   "Длительность паузы (минуты):",
		"pause.minutes_label":   "Минуты",
		"pause.invalid_minutes": "Неверное количество минут",
		"pause.cancel":          "Отмена",
		"pause.wrong_password":  "Неверный пароль",
		"pause.paused_ok":       "Пауза на %d мин.",
		"pause.unpaused_ok":     "Пауза снята",
		"status.paused":         "ПАУЗА до %s",
		"menu.reload":           "Обновить конфиг",
		"reload.ok":             "Конфигурация обновлена",
		"reload.fail":           "Ошибка обновления конфига",
		"menu.password":         "Сменить пароль",
		"password.enter_old":    "Введите текущий пароль:",
		"password.enter_new":    "Введите новый пароль:",
		"password.confirm_new":  "Подтвердите новый пароль:",
		"password.mismatch":     "Пароли не совпадают",
		"password.changed_ok":   "Пароль успешно изменён",
	},
}
