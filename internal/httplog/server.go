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

// PauseProvider abstracts pause/unpause operations.
type PauseProvider interface {
	Pause(password string, minutes int) (bool, string)
	Unpause(password string) (bool, string)
	IsPaused() bool
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
	mux.HandleFunc("/reload-config", h.handleReloadConfig)
	mux.HandleFunc("/change-password", h.handleChangePassword)
	mux.HandleFunc("/change-config-url", h.handleChangeConfigURL)
	mux.HandleFunc("/admin-html", h.handleAdminHTML)
	mux.HandleFunc("/browser-activity", h.handleBrowserActivity)

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
	if lang != "ru" {
		lang = "en"
	}

	ui := dashboardUILabels(lang)

	// Fetch live status.
	var statusMode, statusText string
	if h.statusProvider != nil {
		st := h.statusProvider.CurrentStatus()
		statusMode = st.Mode
		switch st.Mode {
		case "inside_window":
			if lang == "ru" {
				statusText = fmt.Sprintf("Развлечения: %d из %d мин. Осталось: %d мин.", st.EntertainmentMinutes, st.LimitMinutes, st.MinutesRemaining)
			} else {
				statusText = fmt.Sprintf("Entertainment: %d of %d min. Remaining: %d min.", st.EntertainmentMinutes, st.LimitMinutes, st.MinutesRemaining)
			}
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

	fmt.Fprintf(w, `<div class="header"><h1>%s</h1></div>`, ui.heading)

	// Status card.
	fmt.Fprintf(w, `<div class="status-card"><div class="mode">%s`, htmlEscape(statusText))
	fmt.Fprint(w, `</div></div>`)

	// Pause banner (same style as logs/stats pages).
	if h.statusProvider != nil {
		st := h.statusProvider.CurrentStatus()
		if st.Paused {
			var label, until string
			if lang == "ru" {
				label = "⏸ Сервис на паузе"
				until = "Возобновление"
			} else {
				label = "⏸ Service paused"
				until = "Resumes at"
			}
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

	// Language switch.
	otherLang := "en"
	otherLabel := "English"
	if lang == "en" {
		otherLang = "ru"
		otherLabel = "Русский"
	}
	fmt.Fprintf(w, `<div class="lang-switch"><a href="/?lang=%s">%s</a></div>`, otherLang, otherLabel)

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
	if lang == "ru" {
		return dashboardUI{
			title: "Родительский контроль", heading: "Родительский контроль",
			modeOutside: "Нет окна развлечений", modeSleep: "Время сна", modeUnknown: "Загрузка...", paused: "ПАУЗА",
			cardLogs: "Логи", cardLogsDesc: "Журнал событий и блокировок",
			cardStats: "Статистика", cardStatsDesc: "Время использования по приложениям и сайтам",
			cardConfig: "Конфигурация", cardConfigDesc: "Разрешённые программы, сайты и расписание",
			cardAdmin: "Управление", cardAdminDesc: "Пауза, смена пароля, URL конфигурации",
			cardAPI: "API / Статус", cardAPIDesc: "JSON-данные о текущем состоянии сервиса",
		}
	}
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
	if lang != "ru" {
		lang = "en"
	}

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
		WarningBeforeMinutes    int          `json:"warning_before_minutes"`
		SleepWarningBeforeMin   int          `json:"sleep_warning_before_minutes"`
		FullLogging             bool         `json:"full_logging"`
		EntertainmentApps       []string     `json:"entertainment_apps"`
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
		fmt.Fprintf(w, `<h2>%s (%d)</h2>`, ui.sitesTitle, len(sites.Sites))
		fmt.Fprintf(w, `<table><tr><th>%s</th><th>%s</th><th>%s</th></tr>`,
			ui.colDomain, ui.colSubdomains, ui.colPaths)
		for _, s := range sites.Sites {
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
			fmt.Fprintf(w, `<tr><td>%s</td><td class="%s">%s</td><td>%s</td></tr>`,
				htmlEscape(s.Domain), subClass, subText, paths)
		}
		fmt.Fprint(w, `</table>`)
	}

	// --- Schedule section ---
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
	}

	fmt.Fprint(w, `</body></html>`)
}

// configUI holds translated labels for the config HTML page.
type configUI struct {
	title, filterLabel                                     string
	filterAll, filterApps, filterSites, filterSchedule     string
	applyBtn                                               string
	appsTitle, sitesTitle, schedTitle                       string
	colName, colExe, colPath                               string
	colDomain, colSubdomains, colPaths                     string
	entWindows, sleepTimes                                 string
	colDays, colTimeRange, colLimit                        string
	min                                                    string
	warnBefore, sleepWarnBefore, fullLogging                string
	valYes, valNo                                          string
	entApps                                                string
	logsTab, statsTab, configTab, adminTab, reloadBtn      string
}

func configUILabels(lang string) configUI {
	if lang == "ru" {
		return configUI{
			title: "Родительский контроль — Конфигурация", filterLabel: "Раздел",
			filterAll: "Все", filterApps: "Программы", filterSites: "Сайты", filterSchedule: "Расписание",
			applyBtn:    "Применить",
			appsTitle:   "Разрешённые программы", sitesTitle: "Разрешённые сайты", schedTitle: "Расписание",
			colName: "Название", colExe: "Исполняемый файл", colPath: "Путь",
			colDomain: "Домен", colSubdomains: "Поддомены", colPaths: "Разрешённые пути",
			entWindows: "Окна развлечений", sleepTimes: "Время сна",
			colDays: "Дни", colTimeRange: "Время", colLimit: "Лимит",
			min: "мин",
			warnBefore: "Предупреждение до конца развлечений", sleepWarnBefore: "Предупреждение до сна",
			fullLogging: "Полное логирование", valYes: "да", valNo: "нет",
			entApps:   "Развлекательные приложения",
			logsTab: "Логи", statsTab: "Статистика", configTab: "Конфигурация", adminTab: "Управление", reloadBtn: "🔄 Обновить конфиг",
		}
	}
	return configUI{
		title: "Parental Control — Configuration", filterLabel: "Section",
		filterAll: "All", filterApps: "Apps", filterSites: "Sites", filterSchedule: "Schedule",
		applyBtn:    "Apply",
		appsTitle:   "Allowed Apps", sitesTitle: "Allowed Sites", schedTitle: "Schedule",
		colName: "Name", colExe: "Executable", colPath: "Path",
		colDomain: "Domain", colSubdomains: "Subdomains", colPaths: "Allowed Paths",
		entWindows: "Entertainment Windows", sleepTimes: "Sleep Times",
		colDays: "Days", colTimeRange: "Time", colLimit: "Limit",
		min: "min",
		warnBefore: "Warning before entertainment ends", sleepWarnBefore: "Warning before sleep",
		fullLogging: "Full logging", valYes: "yes", valNo: "no",
		entApps:   "Entertainment Apps",
		logsTab: "Logs", statsTab: "Statistics", configTab: "Configuration", adminTab: "Admin", reloadBtn: "🔄 Reload Config",
	}
}

// translateDays converts English day names to localized comma-separated string.
func translateDays(days []string, lang string) string {
	if lang != "ru" {
		result := ""
		for i, d := range days {
			if i > 0 {
				result += ", "
			}
			result += d
		}
		return result
	}
	dayMap := map[string]string{
		"monday": "Пн", "tuesday": "Вт", "wednesday": "Ср",
		"thursday": "Чт", "friday": "Пт", "saturday": "Сб", "sunday": "Вс",
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
	if lang != "ru" {
		lang = "en"
	}

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

	// Page switcher.
	today := time.Now().Format("2006-01-02")
	fmt.Fprintf(w, `<div class="page-switch"><a href="/logs-html?lang=%s">%s</a> <a href="/stats-html?date=%s&lang=%s">%s</a> <a href="/config-html?lang=%s">%s</a> <span>%s</span></div>`,
		lang, ui.logsTab, today, lang, ui.statsTab, lang, ui.configTab, ui.adminTab)

	// Pause banner.
	writePauseBanner(w, h.statusProvider, lang)

	// --- Pause section ---
	fmt.Fprintf(w, `<div class="section"><h2>%s</h2>`, ui.pauseTitle)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="password" id="pausePwd"></div>`, ui.password)
	fmt.Fprintf(w, `<div class="field"><label>%s</label><input type="number" id="pauseMins" value="30" min="1" max="480"></div>`, ui.minutes)
	fmt.Fprintf(w, `<button class="btn" onclick="doPause()">%s</button> `, ui.pauseBtn)
	fmt.Fprintf(w, `<button class="btn btn-danger" onclick="doUnpause()">%s</button>`, ui.unpauseBtn)
	fmt.Fprint(w, `<div class="msg" id="pauseMsg"></div></div>`)

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

	// JavaScript.
	fmt.Fprintf(w, `<script>
function showMsg(id,ok,text){var e=document.getElementById(id);e.textContent=text;e.className='msg '+(ok?'msg-ok':'msg-err')}
async function postAPI(url,body){
  try{var r=await fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});return await r.json()}
  catch(e){return{ok:false,message:'%s'}}
}
async function doPause(){
  var pwd=document.getElementById('pausePwd').value;
  var mins=parseInt(document.getElementById('pauseMins').value);
  var r=await postAPI('/pause',{password:pwd,minutes:mins});
  showMsg('pauseMsg',r.ok,r.message);if(r.ok)setTimeout(()=>location.reload(),1000);
}
async function doUnpause(){
  var pwd=document.getElementById('pausePwd').value;
  var r=await postAPI('/unpause',{password:pwd});
  showMsg('pauseMsg',r.ok,r.message);if(r.ok)setTimeout(()=>location.reload(),1000);
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
</script>`, ui.unavailable, ui.mismatch)

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
	if lang == "ru" {
		return adminUI{
			title: "Родительский контроль — Управление",
			logsTab: "Логи", statsTab: "Статистика", configTab: "Конфигурация", adminTab: "Управление",
			pauseTitle: "⏸ Пауза", password: "Пароль", minutes: "Минуты", pauseBtn: "Поставить паузу", unpauseBtn: "Снять паузу",
			pwdTitle: "🔑 Смена пароля", oldPwd: "Текущий пароль", newPwd: "Новый пароль", confirmPwd: "Подтвердите пароль", changePwdBtn: "Сменить пароль",
			urlTitle: "🔗 URL конфигурации", urlLabel: "URL", changeURLBtn: "Сохранить URL",
			unavailable: "Сервис недоступен", mismatch: "Пароли не совпадают",
		}
	}
	return adminUI{
		title: "Parental Control — Admin",
		logsTab: "Logs", statsTab: "Statistics", configTab: "Configuration", adminTab: "Admin",
		pauseTitle: "⏸ Pause", password: "Password", minutes: "Minutes", pauseBtn: "Pause", unpauseBtn: "Unpause",
		pwdTitle: "🔑 Change Password", oldPwd: "Current password", newPwd: "New password", confirmPwd: "Confirm password", changePwdBtn: "Change Password",
		urlTitle: "🔗 Config URL", urlLabel: "URL", changeURLBtn: "Save URL",
		unavailable: "Service unavailable", mismatch: "Passwords do not match",
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
	if lang != "ru" {
		lang = "en"
	}

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

	// Навигация по дням.
	today := time.Now()
	fmt.Fprint(w, `<div class="nav">`)
	for i := 6; i >= 0; i-- {
		d := today.AddDate(0, 0, -i).Format("2006-01-02")
		if d == date {
			fmt.Fprintf(w, `<span style="color:#f9e2af;font-weight:600;font-size:0.85rem">%s</span>`, d)
		} else {
			fmt.Fprintf(w, `<a href="?date=%s&type=%s&lang=%s">%s</a>`, d, typeFilter, lang, d)
		}
	}
	fmt.Fprint(w, `</div>`)

	// Page switcher: Logs / Stats / Config
	fmt.Fprintf(w, `<div class="page-switch"><span>%s</span> <a href="/stats-html?date=%s&lang=%s">%s</a> <a href="/config-html?lang=%s">%s</a> <a href="/admin-html?lang=%s">%s</a></div>`,
		ui.logsTab, date, lang, ui.statsTab, lang, ui.configTab, lang, ui.adminTab)

	// Pause banner.
	writePauseBanner(w, h.statusProvider, lang)

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

	fmt.Fprintf(w, `<table><tr><th>%s</th><th>%s</th><th>%s</th><th>%s</th><th>%s</th></tr>`,
		ui.colTime, ui.colType, ui.colMessage, ui.colProcess, ui.colURL)

	// Показываем от новых к старым.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		ts := e.Timestamp.Format("15:04:05")
		msg := e.Message
		if lang == "ru" {
			msg = translateLogMessage(msg)
		}
		fmt.Fprintf(w, `<tr class="%s"><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			e.EventType, ts, e.EventType, htmlEscape(msg), htmlEscape(e.ProcessName), htmlEscape(e.URL))
	}

	fmt.Fprint(w, `</table></body></html>`)
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
	if lang == "ru" {
		return logsUI{
			title: "Родительский контроль — Логи", dateLabel: "Дата", typeLabel: "Тип",
			filterAll: "Все", filterBlock: "Блокировка", filterWarning: "Предупреждение",
			filterInfo: "Инфо", filterStart: "Старт", filterStop: "Стоп",
			applyBtn: "Применить", autoRefresh: "Авто-обновление", entriesLabel: "записей",
			colTime: "Время", colType: "Тип", colMessage: "Сообщение", colProcess: "Процесс", colURL: "URL",
			logsTab: "Логи", statsTab: "Статистика", configTab: "Конфигурация", adminTab: "Управление",
		}
	}
	return logsUI{
		title: "Parental Control — Logs", dateLabel: "Date", typeLabel: "Type",
		filterAll: "All", filterBlock: "Block", filterWarning: "Warning",
		filterInfo: "Info", filterStart: "Start", filterStop: "Stop",
		applyBtn: "Apply", autoRefresh: "Auto-refresh", entriesLabel: "entries",
		colTime: "Time", colType: "Type", colMessage: "Message", colProcess: "Process", colURL: "URL",
		logsTab: "Logs", statsTab: "Statistics", configTab: "Configuration", adminTab: "Admin",
	}
}

// translateLogMessage translates English log messages to Russian.
func translateLogMessage(msg string) string {
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
	if lang != "ru" {
		lang = "en"
	}

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

	// Day navigation.
	today := time.Now()
	fmt.Fprint(w, `<div class="nav">`)
	for i := 6; i >= 0; i-- {
		d := today.AddDate(0, 0, -i).Format("2006-01-02")
		if d == date {
			fmt.Fprintf(w, `<span style="color:#f9e2af;font-weight:600;font-size:0.85rem">%s</span>`, d)
		} else {
			fmt.Fprintf(w, `<a href="?date=%s&type=%s&lang=%s">%s</a>`, d, typeFilter, lang, d)
		}
	}
	fmt.Fprint(w, `</div>`)

	// Page switcher: Logs / Stats / Config
	fmt.Fprintf(w, `<div class="page-switch"><a href="/logs-html?date=%s&lang=%s">%s</a> <span>%s</span> <a href="/config-html?lang=%s">%s</a> <a href="/admin-html?lang=%s">%s</a></div>`,
		date, lang, ui.logsTab, ui.statsTab, lang, ui.configTab, lang, ui.adminTab)

	// Pause banner.
	writePauseBanner(w, h.statusProvider, lang)

	// Summary.
	entMins := day.EntertainmentSeconds / 60
	fmt.Fprintf(w, `<div class="summary">%s: <span>%d %s</span></div>`,
		ui.entertainment, entMins, ui.min)

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

	fmt.Fprint(w, `</table></body></html>`)
}

// statsUI holds translated labels for the stats HTML page.
type statsUI struct {
	title, dateLabel, typeLabel                          string
	filterAll, filterApp, filterSite, filterRestricted   string
	applyBtn, entriesLabel                               string
	entertainment, min                                   string
	colName, colType, colTime, colStatus, colBar         string
	typeApp, typeSite, statusAllowed, statusRestricted   string
	logsTab, statsTab, configTab, adminTab               string
}

func statsUILabels(lang string) statsUI {
	if lang == "ru" {
		return statsUI{
			title: "Родительский контроль — Статистика", dateLabel: "Дата", typeLabel: "Фильтр",
			filterAll: "Все", filterApp: "Программы", filterSite: "Сайты", filterRestricted: "Развлечения",
			applyBtn: "Применить", entriesLabel: "записей",
			entertainment: "Развлечения за день", min: "мин",
			colName: "Название", colType: "Тип", colTime: "Время", colStatus: "Статус", colBar: "",
			typeApp: "Программа", typeSite: "Сайт", statusAllowed: "разрешено", statusRestricted: "развлечение",
			logsTab: "Логи", statsTab: "Статистика", configTab: "Конфигурация", adminTab: "Управление",
		}
	}
	return statsUI{
		title: "Parental Control — Statistics", dateLabel: "Date", typeLabel: "Filter",
		filterAll: "All", filterApp: "Apps", filterSite: "Sites", filterRestricted: "Restricted",
		applyBtn: "Apply", entriesLabel: "entries",
		entertainment: "Entertainment today", min: "min",
		colName: "Name", colType: "Type", colTime: "Time", colStatus: "Status", colBar: "",
		typeApp: "App", typeSite: "Site", statusAllowed: "allowed", statusRestricted: "restricted",
		logsTab: "Logs", statsTab: "Statistics", configTab: "Configuration", adminTab: "Admin",
	}
}

// writePauseBanner writes a pause status banner if the service is currently paused.
func writePauseBanner(w http.ResponseWriter, sp StatusProvider, lang string) {
	if sp == nil {
		return
	}
	st := sp.CurrentStatus()
	if !st.Paused {
		return
	}

	var label, until string
	if lang == "ru" {
		label = "⏸ Сервис на паузе"
		until = "Возобновление"
	} else {
		label = "⏸ Service paused"
		until = "Resumes at"
	}

	timeStr := ""
	if st.PauseUntil != "" {
		if t, err := time.Parse(time.RFC3339, st.PauseUntil); err == nil {
			timeStr = fmt.Sprintf(" — %s %s", until, t.Format("15:04"))
		}
	}

	fmt.Fprintf(w, `<div style="background:#f38ba8;color:#1e1e2e;border-radius:6px;padding:8px 16px;margin-bottom:12px;font-size:0.9rem;font-weight:600">%s%s</div>`,
		htmlEscape(label), htmlEscape(timeStr))
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
