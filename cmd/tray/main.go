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
	"reflect"
	"strconv"
	"strings"
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
	mStatus    *systray.MenuItem
	mConfig    *systray.MenuItem
	mLogs      *systray.MenuItem
	mStats     *systray.MenuItem
	mLang      *systray.MenuItem
	mLangSubs  []*systray.MenuItem // подпункты выбора языка
	mAdmin     *systray.MenuItem   // подменю "Админ"
	mModeHeader *systray.MenuItem  // заголовок "Режим"
	mModeSubs  []*systray.MenuItem // подпункты выбора режима
	mPassword  *systray.MenuItem
	mReload    *systray.MenuItem
	mAddUsage  *systray.MenuItem
	mAddBonus  *systray.MenuItem
	mAdjSleep  *systray.MenuItem
	mQuit      *systray.MenuItem
)

// Режимы сервиса для подменю.
var serviceModes = []string{"normal", "filter_paused", "entertainment_paused", "learning", "unrestricted"}

type statusResponse struct {
	Mode                 string  `json:"mode"`
	ServiceMode          string  `json:"service_mode"`
	DayType              string  `json:"day_type"`
	EntertainmentMinutes int     `json:"entertainment_minutes"`
	LimitMinutes         int     `json:"limit_minutes"`
	MinutesRemaining     int     `json:"minutes_remaining"`
	BonusMinutes         int     `json:"bonus_minutes"`
	ComputerMinutes      int     `json:"computer_minutes"`
	ComputerLimitMinutes int     `json:"computer_limit_minutes"`
	ActiveWindow         *string `json:"active_window,omitempty"`
	SleepTime            *string `json:"sleep_time,omitempty"`
	SleepWindow          string  `json:"sleep_window,omitempty"`
	SleepOverride        string  `json:"sleep_override,omitempty"`
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

	// Подменю выбора языка — размещаем в самом верху.
	mLang = systray.AddMenuItem("Language: "+langNames[getLang()], "")
	mLangSubs = make([]*systray.MenuItem, len(langOrder))
	for i, l := range langOrder {
		mLangSubs[i] = mLang.AddSubMenuItem(langNames[l], "")
		if l == getLang() {
			mLangSubs[i].Check()
		}
	}
	systray.AddSeparator()

	mStatus = systray.AddMenuItem(tr("menu.status"), "")
	mConfig = systray.AddMenuItem(tr("menu.config"), "")
	mLogs = systray.AddMenuItem(tr("menu.logs"), "")
	mStats = systray.AddMenuItem(tr("menu.stats"), "")
	systray.AddSeparator()

	// Подменю "Админ": выбор режима, смена пароля, обновление конфига.
	mAdmin = systray.AddMenuItem(tr("menu.admin"), "")

	// Подпункты выбора режима с заголовком.
	mModeHeader = mAdmin.AddSubMenuItem("⚙ "+tr("menu.mode"), "")
	mModeHeader.Disable()
	mModeSubs = make([]*systray.MenuItem, len(serviceModes))
	for i, m := range serviceModes {
		mModeSubs[i] = mAdmin.AddSubMenuItem("  "+tr("mode."+m), "")
		if m == "normal" {
			mModeSubs[i].Check()
		}
	}
	mModeSeparator := mAdmin.AddSubMenuItem("───────────", "")
	mModeSeparator.Disable()

	mPassword = mAdmin.AddSubMenuItem(tr("menu.password"), "")
	mReload = mAdmin.AddSubMenuItem(tr("menu.reload"), "")

	mSep2 := mAdmin.AddSubMenuItem("───────────", "")
	mSep2.Disable()

	mAddUsage = mAdmin.AddSubMenuItem("🎮 "+tr("menu.add_usage"), "")
	mAddBonus = mAdmin.AddSubMenuItem("⏱ "+tr("menu.add_bonus"), "")
	mAdjSleep = mAdmin.AddSubMenuItem("🌙 "+tr("menu.adj_sleep"), "")

	systray.AddSeparator()
	mQuit = systray.AddMenuItem(tr("menu.quit"), "")

	go pollStatus()

	go func() {
		for {
			select {
			case <-mStatus.ClickedCh:
				showStatusPopup()
			case <-mPassword.ClickedCh:
				go handleChangePassword()
			case <-mReload.ClickedCh:
				go handleReloadConfig()
			case <-mAddUsage.ClickedCh:
				go handleAddUsage()
			case <-mAddBonus.ClickedCh:
				go handleAddBonus()
			case <-mAdjSleep.ClickedCh:
				go handleAdjustSleep()
			case <-mConfig.ClickedCh:
				go showConfig()
			case <-mLogs.ClickedCh:
				openLogs()
			case <-mStats.ClickedCh:
				go showStats()
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()

	// Обработка кликов по подпунктам языка и режима.
	go handleLangClicks()
	go handleModeClicks()
}

func onExit() {}

// handleLangClicks слушает клики по подпунктам языка и переключает язык.
func handleLangClicks() {
	for {
		// Собираем каналы кликов из всех подпунктов.
		cases := make([]<-chan struct{}, len(langOrder))
		for i := range langOrder {
			cases[i] = mLangSubs[i].ClickedCh
		}

		// reflect.Select для динамического количества каналов.
		chosen := waitForLangClick(cases)
		if chosen < 0 || chosen >= len(langOrder) {
			continue
		}

		setLang(langOrder[chosen])

		// Обновляем галочки.
		for i := range langOrder {
			if i == chosen {
				mLangSubs[i].Check()
			} else {
				mLangSubs[i].Uncheck()
			}
		}

		updateMenuLabels()
	}
}

// waitForLangClick ожидает клик по любому из каналов и возвращает индекс.
func waitForLangClick(chs []<-chan struct{}) int {
	cases := make([]reflect.SelectCase, len(chs))
	for i, ch := range chs {
		cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ch)}
	}
	chosen, _, _ := reflect.Select(cases)
	return chosen
}

// handleModeClicks слушает клики по подпунктам режима и переключает режим.
func handleModeClicks() {
	for {
		cases := make([]<-chan struct{}, len(serviceModes))
		for i := range serviceModes {
			cases[i] = mModeSubs[i].ClickedCh
		}

		chosen := waitForLangClick(cases)
		if chosen < 0 || chosen >= len(serviceModes) {
			continue
		}

		mode := serviceModes[chosen]

		// Запрашиваем пароль.
		pwd := inputBoxPassword(tr("pause.enter_password"), tr("pause.password_label"))
		if pwd == "" {
			continue
		}

		// Запрашиваем время (0 = бессрочно).
		minutes := 0
		if mode != "normal" {
			minsStr := inputBox(tr("pause.enter_minutes"), tr("pause.minutes_label"))
			if minsStr != "" {
				if m, err := strconv.Atoi(minsStr); err == nil && m >= 0 {
					minutes = m
				}
			}
		}

		// Отправляем на сервер.
		data, _ := json.Marshal(map[string]interface{}{
			"password": pwd,
			"mode":     mode,
			"minutes":  minutes,
		})
		resp, err := httpClient.Post(baseURL+"/set-mode", "application/json", bytes.NewReader(data))
		if err != nil {
			showMessageBox(tr("tray.title"), tr("status.unavailable"))
			continue
		}
		var result pauseResponse
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		if result.OK {
			// Локализованное сообщение об успехе.
			modeLabel := tr("mode." + mode)
			msg := tr("mode.changed_ok") + ": " + modeLabel
			if minutes > 0 {
				msg += fmt.Sprintf(" (%d %s)", minutes, tr("pause.minutes_label"))
			}
			showMessageBox(tr("tray.title"), msg)
			// Обновляем галочки.
			for i := range serviceModes {
				if i == chosen {
					mModeSubs[i].Check()
				} else {
					mModeSubs[i].Uncheck()
				}
			}
			// Сбрасываем и обновляем.
			time.Sleep(500 * time.Millisecond)
			refreshStatusNow()
		} else {
			showMessageBox(tr("tray.title"), tr("pause.wrong_password"))
		}
	}
}

func updateMenuLabels() {
	mStatus.SetTitle(tr("menu.status"))
	mConfig.SetTitle(tr("menu.config"))
	mLogs.SetTitle(tr("menu.logs"))
	mStats.SetTitle(tr("menu.stats"))
	mAdmin.SetTitle(tr("menu.admin"))
	mModeHeader.SetTitle("⚙ " + tr("menu.mode"))
	mPassword.SetTitle(tr("menu.password"))
	mReload.SetTitle(tr("menu.reload"))
	mAddUsage.SetTitle("🎮 " + tr("menu.add_usage"))
	mAddBonus.SetTitle("⏱ " + tr("menu.add_bonus"))
	mAdjSleep.SetTitle("🌙 " + tr("menu.adj_sleep"))
	mLang.SetTitle("Language: " + langNames[getLang()])
	mQuit.SetTitle(tr("menu.quit"))
	systray.SetTitle(tr("tray.title"))
	systray.SetTooltip(tr("tray.title") + " " + version.GitCommit)

	// Обновляем названия режимов.
	for i, m := range serviceModes {
		mModeSubs[i].SetTitle(tr("mode." + m))
	}

	// Обновляем галочки подменю языка.
	cur := getLang()
	for i, l := range langOrder {
		if l == cur {
			mLangSubs[i].Check()
		} else {
			mLangSubs[i].Uncheck()
		}
	}
}

func pollStatus() {
	statusURL := baseURL + "/status"
	notifURL := baseURL + "/notifications"

	if s, err := fetchStatus(httpClient, statusURL); err == nil {
		updateServiceModeIcon(s)
		systray.SetTooltip(formatStatus(s))
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for range ticker.C {
		if s, err := fetchStatus(httpClient, statusURL); err == nil {
			updateServiceModeIcon(s)
			systray.SetTooltip(formatStatus(s))
		} else {
			systray.SetTooltip(tr("tray.title") + ": " + tr("status.unavailable"))
		}

		// Проверяем уведомления от сервиса.
		checkNotifications(notifURL)
	}
}

// checkNotifications забирает уведомления от сервиса и показывает их.
func checkNotifications(url string) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var notifs []struct {
		Title   string `json:"title"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&notifs); err != nil {
		return
	}
	for _, n := range notifs {
		showMessageBox(n.Title, n.Message)
	}
}

// refreshStatusNow немедленно запрашивает статус и обновляет иконку/тултип.
func refreshStatusNow() {
	statusURL := baseURL + "/status"
	if s, err := fetchStatus(httpClient, statusURL); err == nil {
		updateServiceModeIcon(s)
		systray.SetTooltip(formatStatus(s))
	}
}

// updateServiceModeIcon обновляет иконку трея в зависимости от режима сервиса.
func updateServiceModeIcon(s *statusResponse) {
	mode := s.ServiceMode
	if mode == "" {
		mode = "normal"
	}

	switch mode {
	case "filter_paused":
		systray.SetIcon(hourglassGreenICO)
	case "entertainment_paused":
		systray.SetIcon(hourglassPausedICO)
	case "learning":
		systray.SetIcon(hourglassLearningICO)
	case "unrestricted":
		systray.SetIcon(hourglassUnrestrictedICO)
	default: // normal
		if s.Mode == "inside_window" {
			systray.SetIcon(hourglassGreenICO)
		} else {
			systray.SetIcon(hourglassICO)
		}
	}

	// Обновляем галочки режимов в подменю.
	for i, m := range serviceModes {
		if m == mode {
			mModeSubs[i].Check()
		} else {
			mModeSubs[i].Uncheck()
		}
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
	var lines []string

	// Тип дня.
	lines = append(lines, tr("daytype."+s.DayType))

	// Режим сервиса (если не normal).
	svcMode := s.ServiceMode
	if svcMode == "" {
		svcMode = "normal"
	}
	if svcMode != "normal" {
		modeLine := tr("menu.mode") + ": " + tr("mode."+svcMode)
		if s.PauseUntil != "" {
			if t, err := time.Parse(time.RFC3339, s.PauseUntil); err == nil {
				modeLine += " → " + t.Format("15:04")
			}
		}
		lines = append(lines, modeLine)
	}

	// Окно развлечений.
	if s.ActiveWindow != nil {
		lines = append(lines, tr("status.window")+": "+*s.ActiveWindow)
	}

	// Статус расписания.
	switch s.Mode {
	case "sleep_time":
		lines = append(lines, tr("status.sleep"))
	case "outside_window":
		lines = append(lines, tr("status.outside"))
	case "inside_window":
		spent := s.EntertainmentMinutes
		limit := s.LimitMinutes
		remaining := s.MinutesRemaining
		if svcMode == "entertainment_paused" {
			if limit > 0 {
				lines = append(lines, fmt.Sprintf(tr("status.with_limit"), spent, limit, remaining)+" ⏸")
			}
		} else if limit <= 0 {
			lines = append(lines, fmt.Sprintf(tr("status.no_limit"), spent))
		} else {
			lines = append(lines, fmt.Sprintf(tr("status.with_limit"), spent, limit, remaining))
		}
	}

	// Бонус.
	if s.BonusMinutes != 0 {
		lines = append(lines, fmt.Sprintf(tr("status.bonus"), s.BonusMinutes))
	}

	// Сон.
	if s.SleepWindow != "" {
		sleepLine := tr("status.sleep_label") + ": " + s.SleepWindow
		if s.SleepOverride != "" {
			sleepLine += " → " + s.SleepOverride
		}
		lines = append(lines, sleepLine)
	}

	// Общее время за компьютером.
	if s.ComputerLimitMinutes > 0 {
		lines = append(lines, fmt.Sprintf(tr("status.computer_limit"), s.ComputerMinutes, s.ComputerLimitMinutes))
	} else {
		lines = append(lines, fmt.Sprintf(tr("status.computer"), s.ComputerMinutes))
	}

	return strings.Join(lines, "\n")
}

func showStatusPopup() {
	statusURL := baseURL + "/status"
	text := tr("status.unavailable")
	if s, err := fetchStatus(httpClient, statusURL); err == nil {
		text = formatStatus(s)
	}
	showMessageBox(tr("tray.title"), text)
}

// translateDayType возвращает локализованное название типа дня для трея.
func translateDayType(dayType string) string {
	switch dayType {
	case "workday":
		return tr("daytype.workday")
	case "weekend":
		return tr("daytype.weekend")
	case "holiday":
		return tr("daytype.holiday")
	default:
		return ""
	}
}

func openLogs() {
	openURLInBrowser(baseURL + "/logs-html?lang=" + string(getLang()))
}

// showStats opens the stats HTML page in the default browser.
func showStats() {
	openURLInBrowser(baseURL + "/stats-html?lang=" + string(getLang()))
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

// handleAddUsage — добавить использованное время развлечений.
func handleAddUsage() {
	// Получаем текущий статус для отображения.
	s := fetchCurrentStatus()
	info := ""
	if s != nil {
		info = fmt.Sprintf("%s: %d/%d %s", tr("status.window"), s.EntertainmentMinutes, s.LimitMinutes, tr("pause.minutes_label"))
		if s.BonusMinutes != 0 {
			info += fmt.Sprintf(" (%s: %+d)", tr("status.bonus_short"), s.BonusMinutes)
		}
		info += "\n\n"
	}

	pwd := inputBoxPassword(tr("pause.enter_password"), tr("pause.password_label"))
	if pwd == "" {
		return
	}
	minsStr := inputBox(info+tr("adj.enter_usage_mins"), tr("pause.minutes_label"))
	if minsStr == "" {
		return
	}
	mins, err := strconv.Atoi(minsStr)
	if err != nil || mins < 1 {
		showMessageBox(tr("tray.title"), tr("pause.invalid_minutes"))
		return
	}
	reason := inputBox(tr("adj.enter_reason"), tr("adj.reason_label"))

	resp, err := postJSON(baseURL+"/add-usage", map[string]interface{}{
		"password": pwd, "minutes": mins, "reason": reason,
	})
	if err != nil {
		showMessageBox(tr("tray.title"), tr("status.unavailable"))
		return
	}
	if resp.OK {
		showMessageBox(tr("tray.title"), tr("adj.usage_ok"))
	} else {
		showMessageBox(tr("tray.title"), tr("pause.wrong_password"))
	}
	refreshStatusNow()
}

// handleAddBonus — добавить/убрать бонусное время.
func handleAddBonus() {
	s := fetchCurrentStatus()
	info := ""
	if s != nil {
		info = fmt.Sprintf("%s: %d %s", tr("status.bonus_short"), s.BonusMinutes, tr("pause.minutes_label"))
		info += "\n\n"
	}

	pwd := inputBoxPassword(tr("pause.enter_password"), tr("pause.password_label"))
	if pwd == "" {
		return
	}
	minsStr := inputBox(info+tr("adj.enter_bonus_mins"), tr("pause.minutes_label"))
	if minsStr == "" {
		return
	}
	mins, err := strconv.Atoi(minsStr)
	if err != nil || mins == 0 {
		showMessageBox(tr("tray.title"), tr("pause.invalid_minutes"))
		return
	}
	reason := inputBox(tr("adj.enter_reason"), tr("adj.reason_label"))

	resp, err := postJSON(baseURL+"/adjust-bonus", map[string]interface{}{
		"password": pwd, "minutes": mins, "reason": reason,
	})
	if err != nil {
		showMessageBox(tr("tray.title"), tr("status.unavailable"))
		return
	}
	if resp.OK {
		showMessageBox(tr("tray.title"), tr("adj.bonus_ok"))
	} else {
		showMessageBox(tr("tray.title"), tr("pause.wrong_password"))
	}
	refreshStatusNow()
}

// handleAdjustSleep — изменить время сна на сегодня.
func handleAdjustSleep() {
	pwd := inputBoxPassword(tr("pause.enter_password"), tr("pause.password_label"))
	if pwd == "" {
		return
	}

	// Получаем текущее время сна для сегодняшнего дня.
	currentVal := ""
	resp, err := httpClient.Get(baseURL + "/config")
	if err == nil {
		defer resp.Body.Close()
		var cfg struct {
			Schedule struct {
				SleepTimes []struct {
					Days  []string `json:"days"`
					Start string   `json:"start"`
					End   string   `json:"end"`
				} `json:"sleep_times"`
			} `json:"schedule"`
		}
		if json.NewDecoder(resp.Body).Decode(&cfg) == nil {
			today := strings.ToLower(time.Now().Weekday().String())
			for _, st := range cfg.Schedule.SleepTimes {
				for _, d := range st.Days {
					if d == today {
						currentVal = st.Start + "-" + st.End
						break
					}
				}
				if currentVal != "" {
					break
				}
			}
		}
	}

	sleepStr := inputBoxWithDefault(tr("adj.enter_sleep_range"), tr("adj.time_label"), currentVal)
	if sleepStr == "" {
		return
	}

	sleepStr = strings.ReplaceAll(sleepStr, " ", "")
	parts := strings.SplitN(sleepStr, "-", 2)
	start, end := "", ""
	if len(parts) == 2 {
		start = parts[0]
		end = parts[1]
	}
	if start == "" && end == "" {
		return
	}

	reason := inputBox(tr("adj.enter_reason"), tr("adj.reason_label"))

	result, err := postJSON(baseURL+"/adjust-sleep", map[string]interface{}{
		"password": pwd, "new_start": start, "new_end": end, "reason": reason,
	})
	if err != nil {
		showMessageBox(tr("tray.title"), tr("status.unavailable"))
		return
	}
	if result.OK {
		showMessageBox(tr("tray.title"), tr("adj.sleep_ok"))
	} else {
		showMessageBox(tr("tray.title"), tr("pause.wrong_password"))
	}
	refreshStatusNow()
}

// fetchCurrentStatus получает текущий статус для отображения в диалогах.
func fetchCurrentStatus() *statusResponse {
	s, err := fetchStatus(httpClient, baseURL+"/status")
	if err != nil {
		return nil
	}
	return s
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
