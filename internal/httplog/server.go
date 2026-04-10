package httplog

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"parental-control-service/internal/logger"
)

const (
	defaultLimit    = 100
	shutdownTimeout = 10 * time.Second
)

// LogProvider abstracts access to recent log entries.
type LogProvider interface {
	RecentEntries(limit int) []logger.LogEntry
}

// FileLogProvider abstracts access to log entries from files with date filtering.
type FileLogProvider interface {
	ReadEntries(date string, limit int) []logger.LogEntry
}

// StatusProvider abstracts access to the current service status.
type StatusProvider interface {
	CurrentStatus() StatusResponse
}

// ConfigProvider abstracts access to the current service configuration.
type ConfigProvider interface {
	CurrentConfigJSON() ConfigResponse
}

// StatsProvider abstracts access to usage statistics.
type StatsProvider interface {
	GetDayStats(date string) interface{}
	GetWeekStats() interface{}
}

// PauseProvider abstracts pause/unpause and mode operations.
type PauseProvider interface {
	Pause(password string, minutes int) (bool, string)
	Unpause(password string) (bool, string)
	IsPaused() bool
	SetServiceMode(password, mode string, minutes int) (bool, string)
	GetServiceMode() string
	DrainNotifications() []Notification
	SelfPauseEntertainment() (bool, string)
	SelfUnpauseEntertainment() (bool, string)
}

// ConfigReloader abstracts forced config reload.
type ConfigReloader interface {
	ReloadConfig(ctx context.Context) (string, error)
}

// PasswordChanger abstracts password change operations.
type PasswordChanger interface {
	ChangePassword(oldPassword, newPassword string) (bool, string)
}

// ConfigURLChanger abstracts config URL change operations.
type ConfigURLChanger interface {
	ChangeConfigURL(password, newURL string) (bool, string)
	GetConfigURL() string
}

// LearningProvider abstracts access to learning mode data.
type LearningProvider interface {
	GetCurrentReport() interface{}
	ListReports() []string
	ReadReport(name string) ([]byte, error)
	ClearReports() error
}

// AdjustmentProvider abstracts manual time adjustments.
type AdjustmentProvider interface {
	AddUsage(password string, minutes int, reason string) (bool, string)
	AdjustBonus(password string, minutes int, reason string) (bool, string)
	AdjustSleep(password string, newStart, newEnd, reason string) (bool, string)
}

// и возвращает список HWND окон для закрытия и причину блокировки.
type BrowserActivityReceiver interface {
	ReceiveBrowserURLs(urls []BrowserURLEntry) (closeHWNDs []uintptr, reason string)
}

// BrowserURLEntry — запись об открытом URL в браузере.
type BrowserURLEntry struct {
	Browser string  `json:"browser"`
	PID     uint32  `json:"pid"`
	URL     string  `json:"url"`
	HWND    uintptr `json:"hwnd"`
}

// BrowserURLReport — отчёт от tray-приложения.
type BrowserURLReport struct {
	URLs []BrowserURLEntry `json:"urls"`
}

// BrowserCloseResponse — ответ сервиса со списком окон для закрытия.
type BrowserCloseResponse struct {
	CloseHWNDs []uintptr `json:"close_hwnds"`
	Reason     string    `json:"reason"`
}

// HTTPLogServer serves log and status data over HTTP, restricted to LAN clients.
type HTTPLogServer struct {
	server          *http.Server
	port            int
	logProvider     LogProvider
	fileLogProvider FileLogProvider
	statusProvider  StatusProvider
	configProvider  ConfigProvider
	browserReceiver BrowserActivityReceiver
	statsProvider   StatsProvider
	pauseProvider   PauseProvider
	configReloader  ConfigReloader
	passwordChanger PasswordChanger
	configURLChanger ConfigURLChanger
	learningProvider LearningProvider
	adjustmentProvider AdjustmentProvider
}

// LAN CIDR ranges allowed to access the server.
var lanCIDRs []*net.IPNet

func init() {
	cidrs := []string{
		"192.168.0.0/16",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"127.0.0.0/8",
	}
	for _, cidr := range cidrs {
		_, ipNet, _ := net.ParseCIDR(cidr)
		lanCIDRs = append(lanCIDRs, ipNet)
	}
}

// NewHTTPLogServer creates a new HTTPLogServer on the given port.
func NewHTTPLogServer(port int, logProvider LogProvider, statusProvider StatusProvider) *HTTPLogServer {
	return &HTTPLogServer{
		port:           port,
		logProvider:    logProvider,
		statusProvider: statusProvider,
	}
}

// SetStatusProvider sets the status provider after construction.
// This allows wiring the Service as StatusProvider after both are created.
func (h *HTTPLogServer) SetStatusProvider(sp StatusProvider) {
	h.statusProvider = sp
}

// SetLogProvider sets the log provider after construction.
func (h *HTTPLogServer) SetLogProvider(lp LogProvider) {
	h.logProvider = lp
}

// SetFileLogProvider sets the file log provider after construction.
func (h *HTTPLogServer) SetFileLogProvider(flp FileLogProvider) {
	h.fileLogProvider = flp
}

// SetConfigProvider sets the config provider after construction.
func (h *HTTPLogServer) SetConfigProvider(cp ConfigProvider) {
	h.configProvider = cp
}

// SetStatsProvider sets the stats provider after construction.
func (h *HTTPLogServer) SetStatsProvider(sp StatsProvider) {
	h.statsProvider = sp
}

// SetPauseProvider sets the pause provider after construction.
func (h *HTTPLogServer) SetPauseProvider(pp PauseProvider) {
	h.pauseProvider = pp
}

// SetConfigReloader sets the config reloader after construction.
func (h *HTTPLogServer) SetConfigReloader(cr ConfigReloader) {
	h.configReloader = cr
}

// SetPasswordChanger sets the password changer after construction.
func (h *HTTPLogServer) SetPasswordChanger(pc PasswordChanger) {
	h.passwordChanger = pc
}

// SetConfigURLChanger sets the config URL changer after construction.
func (h *HTTPLogServer) SetConfigURLChanger(cu ConfigURLChanger) {
	h.configURLChanger = cu
}

// SetLearningProvider sets the learning data provider after construction.
func (h *HTTPLogServer) SetLearningProvider(lp LearningProvider) {
	h.learningProvider = lp
}

// SetAdjustmentProvider sets the adjustment provider after construction.
func (h *HTTPLogServer) SetAdjustmentProvider(ap AdjustmentProvider) {
	h.adjustmentProvider = ap
}

// SetBrowserReceiver sets the browser activity receiver after construction.
func (h *HTTPLogServer) SetBrowserReceiver(br BrowserActivityReceiver) {
	h.browserReceiver = br
}

// Start starts the HTTP server in a background goroutine and returns immediately.
func (h *HTTPLogServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleDashboard)
	mux.HandleFunc("/logs", h.handleLogs)
	mux.HandleFunc("/logs-html", h.handleLogsHTML)
	mux.HandleFunc("/status", h.handleStatus)
	mux.HandleFunc("/config", h.handleConfig)
	mux.HandleFunc("/config-html", h.handleConfigHTML)
	mux.HandleFunc("/stats", h.handleStats)
	mux.HandleFunc("/stats-html", h.handleStatsHTML)
	mux.HandleFunc("/pause", h.handlePause)
	mux.HandleFunc("/unpause", h.handleUnpause)
	mux.HandleFunc("/set-mode", h.handleSetMode)
	mux.HandleFunc("/reload-config", h.handleReloadConfig)
	mux.HandleFunc("/change-password", h.handleChangePassword)
	mux.HandleFunc("/change-config-url", h.handleChangeConfigURL)
	mux.HandleFunc("/admin-html", h.handleAdminHTML)
	mux.HandleFunc("/browser-activity", h.handleBrowserActivity)
	mux.HandleFunc("/learning/current", h.handleLearningCurrent)
	mux.HandleFunc("/learning/reports", h.handleLearningReports)
	mux.HandleFunc("/learning/report/", h.handleLearningReport)
	mux.HandleFunc("/learning/clear", h.handleLearningClear)
	mux.HandleFunc("/add-usage", h.handleAddUsage)
	mux.HandleFunc("/adjust-bonus", h.handleAdjustBonus)
	mux.HandleFunc("/adjust-sleep", h.handleAdjustSleep)
	mux.HandleFunc("/notifications", h.handleNotifications)
	mux.HandleFunc("/self-pause", h.handleSelfPause)

	h.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", h.port),
		Handler: h.lanOnlyMiddleware(mux),
	}

	go func() {
		if err := h.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// Log error; in production this would go to the event log.
			fmt.Printf("[ERROR] httplog: server error: %v\n", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the HTTP server with a 10-second timeout.
func (h *HTTPLogServer) Stop(ctx context.Context) error {
	if h.server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	return h.server.Shutdown(shutdownCtx)
}

// lanOnlyMiddleware rejects requests from non-LAN IP addresses with 403 Forbidden.
func (h *HTTPLogServer) lanOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if !IsLANAddress(host) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// IsLANAddress reports whether the given IP string belongs to a LAN range
// (192.168.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 127.0.0.0/8).
func IsLANAddress(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidr := range lanCIDRs {
		if cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

// handleDashboard handles GET / — main dashboard page with navigation to all sections.
func (h *HTTPLogServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	lang := r.URL.Query().Get("lang")
	lang = normalizeLang(lang)

	ui := dashboardUILabels(lang)

	// Fetch live status.
	var statusMode, statusText string
	if h.statusProvider != nil {
		st := h.statusProvider.CurrentStatus()
		statusMode = st.Mode
		switch st.Mode {
		case "inside_window":
			statusText = dashboardInsideWindowText(lang, st.EntertainmentMinutes, st.LimitMinutes, st.MinutesRemaining)
		case "outside_window":
			statusText = ui.modeOutside
		case "sleep_time":
			statusText = ui.modeSleep
		default:
			statusText = ui.modeUnknown
		}
	} else {
		statusText = ui.modeUnknown
	}

	// Mode color.
	modeColor := "#a6e3a1" // green
	switch statusMode {
	case "sleep_time":
		modeColor = "#f38ba8" // red
	case "outside_window":
		modeColor = "#fab387" // orange
	}

	today := time.Now().Format("2006-01-02")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8">
<title>%s</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:'Segoe UI',sans-serif;background:#1e1e2e;color:#cdd6f4;padding:24px;min-height:100vh}
.header{text-align:center;margin-bottom:28px}
.header h1{font-size:1.5rem;color:#89b4fa;margin-bottom:6px}
.header .ver{color:#6c7086;font-size:0.8rem}
.status-card{background:#313244;border-radius:8px;padding:16px 20px;margin-bottom:24px;border-left:4px solid %s}
.status-card .mode{font-size:1rem;font-weight:600;color:%s}
.status-card .detail{color:#a6adc8;font-size:0.85rem;margin-top:4px}
.status-card .pause-badge{display:inline-block;background:#f38ba8;color:#1e1e2e;border-radius:4px;padding:2px 10px;font-size:0.8rem;font-weight:600;margin-left:8px}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:16px}
.card{background:#313244;border-radius:8px;padding:20px;text-decoration:none;color:#cdd6f4;transition:background 0.15s,transform 0.1s;display:flex;flex-direction:column;gap:8px}
.card:hover{background:#45475a;transform:translateY(-2px)}
.card .icon{font-size:2rem}
.card .label{font-size:1rem;font-weight:600;color:#89b4fa}
.card .desc{font-size:0.82rem;color:#a6adc8}
.lang-switch{text-align:center;margin-top:24px}
.lang-switch a{color:#89b4fa;text-decoration:none;font-size:0.85rem}
.lang-switch a:hover{text-decoration:underline}
</style></head><body>`, ui.title, modeColor, modeColor)

	// Top bar: day type + language switcher.
	fmt.Fprint(w, topBarHTML(h.statusProvider, lang, "/", ""))

	fmt.Fprintf(w, `<div class="header"><h1>%s</h1></div>`, ui.heading)

	// Day type + mode badge.
	// (уже в topBar)

	// Status card.
	fmt.Fprintf(w, `<div class="status-card"><div class="mode">%s`, htmlEscape(statusText))
	fmt.Fprint(w, `</div></div>`)

	// Pause banner (same style as logs/stats pages).
	if h.statusProvider != nil {
		st := h.statusProvider.CurrentStatus()
		if st.Paused {
			label, until := pauseBannerLabels(lang)
			timeStr := ""
			if st.PauseUntil != "" {
				if t, err := time.Parse(time.RFC3339, st.PauseUntil); err == nil {
					timeStr = fmt.Sprintf(" — %s %s", until, t.Format("15:04"))
				}
			}
			fmt.Fprintf(w, `<div style="background:#f38ba8;color:#1e1e2e;border-radius:6px;padding:8px 16px;margin-bottom:12px;font-size:0.9rem;font-weight:600">%s%s</div>`,
				htmlEscape(label), htmlEscape(timeStr))
		}
	}

	// Navigation cards.
	fmt.Fprint(w, `<div class="cards">`)

	cards := []struct{ icon, label, desc, href string }{
		{"📋", ui.cardLogs, ui.cardLogsDesc, fmt.Sprintf("/logs-html?lang=%s", lang)},
		{"📊", ui.cardStats, ui.cardStatsDesc, fmt.Sprintf("/stats-html?date=%s&lang=%s", today, lang)},
		{"⚙️", ui.cardConfig, ui.cardConfigDesc, fmt.Sprintf("/config-html?lang=%s", lang)},
		{"🔧", ui.cardAdmin, ui.cardAdminDesc, fmt.Sprintf("/admin-html?lang=%s", lang)},
		{"📡", ui.cardAPI, ui.cardAPIDesc, "/status"},
	}
	for _, c := range cards {
		fmt.Fprintf(w, `<a class="card" href="%s"><div class="icon">%s</div><div class="label">%s</div><div class="desc">%s</div></a>`,
			c.href, c.icon, htmlEscape(c.label), htmlEscape(c.desc))
	}
	fmt.Fprint(w, `</div>`)

	fmt.Fprint(w, `</body></html>`)
}

type dashboardUI struct {
	title, heading                                     string
	modeOutside, modeSleep, modeUnknown, paused        string
	cardLogs, cardLogsDesc                             string
	cardStats, cardStatsDesc                           string
	cardConfig, cardConfigDesc                         string
	cardAdmin, cardAdminDesc                           string
	cardAPI, cardAPIDesc                               string
}

func dashboardUILabels(lang string) dashboardUI {
	switch lang {
	case "ru":
		return dashboardUI{
			title: "Родительский контроль", heading: "Родительский контроль",
			modeOutside: "Нет окна развлечений", modeSleep: "Время сна", modeUnknown: "Загрузка...", paused: "ПАУЗА",
			cardLogs: "Логи", cardLogsDesc: "Журнал событий и блокировок",
			cardStats: "Статистика", cardStatsDesc: "Время использования по приложениям и сайтам",
			cardConfig: "Конфигурация", cardConfigDesc: "Разрешённые программы, сайты и расписание",
			cardAdmin: "Управление", cardAdminDesc: "Пауза, смена пароля, URL конфигурации",
			cardAPI: "API / Статус", cardAPIDesc: "JSON-данные о текущем состоянии сервиса",
		}
	case "it":
		return dashboardUI{
			title: "Controllo Genitori", heading: "Controllo Genitori",
			modeOutside: "Nessuna finestra di intrattenimento", modeSleep: "Ora di dormire", modeUnknown: "Caricamento...", paused: "IN PAUSA",
			cardLogs: "Log", cardLogsDesc: "Registro eventi e blocchi",
			cardStats: "Statistiche", cardStatsDesc: "Tempo di utilizzo per app e siti",
			cardConfig: "Configurazione", cardConfigDesc: "App, siti e orari consentiti",
			cardAdmin: "Gestione", cardAdminDesc: "Pausa, password, URL configurazione",
			cardAPI: "API / Stato", cardAPIDesc: "Dati JSON sullo stato attuale del servizio",
		}
	case "es":
		return dashboardUI{
			title: "Control Parental", heading: "Control Parental",
			modeOutside: "Sin ventana de entretenimiento", modeSleep: "Hora de dormir", modeUnknown: "Cargando...", paused: "EN PAUSA",
			cardLogs: "Registros", cardLogsDesc: "Registro de eventos y bloqueos",
			cardStats: "Estadísticas", cardStatsDesc: "Tiempo de uso por apps y sitios",
			cardConfig: "Configuración", cardConfigDesc: "Apps, sitios y horarios permitidos",
			cardAdmin: "Administración", cardAdminDesc: "Pausa, contraseña, URL de configuración",
			cardAPI: "API / Estado", cardAPIDesc: "Datos JSON del estado actual del servicio",
		}
	case "de":
		return dashboardUI{
			title: "Kindersicherung", heading: "Kindersicherung",
			modeOutside: "Kein Unterhaltungsfenster", modeSleep: "Schlafenszeit", modeUnknown: "Laden...", paused: "PAUSIERT",
			cardLogs: "Protokolle", cardLogsDesc: "Ereignis- und Sperrprotokoll",
			cardStats: "Statistiken", cardStatsDesc: "Nutzungszeit nach Apps und Seiten",
			cardConfig: "Konfiguration", cardConfigDesc: "Erlaubte Apps, Seiten und Zeitplan",
			cardAdmin: "Verwaltung", cardAdminDesc: "Pause, Passwort, Konfigurations-URL",
			cardAPI: "API / Status", cardAPIDesc: "JSON-Daten zum aktuellen Dienststatus",
		}
	case "pl":
		return dashboardUI{
			title: "Kontrola Rodzicielska", heading: "Kontrola Rodzicielska",
			modeOutside: "Brak okna rozrywki", modeSleep: "Pora snu", modeUnknown: "Ładowanie...", paused: "PAUZA",
			cardLogs: "Logi", cardLogsDesc: "Dziennik zdarzeń i blokad",
			cardStats: "Statystyki", cardStatsDesc: "Czas użytkowania wg aplikacji i stron",
			cardConfig: "Konfiguracja", cardConfigDesc: "Dozwolone aplikacje, strony i harmonogram",
			cardAdmin: "Zarządzanie", cardAdminDesc: "Pauza, hasło, URL konfiguracji",
			cardAPI: "API / Status", cardAPIDesc: "Dane JSON o bieżącym stanie usługi",
		}
	case "zh-TW":
		return dashboardUI{
			title: "家長控制", heading: "家長控制",
			modeOutside: "目前沒有娛樂時段", modeSleep: "睡眠時間", modeUnknown: "載入中...", paused: "已暫停",
			cardLogs: "日誌", cardLogsDesc: "事件與封鎖記錄",
			cardStats: "統計", cardStatsDesc: "按應用程式和網站的使用時間",
			cardConfig: "設定", cardConfigDesc: "允許的應用程式、網站和時間表",
			cardAdmin: "管理", cardAdminDesc: "暫停、密碼、設定網址",
			cardAPI: "API / 狀態", cardAPIDesc: "服務當前狀態的 JSON 資料",
		}
	default:
		return dashboardUI{
			title: "Parental Control", heading: "Parental Control",
			modeOutside: "No entertainment window", modeSleep: "Sleep time", modeUnknown: "Loading...", paused: "PAUSED",
			cardLogs: "Logs", cardLogsDesc: "Event log and block history",
			cardStats: "Statistics", cardStatsDesc: "Usage time by apps and sites",
			cardConfig: "Configuration", cardConfigDesc: "Allowed apps, sites and schedule",
			cardAdmin: "Admin", cardAdminDesc: "Pause, password, config URL management",
			cardAPI: "API / Status", cardAPIDesc: "JSON data about current service state",
		}
	}
}

// handleLogs handles GET /logs. It accepts an optional ?limit=N query parameter.
func (h *HTTPLogServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.logProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(LogsResponse{Entries: []logger.LogEntry{}, Total: 0})
		return
	}

	limit := defaultLimit
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}

	entries := h.logProvider.RecentEntries(limit)
	if entries == nil {
		entries = []logger.LogEntry{}
	}

	resp := LogsResponse{
		Entries: entries,
		Total:   len(entries),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleStatus handles GET /status. It returns the current service status as JSON.
func (h *HTTPLogServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.statusProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(StatusResponse{Mode: "starting", ActiveProcesses: []string{}})
		return
	}

	status := h.statusProvider.CurrentStatus()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleConfig handles GET /config. Returns the current configuration.
func (h *HTTPLogServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.configProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"apps":null,"sites":null,"schedule":null}`))
		return
	}

	resp := h.configProvider.CurrentConfigJSON()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleConfigHTML handles GET /config-html — HTML page showing current configuration.
// Params: ?type=all|apps|sites|schedule, ?lang=en|ru
func (h *HTTPLogServer) handleConfigHTML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	typeFilter := r.URL.Query().Get("type")
	if typeFilter == "" {
		typeFilter = "all"
	}
	lang := r.URL.Query().Get("lang")
	lang = normalizeLang(lang)

	ui := configUILabels(lang)

	// Get config JSON sections.
	var appsJSON, sitesJSON, schedJSON json.RawMessage
	if h.configProvider != nil {
		cfg := h.configProvider.CurrentConfigJSON()
		appsJSON = cfg.Apps
		sitesJSON = cfg.Sites
		schedJSON = cfg.Schedule
	}

	// Parse apps.
	type appItem struct {
		Name       string `json:"name"`
		Executable string `json:"executable"`
		Path       string `json:"path,omitempty"`
	}
	type appsWrapper struct {
		Apps []appItem `json:"apps"`
	}
	var apps appsWrapper
	if appsJSON != nil {
		json.Unmarshal(appsJSON, &apps)
	}

	// Parse sites.
	type siteItem struct {
		Domain            string   `json:"domain"`
		IncludeSubdomains bool     `json:"include_subdomains"`
		AllowedPaths      []string `json:"allowed_paths,omitempty"`
		Category          string   `json:"category,omitempty"`
	}
	type sitesWrapper struct {
		Sites []siteItem `json:"sites"`
	}
	var sites sitesWrapper
	if sitesJSON != nil {
		json.Unmarshal(sitesJSON, &sites)
	}

	// Parse schedule.
	type timeWindow struct {
		Days         []string `json:"days"`
		Start        string   `json:"start"`
		End          string   `json:"end"`
		LimitMinutes int      `json:"limit_minutes,omitempty"`
	}
	type schedWrapper struct {
		EntertainmentWindows    []timeWindow `json:"entertainment_windows"`
		SleepTimes              []timeWindow `json:"sleep_times"`
		Holidays                []struct {
			Date string `json:"date"`
			Name string `json:"name,omitempty"`
		} `json:"holidays"`
		HolidayWindows          []timeWindow `json:"holiday_windows"`
		HolidaySleepTimes       []timeWindow `json:"holiday_sleep_times"`
		WarningBeforeMinutes    int          `json:"warning_before_minutes"`
		SleepWarningBeforeMin   int          `json:"sleep_warning_before_minutes"`
		FullLogging             bool         `json:"full_logging"`
		EntertainmentApps       []string     `json:"entertainment_apps"`
		Vacations               []struct {
			Name       string       `json:"name,omitempty"`
			StartDate  string       `json:"start_date"`
			EndDate    string       `json:"end_date"`
			Windows    []timeWindow `json:"windows"`
			SleepTimes []timeWindow `json:"sleep_times,omitempty"`
			PreDayStart string      `json:"pre_day_start,omitempty"`
			PreDayEnd   string      `json:"pre_day_end,omitempty"`
		} `json:"vacations"`
	}
	var sched schedWrapper
	if schedJSON != nil {
		json.Unmarshal(schedJSON, &sched)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html><html><head><meta charset="utf-8">
<title>`)
	fmt.Fprint(w, htmlEscape(ui.title))
	fmt.Fprint(w, `</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:'Segoe UI',sans-serif;background:#1e1e2e;color:#cdd6f4;padding:16px}
h2{font-size:1.1rem;margin:18px 0 8px;color:#89b4fa}
h2:first-of-type{margin-top:8px}
.filters{margin-bottom:14px;display:flex;gap:12px;align-items:center;flex-wrap:wrap}
.filters select{background:#313244;color:#cdd6f4;border:1px solid #45475a;border-radius:4px;padding:4px 8px;font-size:0.85rem}
.filters button{background:#89b4fa;color:#1e1e2e;border:none;border-radius:4px;padding:5px 14px;cursor:pointer;font-size:0.85rem;font-weight:600}
.filters button:hover{background:#74c7ec}
.filters label{color:#a6adc8;font-size:0.85rem}
table{width:100%;border-collapse:collapse;font-size:0.82rem;margin-bottom:16px}
th{background:#313244;color:#a6adc8;text-align:left;padding:6px 10px;position:sticky;top:0}
td{padding:4px 10px;border-bottom:1px solid #313244;word-break:break-all}
tr:hover{background:#313244}
.tag{display:inline-block;background:#45475a;color:#cdd6f4;border-radius:3px;padding:1px 6px;font-size:0.75rem;margin:1px 2px}
.yes{color:#a6e3a1} .no{color:#f38ba8}
.page-switch{margin-bottom:12px}
.page-switch a{color:#1e1e2e;background:#89b4fa;text-decoration:none;font-size:0.85rem;padding:4px 14px;border-radius:4px;font-weight:600}
.page-switch a:hover{background:#74c7ec}
.page-switch span{color:#f9e2af;font-size:0.85rem;padding:4px 14px;border:1px solid #45475a;border-radius:4px;font-weight:600}
.info-row{color:#a6adc8;font-size:0.85rem;margin:4px 0}
.info-row span{color:#f9e2af;font-weight:600}
</style></head><body>`)

	// Top bar: day type + language switcher.
	fmt.Fprint(w, topBarHTML(h.statusProvider, lang, "/config-html", "type="+typeFilter))

	// Page switcher.
	today := time.Now().Format("2006-01-02")
	fmt.Fprintf(w, `<div class="page-switch"><a href="/logs-html?lang=%s">%s</a> <a href="/stats-html?date=%s&lang=%s">%s</a> <span>%s</span> <a href="/admin-html?lang=%s">%s</a></div>`,
		lang, ui.logsTab, today, lang, ui.statsTab, ui.configTab, lang, ui.adminTab)

	// Pause banner.
	writePauseBanner(w, h.statusProvider, lang)

	// Reload config button.
	fmt.Fprintf(w, `<div style="margin-bottom:12px"><button onclick="reloadCfg()" style="background:#89b4fa;color:#1e1e2e;border:none;border-radius:4px;padding:6px 18px;cursor:pointer;font-size:0.85rem;font-weight:600">%s</button><span id="reloadMsg" style="margin-left:10px;font-size:0.85rem"></span></div>
<script>async function reloadCfg(){var m=document.getElementById('reloadMsg');m.textContent='...';try{var r=await fetch('/reload-config',{method:'POST'});var j=await r.json();m.textContent=j.message;m.style.color=j.ok?'#a6e3a1':'#f38ba8'}catch(e){m.textContent='Error';m.style.color='#f38ba8'}}</script>`,
		ui.reloadBtn)

	// Filter.
	fmt.Fprintf(w, `<div class="filters">
<label>%s: <select id="typeSelect">
<option value="all"%s>%s</option>
<option value="apps"%s>%s</option>
<option value="sites"%s>%s</option>
<option value="schedule"%s>%s</option>
</select></label>
<button onclick="applyFilter()">%s</button>
</div>`,
		ui.filterLabel,
		selAttr(typeFilter, "all"), ui.filterAll,
		selAttr(typeFilter, "apps"), ui.filterApps,
		selAttr(typeFilter, "sites"), ui.filterSites,
		selAttr(typeFilter, "schedule"), ui.filterSchedule,
		ui.applyBtn)

	fmt.Fprintf(w, `<script>
function applyFilter(){
  var t=document.getElementById('typeSelect').value;
  location.href='?type='+t+'&lang=%s';
}
</script>`, lang)

	// --- Schedule section --- (первым, наверху)
	if typeFilter == "all" || typeFilter == "schedule" {
		fmt.Fprintf(w, `<h2>%s</h2>`, ui.schedTitle)

		// Entertainment windows.
		if len(sched.EntertainmentWindows) > 0 {
			fmt.Fprintf(w, `<h2 style="font-size:0.95rem;color:#f9e2af">%s</h2>`, ui.entWindows)
			fmt.Fprintf(w, `<table><tr><th>%s</th><th>%s</th><th>%s</th></tr>`,
				ui.colDays, ui.colTimeRange, ui.colLimit)
			for _, ew := range sched.EntertainmentWindows {
				days := translateDays(ew.Days, lang)
				timeRange := fmt.Sprintf("%s — %s", ew.Start, ew.End)
				limit := fmt.Sprintf("%d %s", ew.LimitMinutes, ui.min)
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td></tr>`,
					htmlEscape(days), timeRange, limit)
			}
			fmt.Fprint(w, `</table>`)
		}

		// Sleep times.
		if len(sched.SleepTimes) > 0 {
			fmt.Fprintf(w, `<h2 style="font-size:0.95rem;color:#f9e2af">%s</h2>`, ui.sleepTimes)
			fmt.Fprintf(w, `<table><tr><th>%s</th><th>%s</th></tr>`,
				ui.colDays, ui.colTimeRange)
			for _, st := range sched.SleepTimes {
				days := translateDays(st.Days, lang)
				timeRange := fmt.Sprintf("%s — %s", st.Start, st.End)
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`,
					htmlEscape(days), timeRange)
			}
			fmt.Fprint(w, `</table>`)
		}

		// Settings.
		fmt.Fprintf(w, `<div class="info-row">%s: <span>%d %s</span></div>`,
			ui.warnBefore, sched.WarningBeforeMinutes, ui.min)
		fmt.Fprintf(w, `<div class="info-row">%s: <span>%d %s</span></div>`,
			ui.sleepWarnBefore, sched.SleepWarningBeforeMin, ui.min)

		logVal := ui.valNo
		if sched.FullLogging {
			logVal = ui.valYes
		}
		fmt.Fprintf(w, `<div class="info-row">%s: <span>%s</span></div>`,
			ui.fullLogging, logVal)

		// Entertainment apps.
		if len(sched.EntertainmentApps) > 0 {
			fmt.Fprintf(w, `<h2 style="font-size:0.95rem;color:#f9e2af">%s (%d)</h2>`, ui.entApps, len(sched.EntertainmentApps))
			fmt.Fprint(w, `<div>`)
			for _, a := range sched.EntertainmentApps {
				fmt.Fprintf(w, `<span class="tag">%s</span>`, htmlEscape(a))
			}
			fmt.Fprint(w, `</div>`)
		}

		// Holidays.
		if len(sched.Holidays) > 0 {
			fmt.Fprintf(w, `<h2 style="font-size:0.95rem;color:#f38ba8">🎄 %s (%d)</h2>`, ui.holidays, len(sched.Holidays))
			fmt.Fprint(w, `<div style="margin-bottom:8px">`)
			for _, h := range sched.Holidays {
				label := h.Date
				if h.Name != "" {
					label = h.Date + " — " + h.Name
				}
				fmt.Fprintf(w, `<span class="tag">%s</span>`, htmlEscape(label))
			}
			fmt.Fprint(w, `</div>`)
		}

		// Holiday entertainment windows.
		if len(sched.HolidayWindows) > 0 {
			fmt.Fprintf(w, `<h2 style="font-size:0.95rem;color:#f38ba8">%s</h2>`, ui.holidayWindows)
			fmt.Fprintf(w, `<table><tr><th>%s</th><th>%s</th><th>%s</th></tr>`,
				ui.colDays, ui.colTimeRange, ui.colLimit)
			for _, hw := range sched.HolidayWindows {
				days := translateDays(hw.Days, lang)
				timeRange := fmt.Sprintf("%s — %s", hw.Start, hw.End)
				limit := fmt.Sprintf("%d %s", hw.LimitMinutes, ui.min)
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td></tr>`,
					htmlEscape(days), timeRange, limit)
			}
			fmt.Fprint(w, `</table>`)
		}

		// Holiday sleep times.
		if len(sched.HolidaySleepTimes) > 0 {
			fmt.Fprintf(w, `<h2 style="font-size:0.95rem;color:#f38ba8">%s</h2>`, ui.holidaySleepTimes)
			fmt.Fprintf(w, `<table><tr><th>%s</th><th>%s</th></tr>`,
				ui.colDays, ui.colTimeRange)
			for _, hs := range sched.HolidaySleepTimes {
				days := translateDays(hs.Days, lang)
				timeRange := fmt.Sprintf("%s — %s", hs.Start, hs.End)
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`,
					htmlEscape(days), timeRange)
			}
			fmt.Fprint(w, `</table>`)
		}

		// Vacations.
		if len(sched.Vacations) > 0 {
			fmt.Fprintf(w, `<h2 style="font-size:0.95rem;color:#74c7ec">🏖 %s (%d)</h2>`, ui.vacations, len(sched.Vacations))
			for _, v := range sched.Vacations {
				name := v.Name
				if name == "" {
					name = v.StartDate + " — " + v.EndDate
				}
				fmt.Fprintf(w, `<div style="background:#313244;border-radius:6px;padding:10px 14px;margin-bottom:8px;border-left:3px solid #74c7ec">`)
				fmt.Fprintf(w, `<div style="font-weight:600;color:#74c7ec;margin-bottom:6px">%s: %s — %s</div>`, htmlEscape(name), v.StartDate, v.EndDate)

				// Windows.
				if len(v.Windows) > 0 {
					fmt.Fprintf(w, `<table style="margin-bottom:4px"><tr><th>%s</th><th>%s</th><th>%s</th></tr>`,
						ui.colDays, ui.colTimeRange, ui.colLimit)
					for _, vw := range v.Windows {
						days := translateDays(vw.Days, lang)
						fmt.Fprintf(w, `<tr><td>%s</td><td>%s — %s</td><td>%d %s</td></tr>`,
							htmlEscape(days), vw.Start, vw.End, vw.LimitMinutes, ui.min)
					}
					fmt.Fprint(w, `</table>`)
				}

				// Sleep.
				if len(v.SleepTimes) > 0 {
					for _, vs := range v.SleepTimes {
						days := translateDays(vs.Days, lang)
						fmt.Fprintf(w, `<div style="color:#a6adc8;font-size:0.82rem">%s: %s %s — %s</div>`,
							ui.sleepTimes, htmlEscape(days), vs.Start, vs.End)
					}
				}

				// Pre-day.
				preStart := "16:00"
				preEnd := "22:30"
				if v.PreDayStart != "" {
					preStart = v.PreDayStart
				}
				if v.PreDayEnd != "" {
					preEnd = v.PreDayEnd
				}
				fmt.Fprintf(w, `<div style="color:#a6adc8;font-size:0.82rem">%s: %s — %s</div>`,
					ui.preDay, preStart, preEnd)

				fmt.Fprint(w, `</div>`)
			}
		}
	}

	// --- Apps section ---
	if typeFilter == "all" || typeFilter == "apps" {
		fmt.Fprintf(w, `<h2>%s (%d)</h2>`, ui.appsTitle, len(apps.Apps))
		fmt.Fprintf(w, `<table><tr><th>%s</th><th>%s</th><th>%s</th></tr>`,
			ui.colName, ui.colExe, ui.colPath)
		for _, a := range apps.Apps {
			p := a.Path
			if p == "" {
				p = "—"
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td><code>%s</code></td><td>%s</td></tr>`,
				htmlEscape(a.Name), htmlEscape(a.Executable), htmlEscape(p))
		}
		fmt.Fprint(w, `</table>`)
	}

	// --- Sites section ---
	if typeFilter == "all" || typeFilter == "sites" {
		siteCat := r.URL.Query().Get("sitecat")

		// Фильтруем сайты по категории.
		var filteredSites []siteItem
		for _, s := range sites.Sites {
			switch siteCat {
			case "system":
				if s.Category != "system" {
					continue
				}
			case "regular":
				if s.Category == "system" {
					continue
				}
			}
			filteredSites = append(filteredSites, s)
		}

		fmt.Fprintf(w, `<h2>%s (%d)</h2>`, ui.sitesTitle, len(filteredSites))

		// Фильтр по категории сайтов.
		fmt.Fprintf(w, `<div style="margin-bottom:8px;display:flex;gap:6px;flex-wrap:wrap">`)
		for _, fc := range []struct{ val, label string }{
			{"", ui.filterSitesAll}, {"system", ui.filterSitesSystem}, {"regular", ui.filterSitesRegular},
		} {
			if fc.val == siteCat || (fc.val == "" && siteCat == "") {
				fmt.Fprintf(w, `<span style="color:#f9e2af;font-size:0.8rem;padding:2px 10px;border:1px solid #45475a;border-radius:4px">%s</span>`, fc.label)
			} else {
				fmt.Fprintf(w, `<a href="?type=%s&lang=%s&sitecat=%s" style="color:#89b4fa;text-decoration:none;font-size:0.8rem;padding:2px 10px">%s</a>`,
					typeFilter, lang, fc.val, fc.label)
			}
		}
		fmt.Fprint(w, `</div>`)

		fmt.Fprintf(w, `<table><tr><th>%s</th><th>%s</th><th>%s</th><th>%s</th></tr>`,
			ui.colDomain, ui.colCategory, ui.colSubdomains, ui.colPaths)
		for _, s := range filteredSites {
			subClass := "yes"
			subText := "✓"
			if !s.IncludeSubdomains {
				subClass = "no"
				subText = "✗"
			}
			paths := "—"
			if len(s.AllowedPaths) > 0 {
				paths = ""
				for _, p := range s.AllowedPaths {
					paths += fmt.Sprintf(`<span class="tag">%s</span>`, htmlEscape(p))
				}
			}
			catBadge := ""
			if s.Category == "system" {
				catBadge = `<span style="background:#89b4fa;color:#1e1e2e;border-radius:3px;padding:1px 6px;font-size:0.75rem;font-weight:600">` + ui.catSystem + `</span>`
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td class="%s">%s</td><td>%s</td></tr>`,
				htmlEscape(s.Domain), catBadge, subClass, subText, paths)
		}
		fmt.Fprint(w, `</table>`)
	}

	fmt.Fprint(w, `</body></html>`)
}

// configUI holds translated labels for the config HTML page.
type configUI struct {
	title, filterLabel                                     string
	filterAll, filterApps, filterSites, filterSchedule     string
	filterSitesAll, filterSitesSystem, filterSitesRegular  string
	applyBtn                                               string
	appsTitle, sitesTitle, schedTitle                       string
	colName, colExe, colPath                               string
	colDomain, colSubdomains, colPaths                     string
	colCategory                                            string
	catSystem                                              string
	entWindows, sleepTimes                                 string
	holidays, holidayWindows, holidaySleepTimes             string
	vacations, preDay                                       string
	colDays, colTimeRange, colLimit                        string
	min                                                    string
	warnBefore, sleepWarnBefore, fullLogging                string
	valYes, valNo                                          string
	entApps                                                string
	logsTab, statsTab, configTab, adminTab, reloadBtn      string
}

func configUILabels(lang string) configUI {
	switch lang {
	case "ru":
		return configUI{
			title: "Родительский контроль — Конфигурация", filterLabel: "Раздел",
			filterAll: "Все", filterApps: "Программы", filterSites: "Сайты", filterSchedule: "Расписание",
			filterSitesAll: "Все сайты", filterSitesSystem: "Системные", filterSitesRegular: "Обычные",
			applyBtn: "Применить",
			appsTitle: "Разрешённые программы", sitesTitle: "Разрешённые сайты", schedTitle: "Расписание",
			colName: "Название", colExe: "Исполняемый файл", colPath: "Путь",
			colDomain: "Домен", colSubdomains: "Поддомены", colPaths: "Разрешённые пути",
			colCategory: "Категория", catSystem: "системный",
			entWindows: "Окна развлечений", sleepTimes: "Время сна",
			holidays: "Праздники", holidayWindows: "Окна развлечений (праздники)", holidaySleepTimes: "Время сна (праздники)",
			vacations: "Каникулы", preDay: "Предканикулярный день",
			colDays: "Дни", colTimeRange: "Время", colLimit: "Лимит",
			min: "мин",
			warnBefore: "Предупреждение до конца развлечений", sleepWarnBefore: "Предупреждение до сна",
			fullLogging: "Полное логирование", valYes: "да", valNo: "нет",
			entApps:   "Развлекательные приложения",
			logsTab: "Логи", statsTab: "Статистика", configTab: "Конфигурация", adminTab: "Управление", reloadBtn: "🔄 Обновить конфиг",
		}
	case "it":
		return configUI{
			title: "Controllo Genitori — Configurazione", filterLabel: "Sezione",
			filterAll: "Tutto", filterApps: "App", filterSites: "Siti", filterSchedule: "Orario",
			filterSitesAll: "Tutti i siti", filterSitesSystem: "Sistema", filterSitesRegular: "Normali",
			applyBtn: "Applica",
			appsTitle: "App consentite", sitesTitle: "Siti consentiti", schedTitle: "Orario",
			colName: "Nome", colExe: "Eseguibile", colPath: "Percorso",
			colDomain: "Dominio", colSubdomains: "Sottodomini", colPaths: "Percorsi consentiti",
			colCategory: "Categoria", catSystem: "sistema",
			entWindows: "Finestre di intrattenimento", sleepTimes: "Orari di sonno",
			holidays: "Festività", holidayWindows: "Finestre intrattenimento (festivi)", holidaySleepTimes: "Orari sonno (festivi)",
			vacations: "Vacanze", preDay: "Giorno pre-vacanza",
			colDays: "Giorni", colTimeRange: "Orario", colLimit: "Limite",
			min: "min",
			warnBefore: "Avviso prima della fine intrattenimento", sleepWarnBefore: "Avviso prima del sonno",
			fullLogging: "Registrazione completa", valYes: "sì", valNo: "no",
			entApps:   "App di intrattenimento",
			logsTab: "Log", statsTab: "Statistiche", configTab: "Configurazione", adminTab: "Gestione", reloadBtn: "🔄 Ricarica config",
		}
	case "es":
		return configUI{
			title: "Control Parental — Configuración", filterLabel: "Sección",
			filterAll: "Todo", filterApps: "Apps", filterSites: "Sitios", filterSchedule: "Horario",
			filterSitesAll: "Todos los sitios", filterSitesSystem: "Sistema", filterSitesRegular: "Normales",
			applyBtn: "Aplicar",
			appsTitle: "Apps permitidas", sitesTitle: "Sitios permitidos", schedTitle: "Horario",
			colName: "Nombre", colExe: "Ejecutable", colPath: "Ruta",
			colDomain: "Dominio", colSubdomains: "Subdominios", colPaths: "Rutas permitidas",
			colCategory: "Categoría", catSystem: "sistema",
			entWindows: "Ventanas de entretenimiento", sleepTimes: "Horarios de sueño",
			holidays: "Festivos", holidayWindows: "Ventanas entretenimiento (festivos)", holidaySleepTimes: "Horarios sueño (festivos)",
			vacations: "Vacaciones", preDay: "Día pre-vacaciones",
			colDays: "Días", colTimeRange: "Horario", colLimit: "Límite",
			min: "min",
			warnBefore: "Aviso antes del fin del entretenimiento", sleepWarnBefore: "Aviso antes de dormir",
			fullLogging: "Registro completo", valYes: "sí", valNo: "no",
			entApps:   "Apps de entretenimiento",
			logsTab: "Registros", statsTab: "Estadísticas", configTab: "Configuración", adminTab: "Administración", reloadBtn: "🔄 Recargar config",
		}
	case "de":
		return configUI{
			title: "Kindersicherung — Konfiguration", filterLabel: "Bereich",
			filterAll: "Alle", filterApps: "Apps", filterSites: "Seiten", filterSchedule: "Zeitplan",
			filterSitesAll: "Alle Seiten", filterSitesSystem: "System", filterSitesRegular: "Normal",
			applyBtn: "Anwenden",
			appsTitle: "Erlaubte Apps", sitesTitle: "Erlaubte Seiten", schedTitle: "Zeitplan",
			colName: "Name", colExe: "Programm", colPath: "Pfad",
			colDomain: "Domain", colSubdomains: "Subdomains", colPaths: "Erlaubte Pfade",
			colCategory: "Kategorie", catSystem: "System",
			entWindows: "Unterhaltungsfenster", sleepTimes: "Schlafenszeiten",
			holidays: "Feiertage", holidayWindows: "Unterhaltungsfenster (Feiertage)", holidaySleepTimes: "Schlafenszeiten (Feiertage)",
			vacations: "Ferien", preDay: "Vorfeiertag",
			colDays: "Tage", colTimeRange: "Zeit", colLimit: "Limit",
			min: "Min.",
			warnBefore: "Warnung vor Ende der Unterhaltung", sleepWarnBefore: "Warnung vor Schlafenszeit",
			fullLogging: "Vollständige Protokollierung", valYes: "ja", valNo: "nein",
			entApps:   "Unterhaltungs-Apps",
			logsTab: "Protokolle", statsTab: "Statistiken", configTab: "Konfiguration", adminTab: "Verwaltung", reloadBtn: "🔄 Konfig neu laden",
		}
	case "pl":
		return configUI{
			title: "Kontrola Rodzicielska — Konfiguracja", filterLabel: "Sekcja",
			filterAll: "Wszystko", filterApps: "Aplikacje", filterSites: "Strony", filterSchedule: "Harmonogram",
			filterSitesAll: "Wszystkie strony", filterSitesSystem: "Systemowe", filterSitesRegular: "Zwykłe",
			applyBtn: "Zastosuj",
			appsTitle: "Dozwolone aplikacje", sitesTitle: "Dozwolone strony", schedTitle: "Harmonogram",
			colName: "Nazwa", colExe: "Plik wykonywalny", colPath: "Ścieżka",
			colDomain: "Domena", colSubdomains: "Subdomeny", colPaths: "Dozwolone ścieżki",
			colCategory: "Kategoria", catSystem: "systemowa",
			entWindows: "Okna rozrywki", sleepTimes: "Pora snu",
			holidays: "Święta", holidayWindows: "Okna rozrywki (święta)", holidaySleepTimes: "Pora snu (święta)",
			vacations: "Wakacje", preDay: "Dzień przed wakacjami",
			colDays: "Dni", colTimeRange: "Czas", colLimit: "Limit",
			min: "min",
			warnBefore: "Ostrzeżenie przed końcem rozrywki", sleepWarnBefore: "Ostrzeżenie przed snem",
			fullLogging: "Pełne logowanie", valYes: "tak", valNo: "nie",
			entApps:   "Aplikacje rozrywkowe",
			logsTab: "Logi", statsTab: "Statystyki", configTab: "Konfiguracja", adminTab: "Zarządzanie", reloadBtn: "🔄 Przeładuj konfigurację",
		}
	case "zh-TW":
		return configUI{
			title: "家長控制 — 設定", filterLabel: "區段",
			filterAll: "全部", filterApps: "應用程式", filterSites: "網站", filterSchedule: "時間表",
			filterSitesAll: "所有網站", filterSitesSystem: "系統", filterSitesRegular: "一般",
			applyBtn: "套用",
			appsTitle: "允許的應用程式", sitesTitle: "允許的網站", schedTitle: "時間表",
			colName: "名稱", colExe: "執行檔", colPath: "路徑",
			colDomain: "網域", colSubdomains: "子網域", colPaths: "允許的路徑",
			colCategory: "類別", catSystem: "系統",
			entWindows: "娛樂時段", sleepTimes: "睡眠時間",
			holidays: "假日", holidayWindows: "娛樂時段（假日）", holidaySleepTimes: "睡眠時間（假日）",
			vacations: "假期", preDay: "假期前一天",
			colDays: "天", colTimeRange: "時間", colLimit: "限制",
			min: "分鐘",
			warnBefore: "娛樂結束前警告", sleepWarnBefore: "睡眠前警告",
			fullLogging: "完整記錄", valYes: "是", valNo: "否",
			entApps:   "娛樂應用程式",
			logsTab: "日誌", statsTab: "統計", configTab: "設定", adminTab: "管理", reloadBtn: "🔄 重新載入設定",
		}
	default:
		return configUI{
			title: "Parental Control — Configuration", filterLabel: "Section",
			filterAll: "All", filterApps: "Apps", filterSites: "Sites", filterSchedule: "Schedule",
			filterSitesAll: "All Sites", filterSitesSystem: "System", filterSitesRegular: "Regular",
			applyBtn: "Apply",
			appsTitle: "Allowed Apps", sitesTitle: "Allowed Sites", schedTitle: "Schedule",
			colName: "Name", colExe: "Executable", colPath: "Path",
			colDomain: "Domain", colSubdomains: "Subdomains", colPaths: "Allowed Paths",
			colCategory: "Category", catSystem: "system",
			entWindows: "Entertainment Windows", sleepTimes: "Sleep Times",
			holidays: "Holidays", holidayWindows: "Entertainment Windows (Holidays)", holidaySleepTimes: "Sleep Times (Holidays)",
			vacations: "Vacations", preDay: "Pre-vacation day",
			colDays: "Days", colTimeRange: "Time", colLimit: "Limit",
			min: "min",
			warnBefore: "Warning before entertainment ends", sleepWarnBefore: "Warning before sleep",
			fullLogging: "Full logging", valYes: "yes", valNo: "no",
			entApps:   "Entertainment Apps",
			logsTab: "Logs", statsTab: "Statistics", configTab: "Configuration", adminTab: "Admin", reloadBtn: "🔄 Reload Config",
		}
	}
}

// translateDays converts English day names to localized comma-separated string.
func translateDays(days []string, lang string) string {
	dayMaps := map[string]map[string]string{
		"ru": {"monday": "Пн", "tuesday": "Вт", "wednesday": "Ср", "thursday": "Чт", "friday": "Пт", "saturday": "Сб", "sunday": "Вс"},
		"it": {"monday": "Lun", "tuesday": "Mar", "wednesday": "Mer", "thursday": "Gio", "friday": "Ven", "saturday": "Sab", "sunday": "Dom"},
		"es": {"monday": "Lun", "tuesday": "Mar", "wednesday": "Mié", "thursday": "Jue", "friday": "Vie", "saturday": "Sáb", "sunday": "Dom"},
		"de": {"monday": "Mo", "tuesday": "Di", "wednesday": "Mi", "thursday": "Do", "friday": "Fr", "saturday": "Sa", "sunday": "So"},
		"pl": {"monday": "Pon", "tuesday": "Wt", "wednesday": "Śr", "thursday": "Czw", "friday": "Pt", "saturday": "Sob", "sunday": "Ndz"},
		"zh-TW": {"monday": "週一", "tuesday": "週二", "wednesday": "週三", "thursday": "週四", "friday": "週五", "saturday": "週六", "sunday": "週日"},
	}

	dayMap, ok := dayMaps[lang]
	if !ok {
		// English — return as-is.
		result := ""
		for i, d := range days {
			if i > 0 {
				result += ", "
			}
			result += d
		}
		return result
	}

	result := ""
	for i, d := range days {
		if i > 0 {
			result += ", "
		}
		if v, ok := dayMap[d]; ok {
			result += v
		} else {
			result += d
		}
	}
	return result
}

// handleStats handles GET /stats?date=2026-03-22 or GET /stats?period=week.
func (h *HTTPLogServer) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.statsProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"days":[]}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if period := r.URL.Query().Get("period"); period == "week" {
		json.NewEncoder(w).Encode(h.statsProvider.GetWeekStats())
		return
	}

	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	json.NewEncoder(w).Encode(h.statsProvider.GetDayStats(date))
}

// handleBrowserActivity handles POST /browser-activity.
// Принимает данные об открытых URL от tray-приложения.
func (h *HTTPLogServer) handleBrowserActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.browserReceiver == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(BrowserCloseResponse{})
		return
	}

	var report BrowserURLReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	closeHWNDs, reason := h.browserReceiver.ReceiveBrowserURLs(report.URLs)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(BrowserCloseResponse{CloseHWNDs: closeHWNDs, Reason: reason})
}

// handleLearningCurrent handles GET /learning/current — текущий отчёт обучения (JSON).
func (h *HTTPLogServer) handleLearningCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.learningProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error":"not configured"}`))
		return
	}
	report := h.learningProvider.GetCurrentReport()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// handleLearningReports handles GET /learning/reports — список сохранённых отчётов.
func (h *HTTPLogServer) handleLearningReports(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.learningProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
		return
	}
	names := h.learningProvider.ListReports()
	if names == nil {
		names = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(names)
}

// handleLearningReport handles GET /learning/report/{name} — скачать конкретный отчёт.
func (h *HTTPLogServer) handleLearningReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.learningProvider == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/learning/report/")
	if name == "" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	data, err := h.learningProvider.ReadReport(name)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Write(data)
}

// handleLearningClear handles POST /learning/clear — удалить все отчёты обучения.
func (h *HTTPLogServer) handleLearningClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.learningProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PauseResponse{OK: false, Message: "not configured"})
		return
	}
	err := h.learningProvider.ClearReports()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PauseResponse{OK: false, Message: err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PauseResponse{OK: true, Message: "Reports cleared"})
}

func (h *HTTPLogServer) handleSelfPause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	// Только с localhost.
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx >= 0 {
		ip = ip[:idx]
	}
	ip = strings.Trim(ip, "[]")
	if ip != "127.0.0.1" && ip != "::1" && ip != "localhost" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if h.pauseProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PauseResponse{OK: false, Message: "not configured"})
		return
	}
	var req struct {
		Action string `json:"action"` // "pause" or "unpause"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	var ok bool
	var msg string
	if req.Action == "unpause" {
		ok, msg = h.pauseProvider.SelfUnpauseEntertainment()
	} else {
		ok, msg = h.pauseProvider.SelfPauseEntertainment()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PauseResponse{OK: ok, Message: msg})
}

func (h *HTTPLogServer) handleNotifications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.pauseProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
		return
	}
	notifs := h.pauseProvider.DrainNotifications()
	if notifs == nil {
		notifs = []Notification{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(notifs)
}

func (h *HTTPLogServer) handleAddUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.adjustmentProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PauseResponse{OK: false, Message: "not configured"})
		return
	}
	var req AddUsageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	ok, msg := h.adjustmentProvider.AddUsage(req.Password, req.Minutes, req.Reason)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PauseResponse{OK: ok, Message: msg})
}

func (h *HTTPLogServer) handleAdjustBonus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.adjustmentProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PauseResponse{OK: false, Message: "not configured"})
		return
	}
	var req AdjustBonusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	ok, msg := h.adjustmentProvider.AdjustBonus(req.Password, req.Minutes, req.Reason)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PauseResponse{OK: ok, Message: msg})
}

func (h *HTTPLogServer) handleAdjustSleep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.adjustmentProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PauseResponse{OK: false, Message: "not configured"})
		return
	}
	var req AdjustSleepRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	ok, msg := h.adjustmentProvider.AdjustSleep(req.Password, req.NewStart, req.NewEnd, req.Reason)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PauseResponse{OK: ok, Message: msg})
}

// handlePause handles POST /pause — ставит паузу на N минут по паролю.
func (h *HTTPLogServer) handlePause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.pauseProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PauseResponse{OK: false, Message: "not configured"})
		return
	}

	var req PauseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	ok, msg := h.pauseProvider.Pause(req.Password, req.Minutes)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PauseResponse{OK: ok, Message: msg})
}

// handleUnpause handles POST /unpause — снимает паузу по паролю.
func (h *HTTPLogServer) handleUnpause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.pauseProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PauseResponse{OK: false, Message: "not configured"})
		return
	}

	var req UnpauseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	ok, msg := h.pauseProvider.Unpause(req.Password)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PauseResponse{OK: ok, Message: msg})
}

func (h *HTTPLogServer) handleSetMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.pauseProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PauseResponse{OK: false, Message: "not configured"})
		return
	}

	var req SetModeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	ok, msg := h.pauseProvider.SetServiceMode(req.Password, req.Mode, req.Minutes)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PauseResponse{OK: ok, Message: msg})
}

// handleReloadConfig handles POST /reload-config — принудительное обновление конфига.
func (h *HTTPLogServer) handleReloadConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.configReloader == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "message": "not configured"})
		return
	}

	msg, err := h.configReloader.ReloadConfig(r.Context())
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "message": err.Error()})
	} else {
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "message": msg})
	}
}

// handleChangePassword handles POST /change-password — смена пароля.
func (h *HTTPLogServer) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.passwordChanger == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PauseResponse{OK: false, Message: "not configured"})
		return
	}

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	ok, msg := h.passwordChanger.ChangePassword(req.OldPassword, req.NewPassword)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PauseResponse{OK: ok, Message: msg})
}

// handleChangeConfigURL handles POST /change-config-url.
func (h *HTTPLogServer) handleChangeConfigURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.configURLChanger == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PauseResponse{OK: false, Message: "not configured"})
		return
	}

	var req struct {
		Password string `json:"password"`
		URL      string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	ok, msg := h.configURLChanger.ChangeConfigURL(req.Password, req.URL)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PauseResponse{OK: ok, Message: msg})
}

// handleAdminHTML handles GET /admin-html — admin page with pause, password, URL controls.
func (h *HTTPLogServer) handleAdminHTML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	lang := r.URL.Query().Get("lang")
	lang = normalizeLang(lang)

	ui := adminUILabels(lang)

	// Get current config URL.
	configURL := ""
	if h.configURLChanger != nil {
		configURL = h.configURLChanger.GetConfigURL()
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>`)
	fmt.Fprint(w, htmlEscape(ui.title))
	fmt.Fprint(w, `</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:'Segoe UI',sans-serif;background:#1e1e2e;color:#cdd6f4;padding:16px;min-height:100vh}
.page-switch{margin-bottom:12px}
.page-switch a{color:#1e1e2e;background:#89b4fa;text-decoration:none;font-size:0.85rem;padding:4px 14px;border-radius:4px;font-weight:600}
.page-switch a:hover{background:#74c7ec}
.page-switch span{color:#f9e2af;font-size:0.85rem;padding:4px 14px;border:1px solid #45475a;border-radius:4px;font-weight:600}
.section{background:#313244;border-radius:8px;padding:16px 20px;margin-bottom:16px}
.section h2{font-size:1rem;color:#89b4fa;margin-bottom:12px}
.field{margin-bottom:10px}
.field label{display:block;color:#a6adc8;font-size:0.85rem;margin-bottom:4px}
.field input{background:#1e1e2e;color:#cdd6f4;border:1px solid #45475a;border-radius:4px;padding:6px 10px;font-size:0.85rem;width:100%;max-width:400px}
.field input[type=number]{max-width:120px}
.btn{background:#89b4fa;color:#1e1e2e;border:none;border-radius:4px;padding:6px 18px;cursor:pointer;font-size:0.85rem;font-weight:600;margin-top:4px}
.btn:hover{background:#74c7ec}
.btn-danger{background:#f38ba8}
.btn-danger:hover{background:#eba0ac}
.msg{margin-top:8px;font-size:0.85rem;padding:6px 10px;border-radius:4px;display:none}
.msg-ok{background:#a6e3a1;color:#1e1e2e;display:block}
.msg-err{background:#f38ba8;color:#1e1e2e;display:block}
.url-input{max-width:600px!important}
</style></head><body>`)

	// Top bar: day type + language switcher.
	fmt.Fprint(w, topBarHTML(h.statusProvider, lang, "/admin-html", ""))

	// Page switcher.
	today := time.Now().Format("2006-01-02")
	fmt.Fprintf(w, `<div class="page-switch"><a href="/logs-html?lang=%s">%s</a> <a href="/stats-html?date=%s&lang=%s">%s</a> <a href="/config-html?lang=%s">%s</a> <span>%s</span></div>`,
		lang, ui.logsTab, today, lang, ui.statsTab, lang, ui.configTab, ui.adminTab)

	// Pause banner.
	writePauseBanner(w, h.statusProvider, lang)

	// --- Service Mode section ---
	currentMode := "normal"
	if h.pauseProvider != nil {
		currentMode = h.pauseProvider.GetServiceMode()
	}
	modeLabels := serviceModeLabels(lang)
	fmt.Fprintf(w, `<div class="section"><h2>🎛 %s</h2>`, modeLabels.title)
	fmt.Fprintf(w, `<div style="margin-bottom:10px;font-size:0.85rem;color:#a6adc8">%s: <span style="color:#f9e2af;font-weight:600">%s</span></div>`,
		modeLabels.currentLabel, htmlEscape(modeLabels.modes[currentMode]))
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="password" id="modePwd"></div>`, ui.password)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><select id="modeSelect" style="background:#1e1e2e;color:#cdd6f4;border:1px solid #45475a;border-radius:4px;padding:6px 10px;font-size:0.85rem">`)
	for _, m := range []string{"normal", "filter_paused", "entertainment_paused", "learning", "unrestricted"} {
		sel := ""
		if m == currentMode {
			sel = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, m, sel, htmlEscape(modeLabels.modes[m]))
	}
	fmt.Fprint(w, `</select></div>`)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="number" id="modeMins" value="60" min="0" max="480"></div>`, modeLabels.minutesLabel)
	fmt.Fprintf(w, `<div style="color:#6c7086;font-size:0.8rem;margin-bottom:8px">%s</div>`, modeLabels.minutesHint)
	fmt.Fprintf(w, `<button class="btn" onclick="doSetMode()">%s</button>`, modeLabels.applyBtn)
	fmt.Fprint(w, `<div class="msg" id="modeMsg"></div></div>`)

	// --- Change password section ---
	fmt.Fprintf(w, `<div class="section"><h2>%s</h2>`, ui.pwdTitle)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="password" id="oldPwd"></div>`, ui.oldPwd)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="password" id="newPwd"></div>`, ui.newPwd)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="password" id="confirmPwd"></div>`, ui.confirmPwd)
	fmt.Fprintf(w, `<button class="btn" onclick="doChangePwd()">%s</button>`, ui.changePwdBtn)
	fmt.Fprint(w, `<div class="msg" id="pwdMsg"></div></div>`)

	// --- Config URL section ---
	fmt.Fprintf(w, `<div class="section"><h2>%s</h2>`, ui.urlTitle)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="password" id="urlPwd"></div>`, ui.password)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="text" id="configUrl" class="url-input" value="%s"></div>`, ui.urlLabel, htmlEscape(configURL))
	fmt.Fprintf(w, `<button class="btn" onclick="doChangeURL()">%s</button>`, ui.changeURLBtn)
	fmt.Fprint(w, `<div class="msg" id="urlMsg"></div></div>`)

	// --- Adjustments section ---
	adjLabels := adjustmentUILabels(lang)

	// Add usage.
	fmt.Fprintf(w, `<div class="section"><h2>🎮 %s</h2>`, adjLabels.addUsageTitle)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="password" id="usagePwd"></div>`, ui.password)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="number" id="usageMins" value="30" min="1" max="480"></div>`, adjLabels.minutes)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="text" id="usageReason" placeholder="%s"></div>`, adjLabels.reason, adjLabels.reasonHint)
	fmt.Fprintf(w, `<button class="btn" onclick="doAddUsage()">%s</button>`, adjLabels.applyBtn)
	fmt.Fprint(w, `<div class="msg" id="usageMsg"></div></div>`)

	// Adjust bonus.
	fmt.Fprintf(w, `<div class="section"><h2>⏱ %s</h2>`, adjLabels.bonusTitle)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="password" id="bonusPwd"></div>`, ui.password)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="number" id="bonusMins" value="30" min="-480" max="480"></div>`, adjLabels.bonusMinutes)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="text" id="bonusReason"></div>`, adjLabels.reason)
	fmt.Fprintf(w, `<button class="btn" onclick="doAdjustBonus()">%s</button>`, adjLabels.applyBtn)
	fmt.Fprint(w, `<div class="msg" id="bonusMsg"></div></div>`)

	// Adjust sleep.
	fmt.Fprintf(w, `<div class="section"><h2>🌙 %s</h2>`, adjLabels.sleepTitle)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="password" id="sleepPwd"></div>`, ui.password)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="time" id="sleepStart"></div>`, adjLabels.sleepStart)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="time" id="sleepEnd"></div>`, adjLabels.sleepEnd)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="text" id="sleepReason"></div>`, adjLabels.reason)
	fmt.Fprintf(w, `<button class="btn" onclick="doAdjustSleep()">%s</button>`, adjLabels.applyBtn)
	fmt.Fprint(w, `<div class="msg" id="sleepMsg"></div></div>`)

	// --- Learning Logs section ---
	learningLabels := learningUILabels(lang)
	fmt.Fprintf(w, `<div class="section"><h2>📚 %s</h2>`, learningLabels.title)
	fmt.Fprintf(w, `<div id="learningList" style="margin-bottom:10px;color:#a6adc8;font-size:0.85rem">%s</div>`, learningLabels.loading)
	fmt.Fprintf(w, `<button class="btn" onclick="loadLearningReports()">%s</button> `, learningLabels.refreshBtn)
	fmt.Fprintf(w, `<button class="btn btn-danger" onclick="clearLearningReports()">%s</button>`, learningLabels.clearBtn)
	fmt.Fprint(w, `<div class="msg" id="learningMsg"></div></div>`)

	// JavaScript.
	fmt.Fprintf(w, `<script>
function showMsg(id,ok,text){var e=document.getElementById(id);e.textContent=text;e.className='msg '+(ok?'msg-ok':'msg-err')}
async function postAPI(url,body){
  try{var r=await fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});return await r.json()}
  catch(e){return{ok:false,message:'%s'}}
}
async function doSetMode(){
  var pwd=document.getElementById('modePwd').value;
  var mode=document.getElementById('modeSelect').value;
  var mins=parseInt(document.getElementById('modeMins').value)||0;
  var r=await postAPI('/set-mode',{password:pwd,mode:mode,minutes:mins});
  showMsg('modeMsg',r.ok,r.message);if(r.ok)setTimeout(()=>location.reload(),1000);
}
async function doChangePwd(){
  var o=document.getElementById('oldPwd').value;
  var n=document.getElementById('newPwd').value;
  var c=document.getElementById('confirmPwd').value;
  if(n!==c){showMsg('pwdMsg',false,'%s');return}
  var r=await postAPI('/change-password',{old_password:o,new_password:n});
  showMsg('pwdMsg',r.ok,r.message);
}
async function doChangeURL(){
  var pwd=document.getElementById('urlPwd').value;
  var url=document.getElementById('configUrl').value;
  var r=await postAPI('/change-config-url',{password:pwd,url:url});
  showMsg('urlMsg',r.ok,r.message);
}
async function doAddUsage(){
  var pwd=document.getElementById('usagePwd').value;
  var mins=parseInt(document.getElementById('usageMins').value)||0;
  var reason=document.getElementById('usageReason').value;
  var r=await postAPI('/add-usage',{password:pwd,minutes:mins,reason:reason});
  showMsg('usageMsg',r.ok,r.message);
}
async function doAdjustBonus(){
  var pwd=document.getElementById('bonusPwd').value;
  var mins=parseInt(document.getElementById('bonusMins').value)||0;
  var reason=document.getElementById('bonusReason').value;
  var r=await postAPI('/adjust-bonus',{password:pwd,minutes:mins,reason:reason});
  showMsg('bonusMsg',r.ok,r.message);
}
async function doAdjustSleep(){
  var pwd=document.getElementById('sleepPwd').value;
  var start=document.getElementById('sleepStart').value;
  var end=document.getElementById('sleepEnd').value;
  var reason=document.getElementById('sleepReason').value;
  var r=await postAPI('/adjust-sleep',{password:pwd,new_start:start,new_end:end,reason:reason});
  showMsg('sleepMsg',r.ok,r.message);
}
async function loadLearningReports(){
  var el=document.getElementById('learningList');
  var html='';
  try{
    // Текущая активная сессия.
    var cr=await fetch('/learning/current');
    var cur=await cr.json();
    if(cur&&cur.snapshots&&cur.snapshots.length>0){
      var n=cur.snapshots.length;
      var t=cur.start_time?cur.start_time.substring(0,19).replace('T',' '):'';
      html+='<div style="margin:6px 0;padding:8px 12px;background:#45475a;border-radius:4px;border-left:3px solid #89b4fa">';
      html+='<span style="color:#89b4fa;font-weight:600">▶ %s</span>';
      html+='<span style="color:#6c7086;margin-left:8px">'+t+' — '+n+' snapshots</span>';
      html+=' <a href="/learning/current" style="color:#a6e3a1;margin-left:8px" download="learning_current.json">⬇ JSON</a>';
      html+='</div>';
    }
    // Сохранённые отчёты.
    var r=await fetch('/learning/reports');
    var list=await r.json();
    if(list&&list.length>0){
      for(var i=list.length-1;i>=0;i--){
        html+='<div style="margin:4px 0"><a href="/learning/report/'+list[i]+'" style="color:#89b4fa" download>📄 '+list[i]+'</a></div>';
      }
    }
    if(!html){html='<em>%s</em>'}
    el.innerHTML=html;
  }catch(e){el.innerHTML='Error: '+e}
}
async function clearLearningReports(){
  var r=await postAPI('/learning/clear',{});
  showMsg('learningMsg',r.ok,r.message);
  if(r.ok)loadLearningReports();
}
loadLearningReports();
</script>`, ui.unavailable, ui.mismatch, learningLabels.noReports)

	fmt.Fprint(w, `</body></html>`)
}

type adminUI struct {
	title                                                string
	logsTab, statsTab, configTab, adminTab               string
	pauseTitle, password, minutes, pauseBtn, unpauseBtn  string
	pwdTitle, oldPwd, newPwd, confirmPwd, changePwdBtn   string
	urlTitle, urlLabel, changeURLBtn                      string
	unavailable, mismatch                                string
}

func adminUILabels(lang string) adminUI {
	switch lang {
	case "ru":
		return adminUI{
			title: "Родительский контроль — Управление",
			logsTab: "Логи", statsTab: "Статистика", configTab: "Конфигурация", adminTab: "Управление",
			pauseTitle: "⏸ Пауза", password: "Пароль", minutes: "Минуты", pauseBtn: "Поставить паузу", unpauseBtn: "Снять паузу",
			pwdTitle: "🔑 Смена пароля", oldPwd: "Текущий пароль", newPwd: "Новый пароль", confirmPwd: "Подтвердите пароль", changePwdBtn: "Сменить пароль",
			urlTitle: "🔗 URL конфигурации", urlLabel: "URL", changeURLBtn: "Сохранить URL",
			unavailable: "Сервис недоступен", mismatch: "Пароли не совпадают",
		}
	case "it":
		return adminUI{
			title: "Controllo Genitori — Gestione",
			logsTab: "Log", statsTab: "Statistiche", configTab: "Configurazione", adminTab: "Gestione",
			pauseTitle: "⏸ Pausa", password: "Password", minutes: "Minuti", pauseBtn: "Metti in pausa", unpauseBtn: "Riprendi",
			pwdTitle: "🔑 Cambia password", oldPwd: "Password attuale", newPwd: "Nuova password", confirmPwd: "Conferma password", changePwdBtn: "Cambia password",
			urlTitle: "🔗 URL configurazione", urlLabel: "URL", changeURLBtn: "Salva URL",
			unavailable: "Servizio non disponibile", mismatch: "Le password non corrispondono",
		}
	case "es":
		return adminUI{
			title: "Control Parental — Administración",
			logsTab: "Registros", statsTab: "Estadísticas", configTab: "Configuración", adminTab: "Administración",
			pauseTitle: "⏸ Pausa", password: "Contraseña", minutes: "Minutos", pauseBtn: "Pausar", unpauseBtn: "Reanudar",
			pwdTitle: "🔑 Cambiar contraseña", oldPwd: "Contraseña actual", newPwd: "Nueva contraseña", confirmPwd: "Confirmar contraseña", changePwdBtn: "Cambiar contraseña",
			urlTitle: "🔗 URL de configuración", urlLabel: "URL", changeURLBtn: "Guardar URL",
			unavailable: "Servicio no disponible", mismatch: "Las contraseñas no coinciden",
		}
	case "de":
		return adminUI{
			title: "Kindersicherung — Verwaltung",
			logsTab: "Protokolle", statsTab: "Statistiken", configTab: "Konfiguration", adminTab: "Verwaltung",
			pauseTitle: "⏸ Pause", password: "Passwort", minutes: "Minuten", pauseBtn: "Pausieren", unpauseBtn: "Fortsetzen",
			pwdTitle: "🔑 Passwort ändern", oldPwd: "Aktuelles Passwort", newPwd: "Neues Passwort", confirmPwd: "Passwort bestätigen", changePwdBtn: "Passwort ändern",
			urlTitle: "🔗 Konfigurations-URL", urlLabel: "URL", changeURLBtn: "URL speichern",
			unavailable: "Dienst nicht verfügbar", mismatch: "Passwörter stimmen nicht überein",
		}
	case "pl":
		return adminUI{
			title: "Kontrola Rodzicielska — Zarządzanie",
			logsTab: "Logi", statsTab: "Statystyki", configTab: "Konfiguracja", adminTab: "Zarządzanie",
			pauseTitle: "⏸ Pauza", password: "Hasło", minutes: "Minuty", pauseBtn: "Wstrzymaj", unpauseBtn: "Wznów",
			pwdTitle: "🔑 Zmiana hasła", oldPwd: "Aktualne hasło", newPwd: "Nowe hasło", confirmPwd: "Potwierdź hasło", changePwdBtn: "Zmień hasło",
			urlTitle: "🔗 URL konfiguracji", urlLabel: "URL", changeURLBtn: "Zapisz URL",
			unavailable: "Usługa niedostępna", mismatch: "Hasła nie pasują do siebie",
		}
	case "zh-TW":
		return adminUI{
			title: "家長控制 — 管理",
			logsTab: "日誌", statsTab: "統計", configTab: "設定", adminTab: "管理",
			pauseTitle: "⏸ 暫停", password: "密碼", minutes: "分鐘", pauseBtn: "暫停", unpauseBtn: "取消暫停",
			pwdTitle: "🔑 變更密碼", oldPwd: "目前密碼", newPwd: "新密碼", confirmPwd: "確認密碼", changePwdBtn: "變更密碼",
			urlTitle: "🔗 設定網址", urlLabel: "URL", changeURLBtn: "儲存 URL",
			unavailable: "服務不可用", mismatch: "密碼不一致",
		}
	default:
		return adminUI{
			title: "Parental Control — Admin",
			logsTab: "Logs", statsTab: "Statistics", configTab: "Configuration", adminTab: "Admin",
			pauseTitle: "⏸ Pause", password: "Password", minutes: "Minutes", pauseBtn: "Pause", unpauseBtn: "Unpause",
			pwdTitle: "🔑 Change Password", oldPwd: "Current password", newPwd: "New password", confirmPwd: "Confirm password", changePwdBtn: "Change Password",
			urlTitle: "🔗 Config URL", urlLabel: "URL", changeURLBtn: "Save URL",
			unavailable: "Service unavailable", mismatch: "Passwords do not match",
		}
	}
}

// handleLogsHTML handles GET /logs-html — отдаёт логи как HTML-страницу с фильтрами.
// Параметры: ?date=2026-03-24, ?type=block|warning|info|all, ?lang=en|ru
func (h *HTTPLogServer) handleLogsHTML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	typeFilter := r.URL.Query().Get("type")
	lang := r.URL.Query().Get("lang")
	lang = normalizeLang(lang)

	var entries []logger.LogEntry
	if h.fileLogProvider != nil {
		entries = h.fileLogProvider.ReadEntries(date, 2000)
	}
	if entries == nil {
		entries = []logger.LogEntry{}
	}

	// Фильтр по типу.
	if typeFilter != "" && typeFilter != "all" {
		var filtered []logger.LogEntry
		for _, e := range entries {
			if e.EventType == typeFilter {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	// UI labels based on lang.
	ui := logsUILabels(lang)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html><html><head><meta charset="utf-8">
<title>`)
	fmt.Fprint(w, htmlEscape(ui.title))
	fmt.Fprint(w, `</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:'Segoe UI',sans-serif;background:#1e1e2e;color:#cdd6f4;padding:16px}
h1{font-size:1.3rem;margin-bottom:12px;color:#89b4fa}
.filters{margin-bottom:14px;display:flex;gap:12px;align-items:center;flex-wrap:wrap}
.filters label{color:#a6adc8;font-size:0.85rem}
.filters input,.filters select{background:#313244;color:#cdd6f4;border:1px solid #45475a;border-radius:4px;padding:4px 8px;font-size:0.85rem}
.filters button{background:#89b4fa;color:#1e1e2e;border:none;border-radius:4px;padding:5px 14px;cursor:pointer;font-size:0.85rem;font-weight:600}
.filters button:hover{background:#74c7ec}
.count{color:#6c7086;font-size:0.8rem;margin-left:8px}
table{width:100%;border-collapse:collapse;font-size:0.82rem}
th{background:#313244;color:#a6adc8;text-align:left;padding:6px 10px;position:sticky;top:0}
td{padding:4px 10px;border-bottom:1px solid #313244;white-space:pre-wrap;word-break:break-all}
tr:hover{background:#313244}
.block{color:#f38ba8} .warning{color:#fab387} .info{color:#a6e3a1}
.service_start{color:#89b4fa} .service_stop{color:#9399b2}
.nav{margin-bottom:10px;display:flex;gap:8px}
.nav a{color:#89b4fa;text-decoration:none;font-size:0.85rem}
.nav a:hover{text-decoration:underline}
.page-switch{margin-bottom:12px}
.page-switch a{color:#1e1e2e;background:#89b4fa;text-decoration:none;font-size:0.85rem;padding:4px 14px;border-radius:4px;font-weight:600}
.page-switch a:hover{background:#74c7ec}
.page-switch span{color:#f9e2af;font-size:0.85rem;padding:4px 14px;border:1px solid #45475a;border-radius:4px;font-weight:600}
</style></head><body>`)

	// Top bar: day type + language switcher.
	fmt.Fprint(w, topBarHTML(h.statusProvider, lang, "/logs-html", "date="+date+"&type="+typeFilter))

	// Page switcher: Logs / Stats / Config
	fmt.Fprintf(w, `<div class="page-switch"><span>%s</span> <a href="/stats-html?date=%s&lang=%s">%s</a> <a href="/config-html?lang=%s">%s</a> <a href="/admin-html?lang=%s">%s</a></div>`,
		ui.logsTab, date, lang, ui.statsTab, lang, ui.configTab, lang, ui.adminTab)

	// Pause banner.
	writePauseBanner(w, h.statusProvider, lang)

	// Current balance bar.
	if h.statusProvider != nil {
		st := h.statusProvider.CurrentStatus()
		modeLabel := ""
		if st.ServiceMode != "" && st.ServiceMode != "normal" {
			ml := serviceModeLabels(lang)
			if name, ok := ml.modes[st.ServiceMode]; ok {
				modeLabel = name
			}
		}
		fmt.Fprint(w, `<div style="background:#313244;border-radius:6px;padding:8px 14px;margin-bottom:10px;font-size:0.85rem;display:flex;gap:16px;flex-wrap:wrap">`)
		if st.LimitMinutes > 0 {
			fmt.Fprintf(w, `<span style="color:#a6adc8">⏱ %d / %d min</span>`, st.EntertainmentMinutes, st.LimitMinutes)
			if st.MinutesRemaining > 0 {
				fmt.Fprintf(w, `<span style="color:#a6e3a1">↳ %d min left</span>`, st.MinutesRemaining)
			}
		} else {
			fmt.Fprintf(w, `<span style="color:#a6adc8">⏱ %d min</span>`, st.EntertainmentMinutes)
		}
		if st.BonusMinutes != 0 {
			fmt.Fprintf(w, `<span style="color:#f9e2af">bonus: %+d</span>`, st.BonusMinutes)
		}
		fmt.Fprintf(w, `<span style="color:#a6adc8">💻 %d min</span>`, st.ComputerMinutes)
		if modeLabel != "" {
			fmt.Fprintf(w, `<span style="color:#fab387">⚙ %s</span>`, htmlEscape(modeLabel))
		}
		fmt.Fprint(w, `</div>`)
	}

	// Фильтры.
	fmt.Fprintf(w, `<div class="filters">
<label>%s: <input type="date" id="dateInput" value="%s"></label>
<label>%s: <select id="typeSelect">
<option value="all"%s>%s</option>
<option value="block"%s>%s</option>
<option value="warning"%s>%s</option>
<option value="info"%s>%s</option>
<option value="service_start"%s>%s</option>
<option value="service_stop"%s>%s</option>
</select></label>
<button onclick="applyFilter()">%s</button>
<label><input type="checkbox" id="autoRef" onchange="toggleRefresh(this)"> %s</label>
<span class="count">%d %s</span>
</div>`,
		ui.dateLabel, date,
		ui.typeLabel,
		selAttr(typeFilter, "all"), ui.filterAll,
		selAttr(typeFilter, "block"), ui.filterBlock,
		selAttr(typeFilter, "warning"), ui.filterWarning,
		selAttr(typeFilter, "info"), ui.filterInfo,
		selAttr(typeFilter, "service_start"), ui.filterStart,
		selAttr(typeFilter, "service_stop"), ui.filterStop,
		ui.applyBtn, ui.autoRefresh,
		len(entries), ui.entriesLabel)

	fmt.Fprintf(w, `<script>
let timer;
function toggleRefresh(cb){if(cb.checked){timer=setInterval(()=>location.reload(),10000)}else{clearInterval(timer)}}
function applyFilter(){
  let d=document.getElementById('dateInput').value;
  let t=document.getElementById('typeSelect').value;
  location.href='?date='+d+'&type='+t+'&lang=%s';
}
</script>`, lang)

	fmt.Fprintf(w, `<table><tr><th>%s</th><th>%s</th><th style="color:#a6e3a1">⏱</th><th style="color:#f9e2af">+</th><th style="color:#f38ba8">−</th><th style="color:#89b4fa">💻</th><th style="color:#6c7086">CPU</th><th style="color:#6c7086">GPU</th><th style="color:#6c7086">MEM</th><th style="color:#6c7086">NET</th><th>%s</th><th>%s</th><th>%s</th></tr>`,
		ui.colTime, ui.colType, ui.colMessage, ui.colProcess, ui.colURL)

	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		ts := e.Timestamp.Format("15:04:05")
		msg := e.Message
		if lang != "en" {
			msg = translateLogMessage(msg, lang)
		}

		balCol := ""
		if e.EntertainmentLimit > 0 {
			balCol = fmt.Sprintf("%d/%d", e.EntertainmentUsed, e.EntertainmentLimit)
		} else if e.EntertainmentUsed > 0 {
			balCol = fmt.Sprintf("%d", e.EntertainmentUsed)
		}

		addCol, subCol := "", ""
		if e.BonusMinutes > 0 {
			addCol = fmt.Sprintf("+%d", e.BonusMinutes)
		} else if e.BonusMinutes < 0 {
			subCol = fmt.Sprintf("%d", e.BonusMinutes)
		}

		compCol := ""
		if e.ComputerMinutes > 0 {
			compCol = fmt.Sprintf("%d", e.ComputerMinutes)
		}

		cpuCol, gpuCol, memCol, netCol := "", "", "", ""
		if e.CPUPercent > 0 {
			cpuCol = fmt.Sprintf("%.0f%%", e.CPUPercent)
		}
		if e.GPUPercent > 0 {
			gpuCol = fmt.Sprintf("%.0f%%", e.GPUPercent)
		}
		if e.MemoryPercent > 0 {
			memCol = fmt.Sprintf("%.0f%%", e.MemoryPercent)
		}
		if e.NetMBps > 0.01 {
			netCol = fmt.Sprintf("%.1f", e.NetMBps)
		}

		fmt.Fprintf(w, `<tr class="%s"><td>%s</td><td>%s</td><td style="color:#a6e3a1;font-size:0.78rem">%s</td><td style="color:#f9e2af;font-size:0.78rem">%s</td><td style="color:#f38ba8;font-size:0.78rem">%s</td><td style="color:#89b4fa;font-size:0.78rem">%s</td><td style="color:#6c7086;font-size:0.78rem">%s</td><td style="color:#6c7086;font-size:0.78rem">%s</td><td style="color:#6c7086;font-size:0.78rem">%s</td><td style="color:#6c7086;font-size:0.78rem">%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			e.EventType, ts, e.EventType, balCol, addCol, subCol, compCol, cpuCol, gpuCol, memCol, netCol, htmlEscape(msg), htmlEscape(e.ProcessName), htmlEscape(e.URL))
	}

	fmt.Fprint(w, `</table>`)

	fmt.Fprint(w, `</body></html>`)
}

// logsUI holds translated labels for the logs HTML page.
type logsUI struct {
	title, dateLabel, typeLabel                                         string
	filterAll, filterBlock, filterWarning, filterInfo, filterStart, filterStop string
	applyBtn, autoRefresh, entriesLabel                                string
	colTime, colType, colMessage, colProcess, colURL                   string
	logsTab, statsTab, configTab, adminTab                             string
}

func logsUILabels(lang string) logsUI {
	switch lang {
	case "ru":
		return logsUI{
			title: "Родительский контроль — Логи", dateLabel: "Дата", typeLabel: "Тип",
			filterAll: "Все", filterBlock: "Блокировка", filterWarning: "Предупреждение",
			filterInfo: "Инфо", filterStart: "Старт", filterStop: "Стоп",
			applyBtn: "Применить", autoRefresh: "Авто-обновление", entriesLabel: "записей",
			colTime: "Время", colType: "Тип", colMessage: "Сообщение", colProcess: "Процесс", colURL: "URL",
			logsTab: "Логи", statsTab: "Статистика", configTab: "Конфигурация", adminTab: "Управление",
		}
	case "it":
		return logsUI{
			title: "Controllo Genitori — Log", dateLabel: "Data", typeLabel: "Tipo",
			filterAll: "Tutto", filterBlock: "Blocco", filterWarning: "Avviso",
			filterInfo: "Info", filterStart: "Avvio", filterStop: "Arresto",
			applyBtn: "Applica", autoRefresh: "Aggiornamento automatico", entriesLabel: "voci",
			colTime: "Ora", colType: "Tipo", colMessage: "Messaggio", colProcess: "Processo", colURL: "URL",
			logsTab: "Log", statsTab: "Statistiche", configTab: "Configurazione", adminTab: "Gestione",
		}
	case "es":
		return logsUI{
			title: "Control Parental — Registros", dateLabel: "Fecha", typeLabel: "Tipo",
			filterAll: "Todo", filterBlock: "Bloqueo", filterWarning: "Advertencia",
			filterInfo: "Info", filterStart: "Inicio", filterStop: "Parada",
			applyBtn: "Aplicar", autoRefresh: "Auto-actualización", entriesLabel: "entradas",
			colTime: "Hora", colType: "Tipo", colMessage: "Mensaje", colProcess: "Proceso", colURL: "URL",
			logsTab: "Registros", statsTab: "Estadísticas", configTab: "Configuración", adminTab: "Administración",
		}
	case "de":
		return logsUI{
			title: "Kindersicherung — Protokolle", dateLabel: "Datum", typeLabel: "Typ",
			filterAll: "Alle", filterBlock: "Sperre", filterWarning: "Warnung",
			filterInfo: "Info", filterStart: "Start", filterStop: "Stopp",
			applyBtn: "Anwenden", autoRefresh: "Auto-Aktualisierung", entriesLabel: "Einträge",
			colTime: "Zeit", colType: "Typ", colMessage: "Nachricht", colProcess: "Prozess", colURL: "URL",
			logsTab: "Protokolle", statsTab: "Statistiken", configTab: "Konfiguration", adminTab: "Verwaltung",
		}
	case "pl":
		return logsUI{
			title: "Kontrola Rodzicielska — Logi", dateLabel: "Data", typeLabel: "Typ",
			filterAll: "Wszystko", filterBlock: "Blokada", filterWarning: "Ostrzeżenie",
			filterInfo: "Info", filterStart: "Start", filterStop: "Stop",
			applyBtn: "Zastosuj", autoRefresh: "Auto-odświeżanie", entriesLabel: "wpisów",
			colTime: "Czas", colType: "Typ", colMessage: "Wiadomość", colProcess: "Proces", colURL: "URL",
			logsTab: "Logi", statsTab: "Statystyki", configTab: "Konfiguracja", adminTab: "Zarządzanie",
		}
	case "zh-TW":
		return logsUI{
			title: "家長控制 — 日誌", dateLabel: "日期", typeLabel: "類型",
			filterAll: "全部", filterBlock: "封鎖", filterWarning: "警告",
			filterInfo: "資訊", filterStart: "啟動", filterStop: "停止",
			applyBtn: "套用", autoRefresh: "自動重新整理", entriesLabel: "筆記錄",
			colTime: "時間", colType: "類型", colMessage: "訊息", colProcess: "程序", colURL: "URL",
			logsTab: "日誌", statsTab: "統計", configTab: "設定", adminTab: "管理",
		}
	default:
		return logsUI{
			title: "Parental Control — Logs", dateLabel: "Date", typeLabel: "Type",
			filterAll: "All", filterBlock: "Block", filterWarning: "Warning",
			filterInfo: "Info", filterStart: "Start", filterStop: "Stop",
			applyBtn: "Apply", autoRefresh: "Auto-refresh", entriesLabel: "entries",
			colTime: "Time", colType: "Type", colMessage: "Message", colProcess: "Process", colURL: "URL",
			logsTab: "Logs", statsTab: "Statistics", configTab: "Configuration", adminTab: "Admin",
		}
	}
}

// adjustmentUI — переводы для секций ручных корректировок.
type adjustmentUI struct {
	addUsageTitle string
	bonusTitle    string
	sleepTitle    string
	minutes       string
	bonusMinutes  string
	reason        string
	reasonHint    string
	sleepStart    string
	sleepEnd      string
	applyBtn      string
}

func adjustmentUILabels(lang string) adjustmentUI {
	switch lang {
	case "ru":
		return adjustmentUI{"Добавить использование", "Бонусное время", "Время сна (сегодня)", "Минуты", "Минуты (+/-)", "Причина", "Играл на другом компьютере", "Начало сна", "Конец сна", "Применить"}
	case "it":
		return adjustmentUI{"Aggiungi utilizzo", "Tempo bonus", "Orario sonno (oggi)", "Minuti", "Minuti (+/-)", "Motivo", "Giocato su altro computer", "Inizio sonno", "Fine sonno", "Applica"}
	case "es":
		return adjustmentUI{"Agregar uso", "Tiempo bonus", "Hora de dormir (hoy)", "Minutos", "Minutos (+/-)", "Razón", "Jugó en otra computadora", "Inicio sueño", "Fin sueño", "Aplicar"}
	case "de":
		return adjustmentUI{"Nutzung hinzufügen", "Bonuszeit", "Schlafenszeit (heute)", "Minuten", "Minuten (+/-)", "Grund", "Auf anderem Computer gespielt", "Schlafbeginn", "Schlafende", "Anwenden"}
	case "pl":
		return adjustmentUI{"Dodaj użycie", "Czas bonusowy", "Pora snu (dziś)", "Minuty", "Minuty (+/-)", "Powód", "Grał na innym komputerze", "Początek snu", "Koniec snu", "Zastosuj"}
	case "zh-TW":
		return adjustmentUI{"新增使用時間", "獎勵時間", "睡眠時間（今天）", "分鐘", "分鐘（+/-）", "原因", "在其他電腦上玩", "睡眠開始", "睡眠結束", "套用"}
	default:
		return adjustmentUI{"Add Usage", "Bonus Time", "Sleep Time (today)", "Minutes", "Minutes (+/-)", "Reason", "Played on another computer", "Sleep Start", "Sleep End", "Apply"}
	}
}

// learningUI — переводы для секции логов обучения.
type learningUI struct {
	title      string
	loading    string
	noReports  string
	refreshBtn string
	clearBtn   string
}

func learningUILabels(lang string) learningUI {
	switch lang {
	case "ru":
		return learningUI{"Логи обучения", "Загрузка...", "Нет отчётов", "Обновить", "Очистить все"}
	case "it":
		return learningUI{"Log di apprendimento", "Caricamento...", "Nessun report", "Aggiorna", "Cancella tutto"}
	case "es":
		return learningUI{"Registros de aprendizaje", "Cargando...", "Sin informes", "Actualizar", "Borrar todo"}
	case "de":
		return learningUI{"Lernprotokolle", "Laden...", "Keine Berichte", "Aktualisieren", "Alle löschen"}
	case "pl":
		return learningUI{"Logi nauki", "Ładowanie...", "Brak raportów", "Odśwież", "Wyczyść wszystko"}
	case "zh-TW":
		return learningUI{"學習日誌", "載入中...", "無報告", "重新整理", "清除全部"}
	default:
		return learningUI{"Learning Logs", "Loading...", "No reports", "Refresh", "Clear All"}
	}
}

// serviceModeUI — переводы для секции режимов.
type serviceModeUI struct {
	title        string
	currentLabel string
	modes        map[string]string
	minutesLabel string
	minutesHint  string
	applyBtn     string
}

func serviceModeLabels(lang string) serviceModeUI {
	modes := map[string]map[string]string{
		"en":    {"normal": "Normal", "filter_paused": "Filter Paused", "entertainment_paused": "Entertainment Paused", "learning": "Learning Mode", "unrestricted": "Unrestricted", "self_entertainment_paused": "Entertainment Paused (self)"},
		"ru":    {"normal": "Обычный", "filter_paused": "Фильтрация приостановлена", "entertainment_paused": "Развлечения приостановлены", "learning": "Режим обучения", "unrestricted": "Без ограничений", "self_entertainment_paused": "Развлечения приостановлены (ребёнок)"},
		"it":    {"normal": "Normale", "filter_paused": "Filtro in pausa", "entertainment_paused": "Intrattenimento in pausa", "learning": "Modalità apprendimento", "unrestricted": "Senza restrizioni"},
		"es":    {"normal": "Normal", "filter_paused": "Filtro en pausa", "entertainment_paused": "Entretenimiento en pausa", "learning": "Modo aprendizaje", "unrestricted": "Sin restricciones"},
		"de":    {"normal": "Normal", "filter_paused": "Filter pausiert", "entertainment_paused": "Unterhaltung pausiert", "learning": "Lernmodus", "unrestricted": "Uneingeschränkt"},
		"pl":    {"normal": "Normalny", "filter_paused": "Filtrowanie wstrzymane", "entertainment_paused": "Rozrywka wstrzymana", "learning": "Tryb nauki", "unrestricted": "Bez ograniczeń"},
		"zh-TW": {"normal": "正常", "filter_paused": "過濾已暫停", "entertainment_paused": "娛樂已暫停", "learning": "學習模式", "unrestricted": "無限制"},
	}
	labels := map[string][4]string{
		"en":    {"Service Mode", "Current mode", "Minutes (0 = unlimited)", "0 = until manually changed"},
		"ru":    {"Режим работы", "Текущий режим", "Минуты (0 = бессрочно)", "0 = до ручной отмены"},
		"it":    {"Modalità servizio", "Modalità attuale", "Minuti (0 = illimitato)", "0 = fino a modifica manuale"},
		"es":    {"Modo de servicio", "Modo actual", "Minutos (0 = ilimitado)", "0 = hasta cambio manual"},
		"de":    {"Dienstmodus", "Aktueller Modus", "Minuten (0 = unbegrenzt)", "0 = bis zur manuellen Änderung"},
		"pl":    {"Tryb usługi", "Aktualny tryb", "Minuty (0 = bez limitu)", "0 = do ręcznej zmiany"},
		"zh-TW": {"服務模式", "目前模式", "分鐘（0 = 無限制）", "0 = 直到手動變更"},
	}
	applyBtns := map[string]string{
		"en": "Apply", "ru": "Применить", "it": "Applica", "es": "Aplicar",
		"de": "Anwenden", "pl": "Zastosuj", "zh-TW": "套用",
	}

	m := modes["en"]
	if v, ok := modes[lang]; ok {
		m = v
	}
	l := labels["en"]
	if v, ok := labels[lang]; ok {
		l = v
	}
	btn := applyBtns["en"]
	if v, ok := applyBtns[lang]; ok {
		btn = v
	}

	return serviceModeUI{
		title: l[0], currentLabel: l[1], modes: m,
		minutesLabel: l[2], minutesHint: l[3], applyBtn: btn,
	}
}

// translateAdjustmentLog переводит лог-сообщения о корректировках времени.
func translateAdjustmentLog(msg string, lang string) string {
	labels := map[string]map[string]string{
		"manual_usage": {
			"ru": "Добавлено использование: ", "it": "Utilizzo aggiunto: ", "es": "Uso agregado: ",
			"de": "Nutzung hinzugefügt: ", "pl": "Dodano użycie: ", "zh-TW": "已新增使用時間：",
		},
		"bonus_added": {
			"ru": "Бонусное время добавлено: ", "it": "Tempo bonus aggiunto: ", "es": "Tiempo bonus agregado: ",
			"de": "Bonuszeit hinzugefügt: ", "pl": "Czas bonusowy dodany: ", "zh-TW": "已新增獎勵時間：",
		},
		"bonus_removed": {
			"ru": "Бонусное время убрано: ", "it": "Tempo bonus rimosso: ", "es": "Tiempo bonus eliminado: ",
			"de": "Bonuszeit entfernt: ", "pl": "Czas bonusowy usunięty: ", "zh-TW": "已移除獎勵時間：",
		},
		"sleep_adjusted": {
			"ru": "Время сна изменено", "it": "Orario sonno modificato", "es": "Hora de dormir ajustada",
			"de": "Schlafenszeit angepasst", "pl": "Pora snu zmieniona", "zh-TW": "睡眠時間已調整",
		},
		"mode_set": {
			"ru": "Режим установлен: ", "it": "Modalità impostata: ", "es": "Modo establecido: ",
			"de": "Modus gesetzt: ", "pl": "Tryb ustawiony: ", "zh-TW": "模式已設定：",
		},
		"reason": {
			"ru": "Причина: ", "it": "Motivo: ", "es": "Razón: ",
			"de": "Grund: ", "pl": "Powód: ", "zh-TW": "原因：",
		},
		"total_bonus": {
			"ru": "Всего бонус: ", "it": "Bonus totale: ", "es": "Bonus total: ",
			"de": "Bonus gesamt: ", "pl": "Łączny bonus: ", "zh-TW": "總獎勵：",
		},
		"min": {
			"ru": "мин.", "it": "min.", "es": "min.", "de": "Min.", "pl": "min.", "zh-TW": "分鐘",
		},
		"start": {
			"ru": "Начало: ", "it": "Inizio: ", "es": "Inicio: ", "de": "Beginn: ", "pl": "Początek: ", "zh-TW": "開始：",
		},
		"end": {
			"ru": "Конец: ", "it": "Fine: ", "es": "Fin: ", "de": "Ende: ", "pl": "Koniec: ", "zh-TW": "結束：",
		},
		"for_date": {
			"ru": " на ", "it": " per ", "es": " para ", "de": " für ", "pl": " na ", "zh-TW": " 於 ",
		},
	}

	tr := func(key string) string {
		if m, ok := labels[key]; ok {
			if v, ok := m[lang]; ok {
				return v
			}
		}
		return ""
	}

	// "Manual usage added: 20 min. Reason: ..."
	if strings.HasPrefix(msg, "Manual usage added: ") {
		rest := msg[len("Manual usage added: "):]
		result := tr("manual_usage") + rest
		result = strings.Replace(result, " min.", " "+tr("min"), 1)
		result = strings.Replace(result, "Reason: ", tr("reason"), 1)
		return result
	}

	// "Bonus time added: +30 min. Total bonus: +30 min. Reason: ..."
	if strings.HasPrefix(msg, "Bonus time added: ") || strings.HasPrefix(msg, "Bonus time removed: ") {
		prefix := tr("bonus_added")
		if strings.HasPrefix(msg, "Bonus time removed: ") {
			prefix = tr("bonus_removed")
			msg = msg[len("Bonus time removed: "):]
		} else {
			msg = msg[len("Bonus time added: "):]
		}
		result := prefix + msg
		result = strings.ReplaceAll(result, " min.", " "+tr("min"))
		result = strings.Replace(result, "Total bonus: ", tr("total_bonus"), 1)
		result = strings.Replace(result, "Reason: ", tr("reason"), 1)
		return result
	}

	// "Sleep time adjusted for 2026-03-28. Start: 23:30. End: 07:30. Reason: ..."
	if strings.HasPrefix(msg, "Sleep time adjusted") {
		result := tr("sleep_adjusted")
		rest := msg[len("Sleep time adjusted"):]
		rest = strings.Replace(rest, " for ", tr("for_date"), 1)
		rest = strings.Replace(rest, " Start: ", " "+tr("start"), 1)
		rest = strings.Replace(rest, " End: ", " "+tr("end"), 1)
		rest = strings.Replace(rest, " Reason: ", " "+tr("reason"), 1)
		return result + rest
	}

	// "Mode set to learning for 60 min."
	if strings.HasPrefix(msg, "Mode set to ") {
		rest := msg[len("Mode set to "):]
		result := tr("mode_set") + rest
		result = strings.Replace(result, " min.", " "+tr("min"), 1)
		return result
	}

	return msg
}

// translateScheduleLog переводит лог-сообщения о типе дня.
func translateScheduleLog(msg string, lang string) string {
	dayTypes := map[string]map[string]string{
		"workday": {"ru": "рабочий день", "it": "giorno lavorativo", "es": "día laboral", "de": "Werktag", "pl": "dzień roboczy", "zh-TW": "工作日"},
		"weekend": {"ru": "выходной", "it": "fine settimana", "es": "fin de semana", "de": "Wochenende", "pl": "weekend", "zh-TW": "週末"},
		"holiday": {"ru": "праздник", "it": "festivo", "es": "festivo", "de": "Feiertag", "pl": "święto", "zh-TW": "假日"},
	}

	startedLabels := map[string]string{
		"ru": "Сервис запущен. Расписание: ", "it": "Servizio avviato. Orario: ", "es": "Servicio iniciado. Horario: ",
		"de": "Dienst gestartet. Zeitplan: ", "pl": "Usługa uruchomiona. Harmonogram: ", "zh-TW": "服務已啟動。時間表：",
	}
	schedLabels := map[string]string{
		"ru": "Расписание: ", "it": "Orario: ", "es": "Horario: ",
		"de": "Zeitplan: ", "pl": "Harmonogram: ", "zh-TW": "時間表：",
	}

	// Extract day type and optional holiday name.
	var prefix, rest string
	if strings.HasPrefix(msg, "ParentalControlService started. Schedule: ") {
		rest = msg[len("ParentalControlService started. Schedule: "):]
		prefix = startedLabels[lang]
		if prefix == "" {
			return msg
		}
	} else if strings.HasPrefix(msg, "Schedule: ") {
		rest = msg[len("Schedule: "):]
		prefix = schedLabels[lang]
		if prefix == "" {
			return msg
		}
	} else {
		return msg
	}

	// rest = "weekend" or "holiday (Easter Sunday)"
	dayType := rest
	holidayName := ""
	if idx := strings.Index(rest, " ("); idx >= 0 {
		dayType = rest[:idx]
		holidayName = rest[idx:]
	}

	if translations, ok := dayTypes[dayType]; ok {
		if translated, ok := translations[lang]; ok {
			return prefix + translated + holidayName
		}
	}
	return prefix + rest
}

// translateLogMessage translates English log messages to the target language.
func translateLogMessage(msg string, lang string) string {
	// Schedule day type translations.
	if strings.HasPrefix(msg, "Schedule: ") || strings.HasPrefix(msg, "ParentalControlService started. Schedule: ") {
		return translateScheduleLog(msg, lang)
	}

	// Adjustment messages (all languages).
	if strings.HasPrefix(msg, "Manual usage added: ") ||
		strings.HasPrefix(msg, "Bonus time ") ||
		strings.HasPrefix(msg, "Sleep time adjusted") ||
		strings.HasPrefix(msg, "Mode set to ") {
		return translateAdjustmentLog(msg, lang)
	}

	// For non-Russian languages, only translate schedule/adjustment messages.
	if lang != "ru" {
		return msg
	}

	// --- Russian translations below ---
	// Exact matches.
	exact := map[string]string{
		"ParentalControlService started":                              "Сервис запущен",
		"ParentalControlService stopped":                              "Сервис остановлен",
		"Pause removed":                                               "Пауза снята",
		"Password changed":                                            "Пароль изменён",
		"Config URL changed":                                          "URL конфигурации изменён",
		"Config reload failed (network error). Using cached config.":  "Ошибка обновления конфига (нет соединения). Используется кешированный конфиг.",
		"Config update failed (network error). Using cached config.":  "Ошибка обновления конфига (нет соединения). Используется кешированный конфиг.",
		"Sleep time started. All apps will be closed.":                "Наступило время сна. Все программы будут закрыты.",
		"Entertainment time is over. Restricted apps will be closed.": "Время развлечений закончилось. Неразрешённые программы будут закрыты.",
		"Sleep time. Computer is unavailable":                         "Время сна. Компьютер недоступен",
		"This app is not allowed right now":                           "Эта программа сейчас не разрешена",
		"Entertainment time is over for today":                        "Время развлечений на сегодня закончилось",
		"Service config unavailable. Apps are temporarily blocked.":   "Конфигурация недоступна. Программы временно заблокированы.",
	}
	if v, ok := exact[msg]; ok {
		return v
	}

	// Pattern-based translations.
	patterns := []struct {
		prefix, suffix string
		format         string
	}{
		{"Pause set for ", " min.", "Пауза установлена на %s мин."},
		{"Entertainment time ends in ", " min.", "До конца развлечений осталось %s мин."},
		{"Sleep time starts in ", " min.", "До начала сна осталось %s мин."},
		{"Entertainment ", " min. Active: ", ""},
		{"Blocked site: ", "", "Заблокирован сайт: %s"},
		{"Sleep time: blocked ", "", "Время сна: заблокирован %s"},
		{"Config URL changed: ", "", "URL конфигурации изменён: %s"},
	}
	for _, p := range patterns {
		if !strings.HasPrefix(msg, p.prefix) {
			continue
		}
		rest := msg[len(p.prefix):]
		if p.suffix != "" {
			idx := strings.Index(rest, p.suffix)
			if idx < 0 {
				continue
			}
			val := rest[:idx]
			if p.format != "" {
				return fmt.Sprintf(p.format, val)
			}
		} else if p.format != "" {
			return fmt.Sprintf(p.format, rest)
		}
	}

	// Special: "Entertainment N min. Active: ..."
	if strings.HasPrefix(msg, "Entertainment ") && strings.Contains(msg, " min. Active: ") {
		parts := strings.SplitN(msg, " min. Active: ", 2)
		num := strings.TrimPrefix(parts[0], "Entertainment ")
		active := parts[1]
		return fmt.Sprintf("Развлечения %s мин. Активные: %s", num, active)
	}

	// Config update messages.
	if strings.HasPrefix(msg, "Config updated. ") || strings.HasPrefix(msg, "Config reloaded (manual). ") {
		prefix := "Конфиг обновлён. "
		details := msg
		if strings.HasPrefix(msg, "Config reloaded (manual). ") {
			prefix = "Конфиг обновлён (вручную). "
			details = msg[len("Config reloaded (manual). "):]
		} else {
			details = msg[len("Config updated. "):]
		}
		details = strings.ReplaceAll(details, "No changes", "Без изменений")
		details = strings.ReplaceAll(details, "Apps:", "Программы:")
		details = strings.ReplaceAll(details, "Sites:", "Сайты:")
		details = strings.ReplaceAll(details, "Windows:", "Окна:")
		details = strings.ReplaceAll(details, "Sleep:", "Сон:")
		details = strings.ReplaceAll(details, "EntApps:", "Развл.прил.:")
		details = strings.ReplaceAll(details, "FullLog:", "Полн.лог:")
		return prefix + details
	}

	return msg
}

// handleStatsHTML handles GET /stats-html — HTML page with usage statistics.
// Params: ?date=2026-03-24 (default today), ?type=all|app|site|restricted, ?lang=en|ru
func (h *HTTPLogServer) handleStatsHTML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	typeFilter := r.URL.Query().Get("type")
	if typeFilter == "" {
		typeFilter = "all"
	}
	lang := r.URL.Query().Get("lang")
	lang = normalizeLang(lang)

	ui := statsUILabels(lang)

	// Fetch week stats for navigation, day stats for the selected date.
	type appEntry struct {
		Name         string
		Type         string
		IsRestricted bool
		TotalSeconds int
	}
	type dayData struct {
		Date                 string
		EntertainmentSeconds int
		Apps                 []appEntry
	}

	// Parse day stats from JSON interface.
	parseDayStats := func(raw interface{}) dayData {
		data, _ := json.Marshal(raw)
		var d struct {
			Date                 string `json:"date"`
			EntertainmentSeconds int    `json:"entertainment_seconds"`
			Apps                 []struct {
				Name         string `json:"name"`
				Type         string `json:"type"`
				IsRestricted bool   `json:"is_restricted"`
				TotalSeconds int    `json:"total_seconds"`
			} `json:"apps"`
		}
		json.Unmarshal(data, &d)
		dd := dayData{Date: d.Date, EntertainmentSeconds: d.EntertainmentSeconds}
		for _, a := range d.Apps {
			dd.Apps = append(dd.Apps, appEntry{
				Name: a.Name, Type: a.Type,
				IsRestricted: a.IsRestricted, TotalSeconds: a.TotalSeconds,
			})
		}
		return dd
	}

	var day dayData
	if h.statsProvider != nil {
		day = parseDayStats(h.statsProvider.GetDayStats(date))
	}

	// Filter apps.
	var filtered []appEntry
	for _, a := range day.Apps {
		switch typeFilter {
		case "app":
			if a.Type != "app" {
				continue
			}
		case "site":
			if a.Type != "site" {
				continue
			}
		case "restricted":
			if !a.IsRestricted {
				continue
			}
		}
		filtered = append(filtered, a)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html><html><head><meta charset="utf-8">
<title>`)
	fmt.Fprint(w, htmlEscape(ui.title))
	fmt.Fprint(w, `</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:'Segoe UI',sans-serif;background:#1e1e2e;color:#cdd6f4;padding:16px}
.filters{margin-bottom:14px;display:flex;gap:12px;align-items:center;flex-wrap:wrap}
.filters label{color:#a6adc8;font-size:0.85rem}
.filters input,.filters select{background:#313244;color:#cdd6f4;border:1px solid #45475a;border-radius:4px;padding:4px 8px;font-size:0.85rem}
.filters button{background:#89b4fa;color:#1e1e2e;border:none;border-radius:4px;padding:5px 14px;cursor:pointer;font-size:0.85rem;font-weight:600}
.filters button:hover{background:#74c7ec}
.summary{margin-bottom:14px;color:#a6adc8;font-size:0.9rem}
.summary span{color:#f9e2af;font-weight:600}
table{width:100%;border-collapse:collapse;font-size:0.85rem}
th{background:#313244;color:#a6adc8;text-align:left;padding:6px 10px;position:sticky;top:0}
td{padding:5px 10px;border-bottom:1px solid #313244}
tr:hover{background:#313244}
.restricted{color:#f38ba8} .allowed{color:#a6e3a1}
.bar-cell{width:40%} .bar{height:14px;border-radius:3px;min-width:2px}
.bar-r{background:#f38ba8} .bar-a{background:#89b4fa}
.nav{margin-bottom:10px;display:flex;gap:8px}
.nav a{color:#89b4fa;text-decoration:none;font-size:0.85rem}
.nav a:hover{text-decoration:underline}
.count{color:#6c7086;font-size:0.8rem;margin-left:8px}
.page-switch{margin-bottom:12px}
.page-switch a{color:#1e1e2e;background:#89b4fa;text-decoration:none;font-size:0.85rem;padding:4px 14px;border-radius:4px;font-weight:600}
.page-switch a:hover{background:#74c7ec}
.page-switch span{color:#f9e2af;font-size:0.85rem;padding:4px 14px;border:1px solid #45475a;border-radius:4px;font-weight:600}
</style></head><body>`)

	// Top bar: day type + language switcher.
	fmt.Fprint(w, topBarHTML(h.statusProvider, lang, "/stats-html", "date="+date+"&type="+typeFilter))

	// Page switcher: Logs / Stats / Config
	fmt.Fprintf(w, `<div class="page-switch"><a href="/logs-html?date=%s&lang=%s">%s</a> <span>%s</span> <a href="/config-html?lang=%s">%s</a> <a href="/admin-html?lang=%s">%s</a></div>`,
		date, lang, ui.logsTab, ui.statsTab, lang, ui.configTab, lang, ui.adminTab)

	// Pause banner.
	writePauseBanner(w, h.statusProvider, lang)

	// Summary.
	entMins := day.EntertainmentSeconds / 60
	fmt.Fprintf(w, `<div class="summary">%s: <span>%d %s</span>`, ui.entertainment, entMins, ui.min)

	// Общее время за компьютером из текущего статуса.
	if h.statusProvider != nil {
		st := h.statusProvider.CurrentStatus()
		if st.ComputerLimitMinutes > 0 {
			fmt.Fprintf(w, ` &nbsp;|&nbsp; %s: <span>%d / %d %s</span>`,
				ui.computer, st.ComputerMinutes, st.ComputerLimitMinutes, ui.min)
		} else {
			fmt.Fprintf(w, ` &nbsp;|&nbsp; %s: <span>%d %s</span>`,
				ui.computer, st.ComputerMinutes, ui.min)
		}
	}
	fmt.Fprint(w, `</div>`)

	// Filters.
	fmt.Fprintf(w, `<div class="filters">
<label>%s: <input type="date" id="dateInput" value="%s"></label>
<label>%s: <select id="typeSelect">
<option value="all"%s>%s</option>
<option value="app"%s>%s</option>
<option value="site"%s>%s</option>
<option value="restricted"%s>%s</option>
</select></label>
<button onclick="applyFilter()">%s</button>
<span class="count">%d %s</span>
</div>`,
		ui.dateLabel, date,
		ui.typeLabel,
		selAttr(typeFilter, "all"), ui.filterAll,
		selAttr(typeFilter, "app"), ui.filterApp,
		selAttr(typeFilter, "site"), ui.filterSite,
		selAttr(typeFilter, "restricted"), ui.filterRestricted,
		ui.applyBtn,
		len(filtered), ui.entriesLabel)

	fmt.Fprintf(w, `<script>
function applyFilter(){
  var d=document.getElementById('dateInput').value;
  var t=document.getElementById('typeSelect').value;
  location.href='?date='+d+'&type='+t+'&lang=%s';
}
</script>`, lang)

	// Table.
	// Find max seconds for bar scaling.
	maxSec := 1
	for _, a := range filtered {
		if a.TotalSeconds > maxSec {
			maxSec = a.TotalSeconds
		}
	}

	fmt.Fprintf(w, `<table><tr><th>%s</th><th>%s</th><th>%s</th><th>%s</th><th>%s</th></tr>`,
		ui.colName, ui.colType, ui.colTime, ui.colStatus, ui.colBar)

	for _, a := range filtered {
		mins := a.TotalSeconds / 60
		secs := a.TotalSeconds % 60
		timeStr := fmt.Sprintf("%dm %02ds", mins, secs)

		typeLabel := ui.typeApp
		if a.Type == "site" {
			typeLabel = ui.typeSite
		}

		statusClass := "allowed"
		statusLabel := ui.statusAllowed
		if a.IsRestricted {
			statusClass = "restricted"
			statusLabel = ui.statusRestricted
		}

		barPct := a.TotalSeconds * 100 / maxSec
		barClass := "bar-a"
		if a.IsRestricted {
			barClass = "bar-r"
		}

		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td class="%s">%s</td><td class="bar-cell"><div class="bar %s" style="width:%d%%"></div></td></tr>`,
			htmlEscape(a.Name), typeLabel, timeStr, statusClass, statusLabel, barClass, barPct)
	}

	fmt.Fprint(w, `</table>`)

	fmt.Fprint(w, `</body></html>`)
}

// statsUI holds translated labels for the stats HTML page.
type statsUI struct {
	title, dateLabel, typeLabel                          string
	filterAll, filterApp, filterSite, filterRestricted   string
	applyBtn, entriesLabel                               string
	entertainment, min, computer                          string
	colName, colType, colTime, colStatus, colBar         string
	typeApp, typeSite, statusAllowed, statusRestricted   string
	logsTab, statsTab, configTab, adminTab               string
}

func statsUILabels(lang string) statsUI {
	switch lang {
	case "ru":
		return statsUI{
			title: "Родительский контроль — Статистика", dateLabel: "Дата", typeLabel: "Фильтр",
			filterAll: "Все", filterApp: "Программы", filterSite: "Сайты", filterRestricted: "Развлечения",
			applyBtn: "Применить", entriesLabel: "записей",
			entertainment: "Развлечения за день", min: "мин", computer: "Компьютер",
			colName: "Название", colType: "Тип", colTime: "Время", colStatus: "Статус", colBar: "",
			typeApp: "Программа", typeSite: "Сайт", statusAllowed: "разрешено", statusRestricted: "развлечение",
			logsTab: "Логи", statsTab: "Статистика", configTab: "Конфигурация", adminTab: "Управление",
		}
	case "it":
		return statsUI{
			title: "Controllo Genitori — Statistiche", dateLabel: "Data", typeLabel: "Filtro",
			filterAll: "Tutto", filterApp: "App", filterSite: "Siti", filterRestricted: "Limitati",
			applyBtn: "Applica", entriesLabel: "voci",
			entertainment: "Intrattenimento oggi", min: "min", computer: "Computer",
			colName: "Nome", colType: "Tipo", colTime: "Tempo", colStatus: "Stato", colBar: "",
			typeApp: "App", typeSite: "Sito", statusAllowed: "consentito", statusRestricted: "limitato",
			logsTab: "Log", statsTab: "Statistiche", configTab: "Configurazione", adminTab: "Gestione",
		}
	case "es":
		return statsUI{
			title: "Control Parental — Estadísticas", dateLabel: "Fecha", typeLabel: "Filtro",
			filterAll: "Todo", filterApp: "Apps", filterSite: "Sitios", filterRestricted: "Restringidos",
			applyBtn: "Aplicar", entriesLabel: "entradas",
			entertainment: "Entretenimiento hoy", min: "min", computer: "Computadora",
			colName: "Nombre", colType: "Tipo", colTime: "Tiempo", colStatus: "Estado", colBar: "",
			typeApp: "App", typeSite: "Sitio", statusAllowed: "permitido", statusRestricted: "restringido",
			logsTab: "Registros", statsTab: "Estadísticas", configTab: "Configuración", adminTab: "Administración",
		}
	case "de":
		return statsUI{
			title: "Kindersicherung — Statistiken", dateLabel: "Datum", typeLabel: "Filter",
			filterAll: "Alle", filterApp: "Apps", filterSite: "Seiten", filterRestricted: "Eingeschränkt",
			applyBtn: "Anwenden", entriesLabel: "Einträge",
			entertainment: "Unterhaltung heute", min: "Min.", computer: "Computer",
			colName: "Name", colType: "Typ", colTime: "Zeit", colStatus: "Status", colBar: "",
			typeApp: "App", typeSite: "Seite", statusAllowed: "erlaubt", statusRestricted: "eingeschränkt",
			logsTab: "Protokolle", statsTab: "Statistiken", configTab: "Konfiguration", adminTab: "Verwaltung",
		}
	case "pl":
		return statsUI{
			title: "Kontrola Rodzicielska — Statystyki", dateLabel: "Data", typeLabel: "Filtr",
			filterAll: "Wszystko", filterApp: "Aplikacje", filterSite: "Strony", filterRestricted: "Ograniczone",
			applyBtn: "Zastosuj", entriesLabel: "wpisów",
			entertainment: "Rozrywka dzisiaj", min: "min", computer: "Komputer",
			colName: "Nazwa", colType: "Typ", colTime: "Czas", colStatus: "Status", colBar: "",
			typeApp: "Aplikacja", typeSite: "Strona", statusAllowed: "dozwolone", statusRestricted: "ograniczone",
			logsTab: "Logi", statsTab: "Statystyki", configTab: "Konfiguracja", adminTab: "Zarządzanie",
		}
	case "zh-TW":
		return statsUI{
			title: "家長控制 — 統計", dateLabel: "日期", typeLabel: "篩選",
			filterAll: "全部", filterApp: "應用程式", filterSite: "網站", filterRestricted: "受限",
			applyBtn: "套用", entriesLabel: "筆記錄",
			entertainment: "今日娛樂", min: "分鐘", computer: "電腦",
			colName: "名稱", colType: "類型", colTime: "時間", colStatus: "狀態", colBar: "",
			typeApp: "應用程式", typeSite: "網站", statusAllowed: "允許", statusRestricted: "受限",
			logsTab: "日誌", statsTab: "統計", configTab: "設定", adminTab: "管理",
		}
	default:
		return statsUI{
			title: "Parental Control — Statistics", dateLabel: "Date", typeLabel: "Filter",
			filterAll: "All", filterApp: "Apps", filterSite: "Sites", filterRestricted: "Restricted",
			applyBtn: "Apply", entriesLabel: "entries",
			entertainment: "Entertainment today", min: "min", computer: "Computer",
			colName: "Name", colType: "Type", colTime: "Time", colStatus: "Status", colBar: "",
			typeApp: "App", typeSite: "Site", statusAllowed: "allowed", statusRestricted: "restricted",
			logsTab: "Logs", statsTab: "Statistics", configTab: "Configuration", adminTab: "Admin",
		}
	}
}

// writePauseBanner writes a pause status banner if the service is currently paused.
func writePauseBanner(w http.ResponseWriter, sp StatusProvider, lang string) {
	if sp == nil {
		return
	}
	st := sp.CurrentStatus()
	if st.ServiceMode == "" || st.ServiceMode == "normal" {
		return
	}

	mLabels := serviceModeLabels(lang)
	label := "⏸ " + mLabels.modes[st.ServiceMode]
	_, until := pauseBannerLabels(lang)

	// Цвет баннера зависит от режима.
	bgColor := "#f38ba8"
	switch st.ServiceMode {
	case "entertainment_paused", "self_entertainment_paused":
		bgColor = "#fab387"
	case "learning":
		bgColor = "#89b4fa"
	case "unrestricted":
		bgColor = "#fab387"
	}

	timeStr := ""
	if st.PauseUntil != "" {
		if t, err := time.Parse(time.RFC3339, st.PauseUntil); err == nil {
			timeStr = fmt.Sprintf(" — %s %s", until, t.Format("15:04"))
		}
	}

	fmt.Fprintf(w, `<div style="background:%s;color:#1e1e2e;border-radius:6px;padding:8px 16px;margin-bottom:12px;font-size:0.9rem;font-weight:600">%s%s</div>`,
		bgColor, htmlEscape(label), htmlEscape(timeStr))
}

// pauseBannerLabels возвращает локализованные метки для баннера паузы.
func pauseBannerLabels(lang string) (label, until string) {
	switch lang {
	case "ru":
		return "⏸ Сервис на паузе", "Возобновление"
	case "it":
		return "⏸ Servizio in pausa", "Riprende alle"
	case "es":
		return "⏸ Servicio en pausa", "Se reanuda a las"
	case "de":
		return "⏸ Dienst pausiert", "Fortsetzung um"
	case "pl":
		return "⏸ Usługa wstrzymana", "Wznowienie o"
	case "zh-TW":
		return "⏸ 服務已暫停", "恢復時間"
	default:
		return "⏸ Service paused", "Resumes at"
	}
}

// translateDayType возвращает локализованное название типа дня.
func translateDayType(dayType string, lang string) string {
	m := map[string]map[string]string{
		"workday": {"en": "📅 Workday", "ru": "📅 Рабочий день", "it": "📅 Giorno lavorativo", "es": "📅 Día laboral", "de": "📅 Werktag", "pl": "📅 Dzień roboczy", "zh-TW": "📅 工作日"},
		"weekend": {"en": "🎉 Weekend", "ru": "🎉 Выходной", "it": "🎉 Fine settimana", "es": "🎉 Fin de semana", "de": "🎉 Wochenende", "pl": "🎉 Weekend", "zh-TW": "🎉 週末"},
		"holiday": {"en": "🎄 Holiday", "ru": "🎄 Праздник", "it": "🎄 Festivo", "es": "🎄 Festivo", "de": "🎄 Feiertag", "pl": "🎄 Święto", "zh-TW": "🎄 假日"},
		"vacation": {"en": "🏖 Vacation", "ru": "🏖 Каникулы", "it": "🏖 Vacanza", "es": "🏖 Vacaciones", "de": "🏖 Ferien", "pl": "🏖 Wakacje", "zh-TW": "🏖 假期"},
		"pre_vacation": {"en": "🏖 Pre-vacation", "ru": "🏖 Предканикулярный", "it": "🏖 Pre-vacanza", "es": "🏖 Pre-vacaciones", "de": "🏖 Vorferien", "pl": "🏖 Przed wakacjami", "zh-TW": "🏖 假期前"},
	}
	if labels, ok := m[dayType]; ok {
		if label, ok := labels[lang]; ok {
			return label
		}
		return labels["en"]
	}
	return dayType
}

// dayTypeColor возвращает цвет бейджа для типа дня.
func dayTypeColor(dayType string) string {
	switch dayType {
	case "holiday":
		return "#f38ba8"
	case "weekend":
		return "#a6e3a1"
	case "vacation", "pre_vacation":
		return "#74c7ec"
	default:
		return "#89b4fa"
	}
}

// dayTypeBannerHTML генерирует HTML-баннер с типом дня и текущим режимом.
func dayTypeBannerHTML(sp StatusProvider, lang string) string {
	if sp == nil {
		return ""
	}
	st := sp.CurrentStatus()
	dayLabel := translateDayType(st.DayType, lang)
	dayColor := dayTypeColor(st.DayType)
	return fmt.Sprintf(`<div style="display:inline-block;background:%s;color:#1e1e2e;border-radius:4px;padding:4px 14px;font-size:0.85rem;font-weight:600;margin-bottom:8px">%s</div>`,
		dayColor, htmlEscape(dayLabel))
}

// dashboardInsideWindowText возвращает локализованный текст статуса "внутри окна".
func dashboardInsideWindowText(lang string, entertainment, limit, remaining int) string {
	switch lang {
	case "ru":
		return fmt.Sprintf("Развлечения: %d из %d мин. Осталось: %d мин.", entertainment, limit, remaining)
	case "it":
		return fmt.Sprintf("Intrattenimento: %d di %d min. Rimanenti: %d min.", entertainment, limit, remaining)
	case "es":
		return fmt.Sprintf("Entretenimiento: %d de %d min. Restante: %d min.", entertainment, limit, remaining)
	case "de":
		return fmt.Sprintf("Unterhaltung: %d von %d Min. Verbleibend: %d Min.", entertainment, limit, remaining)
	case "pl":
		return fmt.Sprintf("Rozrywka: %d z %d min. Pozostało: %d min.", entertainment, limit, remaining)
	case "zh-TW":
		return fmt.Sprintf("娛樂：%d / %d 分鐘。剩餘：%d 分鐘。", entertainment, limit, remaining)
	default:
		return fmt.Sprintf("Entertainment: %d of %d min. Remaining: %d min.", entertainment, limit, remaining)
	}
}

func selAttr(current, value string) string {
	if current == value || (current == "" && value == "all") {
		return ` selected`
	}
	return ""
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// supportedLangs — список поддерживаемых языков веб-интерфейса.
var supportedLangs = []struct {
	Code string
	Name string
}{
	{"en", "English"},
	{"ru", "Русский"},
	{"it", "Italiano"},
	{"es", "Español"},
	{"de", "Deutsch"},
	{"pl", "Polski"},
	{"zh-TW", "繁體中文"},
}

// normalizeLang проверяет и нормализует код языка.
func normalizeLang(lang string) string {
	for _, l := range supportedLangs {
		if l.Code == lang {
			return lang
		}
	}
	return "en"
}

// langSwitcherHTML генерирует HTML-переключатель языков.
func langSwitcherHTML(currentLang, basePath string, extraParams string) string {
	var sb strings.Builder
	sb.WriteString(`<div style="margin-bottom:16px;display:flex;gap:8px;justify-content:center;flex-wrap:wrap">`)
	for _, l := range supportedLangs {
		if l.Code == currentLang {
			sb.WriteString(fmt.Sprintf(`<span style="color:#f9e2af;font-size:0.85rem;padding:2px 8px;border:1px solid #45475a;border-radius:4px">%s</span>`, l.Name))
		} else {
			sep := "?"
			if strings.Contains(basePath, "?") {
				sep = "&"
			}
			href := basePath + sep + "lang=" + l.Code
			if extraParams != "" {
				href += "&" + extraParams
			}
			sb.WriteString(fmt.Sprintf(`<a href="%s" style="color:#89b4fa;text-decoration:none;font-size:0.85rem;padding:2px 8px">%s</a>`, href, l.Name))
		}
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

// topBarHTML генерирует верхнюю панель: бейдж типа дня слева + переключатель языков справа.
func topBarHTML(sp StatusProvider, currentLang, basePath, extraParams string) string {
	var sb strings.Builder
	sb.WriteString(`<div style="margin-bottom:16px;display:flex;align-items:center;flex-wrap:wrap;gap:8px">`)

	// Day type badge — слева.
	if sp != nil {
		st := sp.CurrentStatus()
		dayLabel := translateDayType(st.DayType, currentLang)
		if st.HolidayName != "" {
			dayLabel += ": " + st.HolidayName
		} else if st.VacationName != "" {
			dayLabel += ": " + st.VacationName
		}
		dayColor := dayTypeColor(st.DayType)
		sb.WriteString(fmt.Sprintf(`<span style="background:%s;color:#1e1e2e;border-radius:4px;padding:3px 12px;font-size:0.82rem;font-weight:600">%s</span>`,
			dayColor, htmlEscape(dayLabel)))
	}

	// Spacer.
	sb.WriteString(`<span style="flex:1"></span>`)

	// Language links — справа.
	for _, l := range supportedLangs {
		if l.Code == currentLang {
			sb.WriteString(fmt.Sprintf(`<span style="color:#f9e2af;font-size:0.82rem;padding:2px 6px;border:1px solid #45475a;border-radius:4px">%s</span>`, l.Name))
		} else {
			sep := "?"
			if strings.Contains(basePath, "?") {
				sep = "&"
			}
			href := basePath + sep + "lang=" + l.Code
			if extraParams != "" {
				href += "&" + extraParams
			}
			sb.WriteString(fmt.Sprintf(`<a href="%s" style="color:#89b4fa;text-decoration:none;font-size:0.82rem;padding:2px 6px">%s</a>`, href, l.Name))
		}
	}

	sb.WriteString(`</div>`)
	return sb.String()
}
