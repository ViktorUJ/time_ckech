//go:build windows

// Tray application for Parental Control Service.
// Shows an icon in the system tray with entertainment time info.
// Communicates with the service via its HTTP API (localhost).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows/registry"

	"parental-control-service/internal/version"
)

const (
	pollInterval = 30 * time.Second
	defaultPort  = 8080
)

var (
	baseURL    string
	httpClient *http.Client
)

// Menu items stored globally for dynamic label updates on language switch.
var (
	mStatus   *systray.MenuItem
	mPause    *systray.MenuItem
	mPassword *systray.MenuItem
	mReload   *systray.MenuItem
	mConfig   *systray.MenuItem
	mLogs     *systray.MenuItem
	mStats    *systray.MenuItem
	mLang     *systray.MenuItem
	mQuit     *systray.MenuItem
)

// Текущее состояние паузы (для переключения иконки).
var isPaused bool

type statusResponse struct {
	Mode                 string  `json:"mode"`
	EntertainmentMinutes int     `json:"entertainment_minutes"`
	LimitMinutes         int     `json:"limit_minutes"`
	MinutesRemaining     int     `json:"minutes_remaining"`
	ActiveWindow         *string `json:"active_window,omitempty"`
	SleepTime            *string `json:"sleep_time,omitempty"`
	Paused               bool    `json:"paused"`
	PauseUntil           string  `json:"pause_until,omitempty"`
}

func main() {
	ensureSingleInstance()
	loadLang()

	port := defaultPort
	if p, err := readPortFromSettings(); err == nil && p > 0 {
		port = p
	}

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	httpClient = &http.Client{Timeout: 5 * time.Second}

	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetIcon(hourglassICO)
	systray.SetTitle(tr("tray.title"))
	systray.SetTooltip(tr("tray.title") + " " + version.GitCommit)

	mStatus = systray.AddMenuItem(tr("menu.status"), "")
	mPause = systray.AddMenuItem(tr("menu.pause"), "")
	mPassword = systray.AddMenuItem(tr("menu.password"), "")
	mReload = systray.AddMenuItem(tr("menu.reload"), "")
	mConfig = systray.AddMenuItem(tr("menu.config"), "")
	mLogs = systray.AddMenuItem(tr("menu.logs"), "")
	mStats = systray.AddMenuItem(tr("menu.stats"), "")
	systray.AddSeparator()
	mLang = systray.AddMenuItem(tr("menu.lang"), "")
	mQuit = systray.AddMenuItem(tr("menu.quit"), "")

	go pollStatus()

	go func() {
		for {
			select {
			case <-mStatus.ClickedCh:
				showStatusPopup()
			case <-mPause.ClickedCh:
				go handlePauseClick()
			case <-mPassword.ClickedCh:
				go handleChangePassword()
			case <-mReload.ClickedCh:
				go handleReloadConfig()
			case <-mConfig.ClickedCh:
				go showConfig()
			case <-mLogs.ClickedCh:
				openLogs()
			case <-mStats.ClickedCh:
				go showStats()
			case <-mLang.ClickedCh:
				toggleLang()
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()
}

func onExit() {}

// toggleLang switches between EN and RU and updates all menu labels.
func toggleLang() {
	if getLang() == LangEN {
		setLang(LangRU)
	} else {
		setLang(LangEN)
	}
	updateMenuLabels()
}

func updateMenuLabels() {
	mStatus.SetTitle(tr("menu.status"))
	if isPaused {
		mPause.SetTitle(tr("menu.unpause"))
	} else {
		mPause.SetTitle(tr("menu.pause"))
	}
	mPassword.SetTitle(tr("menu.password"))
	mReload.SetTitle(tr("menu.reload"))
	mConfig.SetTitle(tr("menu.config"))
	mLogs.SetTitle(tr("menu.logs"))
	mStats.SetTitle(tr("menu.stats"))
	mLang.SetTitle(tr("menu.lang"))
	mQuit.SetTitle(tr("menu.quit"))
	systray.SetTitle(tr("tray.title"))
	systray.SetTooltip(tr("tray.title") + " " + version.GitCommit)
}

func pollStatus() {
	statusURL := baseURL + "/status"
	if s, err := fetchStatus(httpClient, statusURL); err == nil {
		updatePauseState(s.Paused)
		systray.SetTooltip(formatStatus(s))
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for range ticker.C {
		if s, err := fetchStatus(httpClient, statusURL); err == nil {
			updatePauseState(s.Paused)
			systray.SetTooltip(formatStatus(s))
		} else {
			systray.SetTooltip(tr("tray.title") + ": " + tr("status.unavailable"))
		}
	}
}

// refreshStatusNow немедленно запрашивает статус и обновляет иконку/тултип.
func refreshStatusNow() {
	statusURL := baseURL + "/status"
	if s, err := fetchStatus(httpClient, statusURL); err == nil {
		updatePauseState(s.Paused)
		systray.SetTooltip(formatStatus(s))
	}
}

// updatePauseState обновляет иконку и текст меню при изменении состояния паузы.
func updatePauseState(paused bool) {
	if paused == isPaused {
		return
	}
	isPaused = paused
	if paused {
		systray.SetIcon(hourglassPausedICO)
		mPause.SetTitle(tr("menu.unpause"))
	} else {
		systray.SetIcon(hourglassICO)
		mPause.SetTitle(tr("menu.pause"))
	}
}

func fetchStatus(client *http.Client, url string) (*statusResponse, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var s statusResponse
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func formatStatus(s *statusResponse) string {
	if s.Paused && s.PauseUntil != "" {
		if t, err := time.Parse(time.RFC3339, s.PauseUntil); err == nil {
			return fmt.Sprintf(tr("status.paused"), t.Format("15:04"))
		}
	}
	switch s.Mode {
	case "sleep_time":
		return tr("status.sleep")
	case "outside_window":
		return tr("status.outside")
	case "inside_window":
		spent := s.EntertainmentMinutes
		limit := s.LimitMinutes
		remaining := s.MinutesRemaining
		if limit <= 0 {
			return fmt.Sprintf(tr("status.no_limit"), spent)
		}
		return fmt.Sprintf(tr("status.with_limit"), spent, limit, remaining)
	default:
		return tr("status.default")
	}
}

func showStatusPopup() {
	statusURL := baseURL + "/status"
	text := tr("status.unavailable")
	if s, err := fetchStatus(httpClient, statusURL); err == nil {
		text = formatStatus(s)
	}
	showMessageBox(tr("tray.title"), text)
}

func openLogs() {
	openURLInBrowser(baseURL + "/logs-html?lang=" + string(getLang()))
}

// showStats opens the stats HTML page in the default browser.
func showStats() {
	openURLInBrowser(baseURL + "/stats-html?lang=" + string(getLang()))
}

// handlePauseClick обрабатывает нажатие на пункт меню "Пауза"/"Снять паузу".
func handlePauseClick() {
	if isPaused {
		// Снять паузу — запрашиваем только пароль.
		pwd := inputBoxPassword(tr("pause.enter_password"), tr("pause.password_label"))
		if pwd == "" {
			return
		}
		resp, err := postJSON(baseURL+"/unpause", map[string]interface{}{
			"password": pwd,
		})
		if err != nil {
			showMessageBox(tr("tray.title"), tr("status.unavailable"))
			return
		}
		if resp.OK {
			showMessageBox(tr("tray.title"), tr("pause.unpaused_ok"))
		} else {
			showMessageBox(tr("tray.title"), tr("pause.wrong_password"))
		}
		refreshStatusNow()
	} else {
		// Поставить паузу — запрашиваем пароль и время.
		pwd := inputBoxPassword(tr("pause.enter_password"), tr("pause.password_label"))
		if pwd == "" {
			return
		}
		minsStr := inputBox(tr("pause.enter_minutes"), tr("pause.minutes_label"))
		if minsStr == "" {
			return
		}
		mins, err := strconv.Atoi(minsStr)
		if err != nil || mins < 1 {
			showMessageBox(tr("tray.title"), tr("pause.invalid_minutes"))
			return
		}
		resp, err := postJSON(baseURL+"/pause", map[string]interface{}{
			"password": pwd,
			"minutes":  mins,
		})
		if err != nil {
			showMessageBox(tr("tray.title"), tr("status.unavailable"))
			return
		}
		if resp.OK {
			showMessageBox(tr("tray.title"), fmt.Sprintf(tr("pause.paused_ok"), mins))
		} else {
			showMessageBox(tr("tray.title"), tr("pause.wrong_password"))
		}
		refreshStatusNow()
	}
}

type pauseResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func postJSON(url string, body map[string]interface{}) (*pauseResponse, error) {
	data, _ := json.Marshal(body)
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var pr pauseResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// showConfig opens the config HTML page in the default browser.
func showConfig() {
	openURLInBrowser(baseURL + "/config-html?lang=" + string(getLang()))
}

// handleReloadConfig sends a POST to /reload-config to force config refresh.
func handleReloadConfig() {
	resp, err := httpClient.Post(baseURL+"/reload-config", "application/json", nil)
	if err != nil {
		showMessageBox(tr("tray.title"), tr("status.unavailable"))
		return
	}
	defer resp.Body.Close()

	var result struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		showMessageBox(tr("tray.title"), tr("status.unavailable"))
		return
	}

	if result.OK {
		showMessageBox(tr("tray.title"), tr("reload.ok"))
	} else {
		showMessageBox(tr("tray.title"), tr("reload.fail")+": "+result.Message)
	}
}

// handleChangePassword prompts for old and new password, sends to service.
func handleChangePassword() {
	oldPwd := inputBoxPassword(tr("password.enter_old"), tr("pause.password_label"))
	if oldPwd == "" {
		return
	}
	newPwd := inputBoxPassword(tr("password.enter_new"), tr("pause.password_label"))
	if newPwd == "" {
		return
	}
	confirmPwd := inputBoxPassword(tr("password.confirm_new"), tr("pause.password_label"))
	if confirmPwd == "" {
		return
	}
	if newPwd != confirmPwd {
		showMessageBox(tr("tray.title"), tr("password.mismatch"))
		return
	}

	data, _ := json.Marshal(map[string]string{
		"old_password": oldPwd,
		"new_password": newPwd,
	})
	resp, err := httpClient.Post(baseURL+"/change-password", "application/json", bytes.NewReader(data))
	if err != nil {
		showMessageBox(tr("tray.title"), tr("status.unavailable"))
		return
	}
	defer resp.Body.Close()

	var result pauseResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		showMessageBox(tr("tray.title"), tr("status.unavailable"))
		return
	}

	if result.OK {
		showMessageBox(tr("tray.title"), tr("password.changed_ok"))
	} else {
		showMessageBox(tr("tray.title"), tr("pause.wrong_password"))
	}
}

type settingsJSON struct {
	HTTPPort int `json:"http_port"`
}

func readPortFromSettings() (int, error) {
	if port, err := readPortFromRegistry(); err == nil && port > 0 {
		return port, nil
	}

	settingsPath := filepath.Join(os.Getenv("ProgramData"), "ParentalControlService", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return defaultPort, err
	}
	var s settingsJSON
	if err := json.Unmarshal(data, &s); err != nil {
		return defaultPort, err
	}
	if s.HTTPPort > 0 {
		return s.HTTPPort, nil
	}
	return defaultPort, nil
}

func readPortFromRegistry() (int, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\ParentalControlService`, registry.QUERY_VALUE)
	if err != nil {
		return 0, err
	}
	defer key.Close()

	val, _, err := key.GetIntegerValue("HTTPPort")
	if err != nil {
		return 0, err
	}
	return int(val), nil
}
