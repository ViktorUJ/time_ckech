// Package service orchestrates all parental control components in the main loop.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"parental-control-service/internal/browser"
	"parental-control-service/internal/config"
	"parental-control-service/internal/enforcer"
	"parental-control-service/internal/httplog"
	"parental-control-service/internal/logger"
	"parental-control-service/internal/monitor"
	"parental-control-service/internal/scheduler"
	"parental-control-service/internal/sleepmode"
	"parental-control-service/internal/state"
	"parental-control-service/internal/stats"

	"golang.org/x/crypto/bcrypt"
)

const (
	tickInterval         = 15 * time.Second
	stateSaveInterval    = 60 * time.Second
	configUpdateInterval = 5 * time.Minute
)

// Service is the main orchestrator that ties all components together.
type Service struct {
	configManager  *config.ConfigManager
	processMonitor *monitor.ProcessMonitor
	browserMonitor *browser.BrowserMonitor
	scheduler      *scheduler.Scheduler
	enforcer       *enforcer.Enforcer
	sleepManager   *sleepmode.SleepModeManager
	stateManager   *state.StateManager
	logger         *logger.Logger
	httpServer     *httplog.HTTPLogServer
	notifier       enforcer.Notifier
	statsTracker   *stats.Tracker

	entertainmentSeconds int
	currentWindowStart   string
	currentWindowEnd     string
	lastStateSave        time.Time
	lastTick             time.Time
	httpServerRunning    bool

	// Данные о браузерных URL, полученные от tray-приложения.
	browserURLsMu sync.Mutex
	browserURLs   []httplog.BrowserURLEntry

	// One-shot notification flags (reset on window/mode change).
	notifiedLimitReached bool
	notifiedSleepStart   bool

	// Пауза: временная приостановка всех ограничений.
	pauseMu       sync.Mutex
	pauseUntil    time.Time // нулевое значение = пауза не активна
	passwordHash  string    // bcrypt hash из settings.json
	dataDir       string    // путь к директории данных (для сохранения settings.json)
}

// NewService creates a new Service with all component dependencies.
func NewService(
	configMgr *config.ConfigManager,
	procMon *monitor.ProcessMonitor,
	browserMon *browser.BrowserMonitor,
	sched *scheduler.Scheduler,
	enf *enforcer.Enforcer,
	sleepMgr *sleepmode.SleepModeManager,
	stateMgr *state.StateManager,
	lgr *logger.Logger,
	httpSrv *httplog.HTTPLogServer,
	notifier enforcer.Notifier,
	statsTracker *stats.Tracker,
	passwordHash string,
	dataDir string,
) *Service {
	return &Service{
		configManager:  configMgr,
		processMonitor: procMon,
		browserMonitor: browserMon,
		scheduler:      sched,
		enforcer:       enf,
		sleepManager:   sleepMgr,
		stateManager:   stateMgr,
		logger:         lgr,
		httpServer:     httpSrv,
		notifier:       notifier,
		statsTracker:   statsTracker,
		passwordHash:   passwordHash,
		dataDir:        dataDir,
	}
}

// Run starts the main service loop. It restores state, starts background
// goroutines, and runs the main tick loop until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	// 1. Restore state from disk.
	now := time.Now()
	restored := s.stateManager.Restore(now)
	s.entertainmentSeconds = restored.EntertainmentSeconds
	s.lastTick = now
	s.lastStateSave = now

	// Restore window boundaries from saved state.
	if !restored.WindowStart.IsZero() && !restored.WindowEnd.IsZero() {
		s.currentWindowStart = restored.WindowStart.Format("15:04")
		s.currentWindowEnd = restored.WindowEnd.Format("15:04")
	}

	// Log service start.
	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: now,
		EventType: logger.EventServiceStart,
		Message:   "ParentalControlService started",
	})

	// 2. Always start HTTP server so the tray app can connect regardless of config state.
	if s.httpServer != nil && !s.httpServerRunning {
		if err := s.httpServer.Start(ctx); err != nil {
			log.Printf("[service] failed to start HTTP server: %v", err)
		} else {
			s.httpServerRunning = true
		}
	}

	// 3. Start initial config load (retry until success or context cancelled).
	s.initialConfigLoad(ctx)

	// Log service start (after config load so full_logging is enabled).
	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventServiceStart,
		Message:   "ParentalControlService started",
	})

	// 4. Start background config update goroutine.
	go s.configUpdateLoop(ctx)

	// 4. Main tick loop.
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.Stop(ctx)
			return nil
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// Stop performs graceful shutdown: saves state, stops HTTP server, logs stop event.
func (s *Service) Stop(ctx context.Context) {
	// Save final state.
	now := time.Now()
	s.saveState(now)

	// Stop HTTP server if running.
	if s.httpServerRunning && s.httpServer != nil {
		if err := s.httpServer.Stop(ctx); err != nil {
			log.Printf("[service] error stopping HTTP server: %v", err)
		}
		s.httpServerRunning = false
	}

	// Log service stop.
	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: now,
		EventType: logger.EventServiceStop,
		Message:   "ParentalControlService stopped",
	})
}

// tick executes one iteration of the main service loop.
func (s *Service) tick(ctx context.Context) {
	now := time.Now()

	// Проверяем паузу: если активна — пропускаем все ограничения.
	if s.IsPaused() {
		s.lastTick = now
		s.maybeSaveState(now)
		return
	}

	elapsed := now.Sub(s.lastTick)
	s.lastTick = now

	cfg := s.configManager.Current()
	if cfg == nil {
		// No config available — fail-closed: block everything except system processes.
		s.failClosedTick(ctx, now)
		return
	}

	// a. Check schedule → get current mode.
	schedState := s.scheduler.CurrentState(now)

	// Reset sleep notification flag when NOT in sleep mode.
	if schedState.Mode != scheduler.ModeSleepTime {
		s.notifiedSleepStart = false
	}

	// b. If sleep mode → enforce sleep (kill all user processes).
	if schedState.Mode == scheduler.ModeSleepTime {
		s.handleSleepMode(ctx, now)
		s.maybeSaveState(now)
		return
	}

	// c. Scan processes → classify.
	processes, err := s.processMonitor.Scan(ctx)
	if err != nil {
		log.Printf("[service] process scan error: %v", err)
		s.maybeSaveState(now)
		return
	}

	// d. Classify browser URLs received from tray app.
	browserActivities := s.classifyBrowserURLs(cfg)

	// Detect restricted activity.
	hasRestrictedProcess := hasRestricted(processes)
	hasRestrictedSite := hasRestrictedBrowser(browserActivities)
	hasRestrictedActivity := hasRestrictedProcess || hasRestrictedSite

	// e. Check window transition → reset counter if new window.
	if schedState.CurrentWindow != nil {
		winStart := schedState.CurrentWindow.Start
		winEnd := schedState.CurrentWindow.End
		if winStart != s.currentWindowStart || winEnd != s.currentWindowEnd {
			// New window — reset counter and notification flag.
			s.entertainmentSeconds = 0
			s.notifiedLimitReached = false
			s.currentWindowStart = winStart
			s.currentWindowEnd = winEnd
		}
	} else {
		// Outside any window — clear window tracking.
		if s.currentWindowStart != "" || s.currentWindowEnd != "" {
			s.currentWindowStart = ""
			s.currentWindowEnd = ""
			s.entertainmentSeconds = 0
		}
	}

	// f. Update entertainment counter (wall clock, only if restricted activity detected).
	if hasRestrictedActivity {
		s.entertainmentSeconds += int(elapsed.Seconds())

		// Log which restricted processes/sites are consuming entertainment time.
		s.logRestrictedActivity(now, processes, browserActivities)
	}

	// f2. Record usage stats for all detected processes and sites.
	// Дедупликация: считаем каждое имя процесса/домен только один раз за тик,
	// даже если запущено несколько процессов с одинаковым именем.
	if s.statsTracker != nil {
		elapsedSec := int(elapsed.Seconds())
		seenApps := make(map[string]bool)
		for _, p := range processes {
			if p.IsSystem {
				continue
			}
			if seenApps[p.Name] {
				continue
			}
			seenApps[p.Name] = true
			s.statsTracker.RecordApp(p.Name, !p.IsAllowed, elapsedSec)
		}
		seenSites := make(map[string]bool)
		for _, a := range browserActivities {
			if seenSites[a.Domain] {
				continue
			}
			seenSites[a.Domain] = true
			s.statsTracker.RecordSite(a.Domain, a.Browser, !a.IsAllowed, elapsedSec)
		}
	}

	// g. Apply rules based on current mode.
	limitMinutes := schedState.LimitMinutes
	shouldBlock := enforcer.ShouldBlock(schedState.Mode, s.entertainmentSeconds, limitMinutes)

	if shouldBlock {
		// Notify once when entertainment limit is reached.
		if schedState.Mode == scheduler.ModeInsideWindow && !s.notifiedLimitReached {
			msg := "Entertainment time is over. Restricted apps will be closed."
			_ = s.notifier.ShowNotification("Parental Control", msg)
			_ = s.logger.LogEvent(logger.LogEntry{
				Timestamp: now,
				EventType: logger.EventWarning,
				Message:   msg,
			})
			s.notifiedLimitReached = true
		}
		s.blockRestricted(ctx, processes, browserActivities, schedState.Mode, limitMinutes)
	}

	// h. Check warnings.
	s.checkWarnings(now, schedState)

	// i. Save state periodically.
	s.maybeSaveState(now)
}

// initialConfigLoad tries to load config from GitHub. If it fails and no
// cached config is available, it retries every 30 seconds until success or
// context cancellation. The service stays in fail-closed mode while waiting.
func (s *Service) initialConfigLoad(ctx context.Context) {
	const retryInterval = 30 * time.Second

	if _, err := s.configManager.Load(ctx); err == nil {
		s.applyConfigChanges(ctx)
		return
	} else {
		log.Printf("[service] initial config load failed: %v", err)
	}

	// If we already have a cached config (from disk), no need to block startup.
	if s.configManager.Current() != nil {
		log.Printf("[service] using cached config from disk, will retry GitHub in background")
		s.applyConfigChanges(ctx)
		return
	}

	// No config at all — retry until we get one.
	log.Printf("[service] no config available, retrying every %v...", retryInterval)
	retryTicker := time.NewTicker(retryInterval)
	defer retryTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-retryTicker.C:
			if _, err := s.configManager.Load(ctx); err != nil {
				log.Printf("[service] config retry failed: %v", err)
				continue
			}
			log.Printf("[service] config loaded successfully after retry")
			s.applyConfigChanges(ctx)
			return
		}
	}
}

// configUpdateLoop runs in a background goroutine and reloads config
// periodically. Uses a shorter interval (30s) when no config is loaded,
// and the normal interval (5min) otherwise.
func (s *Service) configUpdateLoop(ctx context.Context) {
	const retryInterval = 30 * time.Second

	timer := time.NewTimer(s.nextConfigInterval())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			oldCfg := s.configManager.Current()
			newCfg, err := s.configManager.Load(ctx)
			if err != nil {
				if errors.Is(err, config.ErrUsedCache) {
					_ = s.logger.LogEvent(logger.LogEntry{
						Timestamp: time.Now(),
						EventType: logger.EventWarning,
						Message:   "Config update failed (network error). Using cached config.",
					})
				} else {
					log.Printf("[service] config update failed: %v", err)
				}
			} else {
				s.applyConfigChanges(ctx)
				changes := describeConfigChanges(oldCfg, newCfg)
				_ = s.logger.LogEvent(logger.LogEntry{
					Timestamp: time.Now(),
					EventType: logger.EventInfo,
					Message:   "Config updated. " + changes,
				})
			}
			timer.Reset(s.nextConfigInterval())
		}
	}
}

// nextConfigInterval returns a shorter interval when config is missing,
// and the normal interval when config is loaded.
func (s *Service) nextConfigInterval() time.Duration {
	const retryInterval = 30 * time.Second
	if s.configManager.Current() == nil {
		return retryInterval
	}
	return configUpdateInterval
}

// applyConfigChanges handles changes to full_logging and schedule
// when configuration is updated.
func (s *Service) applyConfigChanges(ctx context.Context) {
	cfg := s.configManager.Current()
	if cfg == nil {
		return
	}

	// Update full_logging flag on the logger.
	s.logger.SetFullLogging(cfg.Schedule.FullLogging)

	// Update scheduler with new schedule.
	s.scheduler.UpdateSchedule(cfg.Schedule)

	// Update process classifier with new allowed apps list.
	classifier := monitor.NewDefaultClassifier(cfg.AllowedApps.Apps, nil, cfg.Schedule.EntertainmentApps)
	s.processMonitor.SetClassifier(classifier)
}

// failClosedTick handles the case when no config is available.
// In fail-closed mode, all non-system user processes are terminated.
func (s *Service) failClosedTick(ctx context.Context, now time.Time) {
	processes, err := s.processMonitor.Scan(ctx)
	if err != nil {
		log.Printf("[service] process scan error in fail-closed mode: %v", err)
		return
	}

	for _, p := range processes {
		if p.IsSystem || p.IsAllowed {
			continue
		}
		_ = s.enforcer.BlockWithWarning(ctx, p.PID,
			"Service config unavailable. Apps are temporarily blocked.")
	}
	s.maybeSaveState(now)
}

// handleSleepMode enforces sleep mode by killing all user processes.
func (s *Service) handleSleepMode(ctx context.Context, now time.Time) {
	// Notify once when sleep mode starts.
	if !s.notifiedSleepStart {
		msg := "Sleep time started. All apps will be closed."
		_ = s.notifier.ShowNotification("Parental Control", msg)
		_ = s.logger.LogEvent(logger.LogEntry{
			Timestamp: now,
			EventType: logger.EventWarning,
			Message:   msg,
		})
		s.notifiedSleepStart = true
	}

	processes, err := s.processMonitor.Scan(ctx)
	if err != nil {
		log.Printf("[service] process scan error in sleep mode: %v", err)
		return
	}

	if err := s.sleepManager.Enforce(ctx, processes); err != nil {
		log.Printf("[service] sleep mode enforcement error: %v", err)
	}

	// Restricted сайты в браузерах закрываются через tray (WM_CLOSE).
	// Здесь только логируем.
	cfg := s.configManager.Current()
	if cfg != nil {
		browserActivities := s.classifyBrowserURLs(cfg)
		for _, a := range browserActivities {
			if a.IsAllowed {
				continue
			}
			_ = s.logger.LogEvent(logger.LogEntry{
				Timestamp: now,
				EventType: logger.EventBlock,
				URL:       a.URL,
				Browser:   a.Browser,
				Message:   fmt.Sprintf("Sleep time: blocked %s (%s)", a.Domain, a.Browser),
			})
		}
	}
}

// blockRestricted terminates restricted processes and redirects restricted browser tabs.
func (s *Service) blockRestricted(
	ctx context.Context,
	processes []monitor.ProcessInfo,
	browserActivities []browser.BrowserActivity,
	mode scheduler.Mode,
	limitMinutes int,
) {
	msg := blockMessage(mode, limitMinutes)

	// Block restricted processes.
	for _, p := range processes {
		if p.IsSystem || p.IsAllowed {
			continue
		}
		if err := s.enforcer.BlockWithWarning(ctx, p.PID, msg); err != nil {
			log.Printf("[service] failed to block process %s (pid %d): %v", p.Name, p.PID, err)
		}
		_ = s.logger.LogEvent(logger.LogEntry{
			Timestamp:   time.Now(),
			EventType:   logger.EventBlock,
			ProcessName: p.Name,
			ExePath:     p.ExePath,
			Message:     msg,
		})
	}

	// Restricted сайты в браузерах закрываются через tray (WM_CLOSE на окно).
	// Логируем блокировку.
	for _, activity := range browserActivities {
		if activity.IsAllowed {
			continue
		}
		_ = s.logger.LogEvent(logger.LogEntry{
			Timestamp: time.Now(),
			EventType: logger.EventBlock,
			URL:       activity.URL,
			Browser:   activity.Browser,
			Message:   fmt.Sprintf("Blocked site: %s (%s)", activity.Domain, activity.Browser),
		})
	}
}

// logRestrictedActivity logs the names of restricted processes and sites
// that are currently consuming entertainment time. Uses full_logging level
// so it only appears in the detailed log file.
func (s *Service) logRestrictedActivity(now time.Time, processes []monitor.ProcessInfo, activities []browser.BrowserActivity) {
	var names []string
	for _, p := range processes {
		if !p.IsSystem && !p.IsAllowed {
			names = append(names, p.Name)
		}
	}
	for _, a := range activities {
		if !a.IsAllowed {
			names = append(names, fmt.Sprintf("%s(%s)", a.Domain, a.Browser))
		}
	}
	if len(names) == 0 {
		return
	}

	spent := s.entertainmentSeconds / 60
	msg := fmt.Sprintf("Entertainment %d min. Active: %s", spent, joinUnique(names))
	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: now,
		EventType: logger.EventInfo,
		Message:   msg,
	})
}

// joinUnique returns a comma-separated string of unique values.
func joinUnique(items []string) string {
	seen := make(map[string]bool, len(items))
	var result []string
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	out := ""
	for i, r := range result {
		if i > 0 {
			out += ", "
		}
		out += r
	}
	return out
}

// checkWarnings checks and emits warnings for entertainment limit and sleep time.
func (s *Service) checkWarnings(now time.Time, schedState scheduler.ScheduleState) {
	// Warn about entertainment limit approaching.
	if schedState.Mode == scheduler.ModeInsideWindow && schedState.LimitMinutes > 0 {
		if s.scheduler.ShouldWarnEntertainment(s.entertainmentSeconds, schedState.LimitMinutes) {
			remaining := schedState.LimitMinutes - s.entertainmentSeconds/60
			msg := fmt.Sprintf("Entertainment time ends in %d min.", remaining)
			_ = s.notifier.ShowNotification("Parental Control", msg)
			_ = s.logger.LogEvent(logger.LogEntry{
				Timestamp: now,
				EventType: logger.EventWarning,
				Message:   msg,
			})
		}
	}

	// Warn about sleep time approaching.
	if shouldWarn, minutesLeft := s.scheduler.ShouldWarnSleep(now); shouldWarn {
		_ = s.sleepManager.WarnUpcoming(minutesLeft)
		_ = s.logger.LogEvent(logger.LogEntry{
			Timestamp: now,
			EventType: logger.EventWarning,
			Message:   fmt.Sprintf("Sleep time starts in %d min.", minutesLeft),
		})
	}
}

// saveState persists the current service state to disk.
func (s *Service) saveState(now time.Time) {
	st := &state.ServiceState{
		EntertainmentSeconds: s.entertainmentSeconds,
		LastTickTime:         now,
	}

	if s.currentWindowStart != "" && s.currentWindowEnd != "" {
		st.WindowStart = todayTime(now, s.currentWindowStart)
		st.WindowEnd = todayTime(now, s.currentWindowEnd)
	}

	if err := s.stateManager.Save(st); err != nil {
		log.Printf("[service] failed to save state: %v", err)
	}

	// Flush stats to disk.
	if s.statsTracker != nil {
		s.statsTracker.Flush()
	}

	s.lastStateSave = now
}

// maybeSaveState saves state if enough time has passed since the last save.
func (s *Service) maybeSaveState(now time.Time) {
	if now.Sub(s.lastStateSave) >= stateSaveInterval {
		s.saveState(now)
	}
}

// --- Helpers ---

// CurrentStatus implements httplog.StatusProvider. It returns the current
// service status for the /status HTTP endpoint used by the tray application.
func (s *Service) CurrentStatus() httplog.StatusResponse {
	now := time.Now()
	schedState := s.scheduler.CurrentState(now)

	var modeName string
	switch schedState.Mode {
	case scheduler.ModeInsideWindow:
		modeName = "inside_window"
	case scheduler.ModeOutsideWindow:
		modeName = "outside_window"
	case scheduler.ModeSleepTime:
		modeName = "sleep_time"
	default:
		modeName = "unknown"
	}

	resp := httplog.StatusResponse{
		Mode:                 modeName,
		EntertainmentMinutes: s.entertainmentSeconds / 60,
		LimitMinutes:         schedState.LimitMinutes,
		ActiveProcesses:      []string{},
	}

	if schedState.LimitMinutes > 0 {
		remaining := schedState.LimitMinutes - s.entertainmentSeconds/60
		if remaining < 0 {
			remaining = 0
		}
		resp.MinutesRemaining = remaining
	}

	if schedState.CurrentWindow != nil {
		w := fmt.Sprintf("%s-%s", schedState.CurrentWindow.Start, schedState.CurrentWindow.End)
		resp.ActiveWindow = &w
	}

	// Информация о паузе.
	s.pauseMu.Lock()
	if !s.pauseUntil.IsZero() && time.Now().Before(s.pauseUntil) {
		resp.Paused = true
		resp.PauseUntil = s.pauseUntil.Format(time.RFC3339)
	}
	s.pauseMu.Unlock()

	return resp
}

// IsPaused возвращает true, если пауза активна.
func (s *Service) IsPaused() bool {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()
	if s.pauseUntil.IsZero() {
		return false
	}
	if time.Now().After(s.pauseUntil) {
		s.pauseUntil = time.Time{} // пауза истекла
		return false
	}
	return true
}

// Pause ставит паузу на minutes минут, если пароль верный.
func (s *Service) Pause(password string, minutes int) (bool, string) {
	if s.passwordHash == "" {
		return false, "Password not configured"
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.passwordHash), []byte(password)); err != nil {
		return false, "Wrong password"
	}
	if minutes < 1 || minutes > 480 {
		return false, "Allowed range: 1 to 480 minutes"
	}

	s.pauseMu.Lock()
	s.pauseUntil = time.Now().Add(time.Duration(minutes) * time.Minute)
	s.pauseMu.Unlock()

	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventInfo,
		Message:   fmt.Sprintf("Pause set for %d min.", minutes),
	})
	return true, fmt.Sprintf("Paused for %d min.", minutes)
}

// Unpause снимает паузу, если пароль верный.
func (s *Service) Unpause(password string) (bool, string) {
	if s.passwordHash == "" {
		return false, "Password not configured"
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.passwordHash), []byte(password)); err != nil {
		return false, "Wrong password"
	}

	s.pauseMu.Lock()
	s.pauseUntil = time.Time{}
	s.pauseMu.Unlock()

	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventInfo,
		Message:   "Pause removed",
	})
	return true, "Pause removed"
}

// ReloadConfig implements httplog.ConfigReloader. Forces immediate config reload.
func (s *Service) ReloadConfig(ctx context.Context) (string, error) {
	oldCfg := s.configManager.Current()
	newCfg, err := s.configManager.Load(ctx)
	if err != nil {
		if errors.Is(err, config.ErrUsedCache) {
			// Сеть недоступна, используем кешированный конфиг.
			_ = s.logger.LogEvent(logger.LogEntry{
				Timestamp: time.Now(),
				EventType: logger.EventWarning,
				Message:   "Config reload failed (network error). Using cached config.",
			})
			return "", fmt.Errorf("network error, using cached config")
		}
		return "", fmt.Errorf("config reload failed: %w", err)
	}
	s.applyConfigChanges(ctx)
	changes := describeConfigChanges(oldCfg, newCfg)
	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventInfo,
		Message:   "Config reloaded (manual). " + changes,
	})
	return changes, nil
}

// ChangePassword implements httplog.PasswordChanger. Changes the pause password.
func (s *Service) ChangePassword(oldPassword, newPassword string) (bool, string) {
	if s.passwordHash == "" {
		return false, "Password not configured"
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.passwordHash), []byte(oldPassword)); err != nil {
		return false, "Wrong password"
	}
	if len(newPassword) < 1 {
		return false, "New password cannot be empty"
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return false, "Failed to hash password"
	}

	s.passwordHash = string(hash)

	// Сохраняем новый хеш в settings.json.
	settings, err := config.LoadSettings(s.dataDir)
	if err != nil {
		log.Printf("[service] failed to load settings for password change: %v", err)
		return false, "Failed to save password"
	}
	settings.PasswordHash = string(hash)
	if err := config.SaveSettings(s.dataDir, settings); err != nil {
		log.Printf("[service] failed to save settings after password change: %v", err)
		return false, "Failed to save password"
	}

	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventInfo,
		Message:   "Password changed",
	})
	return true, "Password changed"
}

// ChangeConfigURL implements httplog.ConfigURLChanger. Changes the config URL.
func (s *Service) ChangeConfigURL(password, newURL string) (bool, string) {
	if s.passwordHash == "" {
		return false, "Password not configured"
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.passwordHash), []byte(password)); err != nil {
		return false, "Wrong password"
	}
	if newURL == "" {
		return false, "URL cannot be empty"
	}

	settings, err := config.LoadSettings(s.dataDir)
	if err != nil {
		return false, "Failed to load settings"
	}
	oldURL := settings.ConfigURL
	settings.ConfigURL = newURL
	if err := config.SaveSettings(s.dataDir, settings); err != nil {
		return false, "Failed to save settings"
	}

	// Update config manager with new URL.
	s.configManager.SetConfigURL(newURL)

	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventInfo,
		Message:   fmt.Sprintf("Config URL changed: %s → %s", oldURL, newURL),
	})
	return true, "Config URL changed"
}

// GetConfigURL implements httplog.ConfigURLChanger.
func (s *Service) GetConfigURL() string {
	settings, err := config.LoadSettings(s.dataDir)
	if err != nil {
		return ""
	}
	return settings.ConfigURL
}

// CurrentConfigJSON implements httplog.ConfigProvider. Returns the current
// configuration as raw JSON for the /config HTTP endpoint.
func (s *Service) CurrentConfigJSON() httplog.ConfigResponse {
	cfg := s.configManager.Current()
	if cfg == nil {
		return httplog.ConfigResponse{}
	}

	appsJSON, _ := json.MarshalIndent(cfg.AllowedApps, "", "  ")
	sitesJSON, _ := json.MarshalIndent(cfg.AllowedSites, "", "  ")
	schedJSON, _ := json.MarshalIndent(cfg.Schedule, "", "  ")

	return httplog.ConfigResponse{
		Apps:     appsJSON,
		Sites:    sitesJSON,
		Schedule: schedJSON,
	}
}

// BrowserBlockResult содержит список HWND для закрытия и причину блокировки.
type BrowserBlockResult struct {
	CloseHWNDs []uintptr
	Reason     string
}

// ReceiveBrowserURLs implements httplog.BrowserActivityReceiver.
// Вызывается HTTP-сервером при получении POST /browser-activity от tray-приложения.
// Сохраняет URL для обработки в tick() и сразу возвращает список HWND окон
// с restricted сайтами, которые tray должен закрыть.
func (s *Service) ReceiveBrowserURLs(urls []httplog.BrowserURLEntry) ([]uintptr, string) {
	s.browserURLsMu.Lock()
	s.browserURLs = urls
	s.browserURLsMu.Unlock()

	// Определяем, нужно ли блокировать restricted сайты прямо сейчас.
	// Если пауза активна — не блокируем ничего.
	if s.IsPaused() {
		return nil, ""
	}

	cfg := s.configManager.Current()
	if cfg == nil {
		return nil, ""
	}

	now := time.Now()
	schedState := s.scheduler.CurrentState(now)

	shouldBlock := enforcer.ShouldBlock(schedState.Mode, s.entertainmentSeconds, schedState.LimitMinutes)
	if !shouldBlock {
		return nil, ""
	}

	// Определяем причину блокировки.
	var reason string
	switch schedState.Mode {
	case scheduler.ModeSleepTime:
		reason = "Sleep time. Entertainment sites are blocked."
	case scheduler.ModeOutsideWindow:
		reason = "No entertainment window now. Site is blocked."
	case scheduler.ModeInsideWindow:
		reason = "Entertainment time is over for today. Site is blocked."
	default:
		reason = "Access to this site is currently blocked."
	}

	var closeHWNDs []uintptr
	for _, u := range urls {
		allowed := browser.IsURLAllowed(u.URL, cfg.AllowedSites.Sites)
		if !allowed && u.HWND != 0 {
			closeHWNDs = append(closeHWNDs, u.HWND)
		}
	}
	return closeHWNDs, reason
}

// getBrowserURLs возвращает последние полученные URL от tray и очищает буфер.
func (s *Service) getBrowserURLs() []httplog.BrowserURLEntry {
	s.browserURLsMu.Lock()
	urls := s.browserURLs
	s.browserURLs = nil
	s.browserURLsMu.Unlock()
	return urls
}

// classifyBrowserURLs классифицирует URL, полученные от tray-приложения,
// используя текущий список разрешённых сайтов.
func (s *Service) classifyBrowserURLs(cfg *config.Config) []browser.BrowserActivity {
	urls := s.getBrowserURLs()
	if len(urls) == 0 {
		return nil
	}

	var activities []browser.BrowserActivity
	for _, u := range urls {
		domain := browser.ExtractDomain(u.URL)
		allowed := browser.IsURLAllowed(u.URL, cfg.AllowedSites.Sites)
		activities = append(activities, browser.BrowserActivity{
			Browser:   u.Browser,
			PID:       u.PID,
			URL:       u.URL,
			Domain:    domain,
			IsAllowed: allowed,
			TabID:     fmt.Sprintf("%s-%d", u.Browser, u.PID),
		})
	}
	return activities
}

// blockMessage returns the user-facing notification message based on the current mode.
func blockMessage(mode scheduler.Mode, limitMinutes int) string {
	switch mode {
	case scheduler.ModeSleepTime:
		return "Sleep time. Computer is unavailable"
	case scheduler.ModeOutsideWindow:
		return "This app is not allowed right now"
	case scheduler.ModeInsideWindow:
		return "Entertainment time is over for today"
	default:
		return "This app is not allowed right now"
	}
}

// extractBrowserProcesses identifies browser processes from the scanned process list.
var knownBrowsers = map[string]string{
	"chrome.exe":  "chrome",
	"msedge.exe":  "edge",
	"firefox.exe": "firefox",
}

func extractBrowserProcesses(processes []monitor.ProcessInfo) []browser.BrowserProcess {
	var result []browser.BrowserProcess
	for _, p := range processes {
		if browserName, ok := knownBrowsers[p.Name]; ok {
			result = append(result, browser.BrowserProcess{
				Browser: browserName,
				PID:     p.PID,
			})
		}
	}
	return result
}

// hasRestricted returns true if any process is restricted (not system, not allowed).
func hasRestricted(processes []monitor.ProcessInfo) bool {
	for _, p := range processes {
		if !p.IsSystem && !p.IsAllowed {
			return true
		}
	}
	return false
}

// hasRestrictedBrowser returns true if any browser activity is on a restricted site.
func hasRestrictedBrowser(activities []browser.BrowserActivity) bool {
	for _, a := range activities {
		if !a.IsAllowed {
			return true
		}
	}
	return false
}

// GetDayStats implements httplog.StatsProvider.
func (s *Service) GetDayStats(date string) interface{} {
	if s.statsTracker == nil {
		return stats.DayStats{Date: date}
	}
	return s.statsTracker.GetDayStats(date)
}

// GetWeekStats implements httplog.StatsProvider.
func (s *Service) GetWeekStats() interface{} {
	if s.statsTracker == nil {
		return stats.WeekStats{}
	}
	return s.statsTracker.GetWeekStats()
}

// todayTime builds a time.Time for today at the given "HH:MM" time string.
func todayTime(now time.Time, hhmm string) time.Time {
	var h, m int
	fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	return time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
}

// describeConfigChanges returns a human-readable summary of what changed
// between old and new configs.
func describeConfigChanges(old, new *config.Config) string {
	if old == nil {
		return fmt.Sprintf("Apps: %d, Sites: %d, Windows: %d, Sleep: %d",
			len(new.AllowedApps.Apps), len(new.AllowedSites.Sites),
			len(new.Schedule.EntertainmentWindows), len(new.Schedule.SleepTimes))
	}

	var parts []string

	oldApps := len(old.AllowedApps.Apps)
	newApps := len(new.AllowedApps.Apps)
	if oldApps != newApps {
		parts = append(parts, fmt.Sprintf("Apps: %d→%d", oldApps, newApps))
	}

	oldSites := len(old.AllowedSites.Sites)
	newSites := len(new.AllowedSites.Sites)
	if oldSites != newSites {
		parts = append(parts, fmt.Sprintf("Sites: %d→%d", oldSites, newSites))
	}

	oldWin := len(old.Schedule.EntertainmentWindows)
	newWin := len(new.Schedule.EntertainmentWindows)
	if oldWin != newWin {
		parts = append(parts, fmt.Sprintf("Windows: %d→%d", oldWin, newWin))
	}

	oldSleep := len(old.Schedule.SleepTimes)
	newSleep := len(new.Schedule.SleepTimes)
	if oldSleep != newSleep {
		parts = append(parts, fmt.Sprintf("Sleep: %d→%d", oldSleep, newSleep))
	}

	oldEntApps := len(old.Schedule.EntertainmentApps)
	newEntApps := len(new.Schedule.EntertainmentApps)
	if oldEntApps != newEntApps {
		parts = append(parts, fmt.Sprintf("EntApps: %d→%d", oldEntApps, newEntApps))
	}

	if old.Schedule.FullLogging != new.Schedule.FullLogging {
		parts = append(parts, fmt.Sprintf("FullLog: %v→%v", old.Schedule.FullLogging, new.Schedule.FullLogging))
	}

	if len(parts) == 0 {
		return fmt.Sprintf("No changes (Apps: %d, Sites: %d)", newApps, newSites)
	}

	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
