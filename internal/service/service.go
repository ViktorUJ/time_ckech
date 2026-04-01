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
	"parental-control-service/internal/learning"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	tickInterval         = 15 * time.Second
	stateSaveInterval    = 60 * time.Second
	configUpdateInterval = 5 * time.Minute
)

// sleepOverrideData — переопределение времени сна на сегодня.
type sleepOverrideData struct {
	Date     string // "2006-01-02"
	NewStart string // "HH:MM" или "" (не менять)
	NewEnd   string // "HH:MM" или "" (не менять)
}

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
	learningCollector *learning.Collector

	entertainmentSeconds int
	computerSeconds      int // общее время за компьютером сегодня
	currentWindowStart   string
	currentWindowEnd     string
	currentDayType       string // текущий тип дня для отслеживания смены
	lastStateSave        time.Time
	lastTick             time.Time
	httpServerRunning    bool

	// Данные о браузерных URL, полученные от tray-приложения.
	browserURLsMu sync.Mutex
	browserURLs   []httplog.BrowserURLEntry

	// One-shot notification flags (reset on window/mode change).
	notifiedLimitReached  bool
	notifiedSleepStart    bool
	lastSleepWarnMinute   int // последняя минута на которой показали предупреждение о сне (-1 = не показывали)

	// Счётчик тиков для отложенной блокировки сайтов.
	siteWarningTicks map[string]int // domain → количество тиков с предупреждением

	// Очередь уведомлений для tray.
	notifMu     sync.Mutex
	notifQueue  []httplog.Notification

	// Пауза: временная приостановка всех ограничений.
	pauseMu       sync.Mutex
	pauseUntil    time.Time // нулевое значение = пауза не активна
	serviceMode   string    // "normal", "filter_paused", "entertainment_paused", "learning"
	modeUntil     time.Time // время окончания текущего режима (нулевое = бессрочно)
	bonusSeconds  int       // бонусное время развлечений (может быть отрицательным)
	sleepOverride *sleepOverrideData // переопределение сна на сегодня
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
		serviceMode:    "normal",
		learningCollector: learning.NewCollector(dataDir),
		siteWarningTicks: make(map[string]int),
		lastSleepWarnMinute: -1,
	}
}

// Run starts the main service loop. It restores state, starts background
// goroutines, and runs the main tick loop until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	// 1. Restore state from disk.
	now := time.Now()
	restored := s.stateManager.Restore(now)
	s.entertainmentSeconds = restored.EntertainmentSeconds
	s.computerSeconds = restored.ComputerSeconds
	s.bonusSeconds = restored.BonusSeconds
	s.lastTick = now
	s.lastStateSave = now

	// Restore window boundaries from saved state.
	if !restored.WindowStart.IsZero() && !restored.WindowEnd.IsZero() {
		s.currentWindowStart = restored.WindowStart.Format("15:04")
		s.currentWindowEnd = restored.WindowEnd.Format("15:04")
	}

	// Restore sleep override (only if it's for today).
	if restored.SleepOverrideDate == now.Format("2006-01-02") {
		s.sleepOverride = &sleepOverrideData{
			Date:     restored.SleepOverrideDate,
			NewStart: restored.SleepOverrideStart,
			NewEnd:   restored.SleepOverrideEnd,
		}
	}

	// Restore service mode (if not expired).
	if restored.ServiceMode != "" && restored.ServiceMode != "normal" {
		if restored.ModeUntil.IsZero() || now.Before(restored.ModeUntil) {
			s.serviceMode = restored.ServiceMode
			s.modeUntil = restored.ModeUntil
			if s.serviceMode == "filter_paused" || s.serviceMode == "unrestricted" {
				s.pauseUntil = s.modeUntil
			}
		}
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
	startNow := time.Now()
	schedAtStart := s.scheduler.CurrentState(startNow)
	s.currentDayType = string(schedAtStart.DayType)
	startMsg := "ParentalControlService started. Schedule: " + s.currentDayType
	if schedAtStart.HolidayName != "" {
		startMsg += " (" + schedAtStart.HolidayName + ")"
	}
	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: startNow,
		EventType: logger.EventServiceStart,
		Message:   startMsg,
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

	// Режим обучения: логируем всё, но не блокируем.
	isLearning := s.IsLearningMode()

	// Собираем детальную информацию в режиме обучения.
	if isLearning && s.learningCollector != nil {
		s.learningCollector.CollectTick()
	}

	elapsed := now.Sub(s.lastTick)
	s.lastTick = now

	// Считаем общее время за компьютером (всегда, кроме паузы).
	s.computerSeconds += int(elapsed.Seconds())

	cfg := s.configManager.Current()
	if cfg == nil {
		// No config available — fail-closed: block everything except system processes.
		s.failClosedTick(ctx, now)
		return
	}

	// a. Check schedule → get current mode.
	schedState := s.scheduler.CurrentState(now)

	// Log day type change (workday/weekend/holiday).
	newDayType := string(schedState.DayType)
	if newDayType != s.currentDayType {
		// Сброс счётчиков при смене дня.
		if s.currentDayType != "" {
			s.computerSeconds = 0
			s.bonusSeconds = 0
			s.sleepOverride = nil
		}
		dayMsg := "Schedule: " + newDayType
		if schedState.HolidayName != "" {
			dayMsg += " (" + schedState.HolidayName + ")"
		}
		_ = s.logger.LogEvent(logger.LogEntry{
			Timestamp: now,
			EventType: logger.EventInfo,
			Message:   dayMsg,
		})
		s.currentDayType = newDayType
	}

	// Reset sleep notification flag when NOT in sleep mode.
	if schedState.Mode != scheduler.ModeSleepTime {
		s.notifiedSleepStart = false
	}

	// b. If sleep mode → enforce sleep (kill all user processes).
	// В режиме обучения — не блокируем даже во время сна.
	if schedState.Mode == scheduler.ModeSleepTime && !isLearning {
		s.handleSleepMode(ctx, now)
		s.maybeSaveState(now)
		return
	}

	// b2. Check total computer time limit.
	if cfg.Schedule.TotalComputerMinutes > 0 && !isLearning {
		if s.computerSeconds/60 >= cfg.Schedule.TotalComputerMinutes {
			// Лимит общего времени исчерпан — блокируем как при sleep mode.
			s.handleSleepMode(ctx, now)
			s.maybeSaveState(now)
			return
		}
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
	// В режиме entertainment_paused — не считаем время развлечений.
	if hasRestrictedActivity && !s.IsEntertainmentPaused() {
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
	// В режиме обучения или entertainment_paused — не блокируем.
	limitMinutes := schedState.LimitMinutes + s.bonusSeconds/60
	if limitMinutes < 0 {
		limitMinutes = 0
	}
	shouldBlock := enforcer.ShouldBlock(schedState.Mode, s.entertainmentSeconds, limitMinutes)

	if shouldBlock && !isLearning && !s.IsEntertainmentPaused() {
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

	// Block restricted processes — сразу на первом тике.
	for _, p := range processes {
		if p.IsSystem || p.IsAllowed {
			continue
		}
		_ = s.notifier.ShowNotification("Parental Control", msg)
		if err := s.enforcer.TerminateProcess(ctx, p.PID); err != nil {
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

	// Restricted сайты — предупреждение, блокировка на 3-м тике.
	const siteBlockAfterTicks = 3
	for _, activity := range browserActivities {
		if activity.IsAllowed {
			continue
		}
		s.siteWarningTicks[activity.Domain]++
		ticks := s.siteWarningTicks[activity.Domain]

		if ticks >= siteBlockAfterTicks {
			// Блокируем — закрываем окно браузера.
			_ = s.notifier.ShowNotification("Parental Control",
				fmt.Sprintf("Blocked site: %s", activity.Domain))
			_ = s.logger.LogEvent(logger.LogEntry{
				Timestamp: time.Now(),
				EventType: logger.EventBlock,
				URL:       activity.URL,
				Browser:   activity.Browser,
				Message:   fmt.Sprintf("Blocked site: %s (%s)", activity.Domain, activity.Browser),
			})
			// Сбрасываем счётчик после блокировки.
			delete(s.siteWarningTicks, activity.Domain)
		} else {
			// Предупреждение.
			remaining := siteBlockAfterTicks - ticks
			warnMsg := fmt.Sprintf("%s\n\nClose the tab within %d ticks or the browser window will be closed.", activity.Domain, remaining)
			_ = s.notifier.ShowNotification("Parental Control", warnMsg)
			_ = s.logger.LogEvent(logger.LogEntry{
				Timestamp: time.Now(),
				EventType: logger.EventWarning,
				URL:       activity.URL,
				Browser:   activity.Browser,
				Message:   fmt.Sprintf("Warning: site %s will be blocked in %d ticks", activity.Domain, remaining),
			})
		}
	}

	// Очищаем счётчики для сайтов которые больше не открыты.
	activeDomains := make(map[string]bool)
	for _, a := range browserActivities {
		if !a.IsAllowed {
			activeDomains[a.Domain] = true
		}
	}
	for domain := range s.siteWarningTicks {
		if !activeDomains[domain] {
			delete(s.siteWarningTicks, domain)
		}
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
	// Warn about entertainment limit approaching — only log, no popup.
	if schedState.Mode == scheduler.ModeInsideWindow && schedState.LimitMinutes > 0 {
		if s.scheduler.ShouldWarnEntertainment(s.entertainmentSeconds, schedState.LimitMinutes) {
			remaining := schedState.LimitMinutes - s.entertainmentSeconds/60
			msg := fmt.Sprintf("Entertainment time ends in %d min.", remaining)
			_ = s.logger.LogEvent(logger.LogEntry{
				Timestamp: now,
				EventType: logger.EventWarning,
				Message:   msg,
			})
		}
	}

	// Warn about sleep time approaching — only at 1 minute.
	if shouldWarn, minutesLeft := s.scheduler.ShouldWarnSleep(now); shouldWarn {
		if minutesLeft <= 1 && s.lastSleepWarnMinute != 1 {
			s.lastSleepWarnMinute = 1
			_ = s.notifier.ShowNotification("Parental Control",
				fmt.Sprintf("Sleep time starts in %d min.", minutesLeft))
			_ = s.logger.LogEvent(logger.LogEntry{
				Timestamp: now,
				EventType: logger.EventWarning,
				Message:   fmt.Sprintf("Sleep time starts in %d min.", minutesLeft),
			})
		}
	} else {
		s.lastSleepWarnMinute = -1
	}
}

// saveState persists the current service state to disk.
func (s *Service) saveState(now time.Time) {
	st := &state.ServiceState{
		EntertainmentSeconds: s.entertainmentSeconds,
		BonusSeconds:         s.bonusSeconds,
		ComputerSeconds:      s.computerSeconds,
		LastTickTime:         now,
	}

	if s.currentWindowStart != "" && s.currentWindowEnd != "" {
		st.WindowStart = todayTime(now, s.currentWindowStart)
		st.WindowEnd = todayTime(now, s.currentWindowEnd)
	}

	// Сохраняем переопределение сна.
	s.pauseMu.Lock()
	if s.sleepOverride != nil {
		st.SleepOverrideDate = s.sleepOverride.Date
		st.SleepOverrideStart = s.sleepOverride.NewStart
		st.SleepOverrideEnd = s.sleepOverride.NewEnd
	}
	st.ServiceMode = s.serviceMode
	st.ModeUntil = s.modeUntil
	s.pauseMu.Unlock()

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

	svcMode := s.GetServiceMode()

	resp := httplog.StatusResponse{
		Mode:                 modeName,
		ServiceMode:          svcMode,
		DayType:              string(schedState.DayType),
		HolidayName:          schedState.HolidayName,
		VacationName:         schedState.VacationName,
		EntertainmentMinutes: s.entertainmentSeconds / 60,
		LimitMinutes:         schedState.LimitMinutes,
		BonusMinutes:         s.bonusSeconds / 60,
		ComputerMinutes:      s.computerSeconds / 60,
		ActiveProcesses:      []string{},
	}

	// Computer time limit from config.
	cfgCur := s.configManager.Current()
	if cfgCur != nil && cfgCur.Schedule.TotalComputerMinutes > 0 {
		resp.ComputerLimitMinutes = cfgCur.Schedule.TotalComputerMinutes
	}

	if schedState.LimitMinutes > 0 {
		effectiveLimit := schedState.LimitMinutes + s.bonusSeconds/60
		if effectiveLimit < 0 {
			effectiveLimit = 0
		}
		resp.LimitMinutes = effectiveLimit
		remaining := effectiveLimit - s.entertainmentSeconds/60
		if remaining < 0 {
			remaining = 0
		}
		resp.MinutesRemaining = remaining
	}

	if schedState.CurrentWindow != nil {
		w := fmt.Sprintf("%s-%s", schedState.CurrentWindow.Start, schedState.CurrentWindow.End)
		resp.ActiveWindow = &w
	} else {
		// Показываем ближайшее окно на сегодня (NextWindow).
		if nw := s.scheduler.NextWindowToday(now); nw != nil {
			resp.NextWindow = fmt.Sprintf("%s-%s (%d min)", nw.Start, nw.End, nw.LimitMinutes)
		}
	}

	// Информация о паузе/режиме.
	s.pauseMu.Lock()
	if svcMode != "normal" {
		resp.Paused = true
		if !s.modeUntil.IsZero() && time.Now().Before(s.modeUntil) {
			resp.PauseUntil = s.modeUntil.Format(time.RFC3339)
		}
	}
	// Информация о сне.
	if s.sleepOverride != nil && s.sleepOverride.Date == now.Format("2006-01-02") {
		resp.SleepOverride = s.sleepOverride.NewStart + "-" + s.sleepOverride.NewEnd
	}
	s.pauseMu.Unlock()

	// Расписание сна на сегодня из конфига.
	cfg := s.configManager.Current()
	if cfg != nil {
		today := strings.ToLower(now.Weekday().String())
		var sleepTimes []config.SleepTimeSlot

		switch schedState.DayType {
		case scheduler.DayTypeVacation:
			if v := s.scheduler.ActiveVacationAt(now); v != nil && len(v.SleepTimes) > 0 {
				sleepTimes = v.SleepTimes
			} else {
				sleepTimes = cfg.Schedule.SleepTimes
			}
		case scheduler.DayTypeHoliday:
			if len(cfg.Schedule.HolidaySleepTimes) > 0 {
				sleepTimes = cfg.Schedule.HolidaySleepTimes
			} else {
				sleepTimes = cfg.Schedule.SleepTimes
			}
		default:
			sleepTimes = cfg.Schedule.SleepTimes
		}

		for _, st := range sleepTimes {
			for _, d := range st.Days {
				if strings.ToLower(d) == today {
					resp.SleepWindow = st.Start + "-" + st.End
					break
				}
			}
			if resp.SleepWindow != "" {
				break
			}
		}
	}

	return resp
}

// IsPaused возвращает true, если пауза активна.
func (s *Service) IsPaused() bool {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()

	// Проверяем истечение режима по времени.
	if !s.modeUntil.IsZero() && time.Now().After(s.modeUntil) {
		s.serviceMode = "normal"
		s.modeUntil = time.Time{}
		s.pauseUntil = time.Time{}
		return false
	}

	return s.serviceMode == "filter_paused" || s.serviceMode == "unrestricted"
}

// GetServiceMode возвращает текущий режим работы сервиса.
func (s *Service) GetServiceMode() string {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()

	// Проверяем истечение режима.
	if !s.modeUntil.IsZero() && time.Now().After(s.modeUntil) {
		s.serviceMode = "normal"
		s.modeUntil = time.Time{}
		s.pauseUntil = time.Time{}
	}

	return s.serviceMode
}

// IsEntertainmentPaused возвращает true если развлечения приостановлены.
func (s *Service) IsEntertainmentPaused() bool {
	mode := s.GetServiceMode()
	return mode == "entertainment_paused" || mode == "self_entertainment_paused"
}

// IsLearningMode возвращает true если сервис в режиме обучения.
func (s *Service) IsLearningMode() bool {
	return s.GetServiceMode() == "learning"
}

// SetServiceMode устанавливает режим работы сервиса.
func (s *Service) SetServiceMode(password, mode string, minutes int) (bool, string) {
	if s.passwordHash == "" {
		return false, "Password not configured"
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.passwordHash), []byte(password)); err != nil {
		return false, "Wrong password"
	}

	switch mode {
	case "normal", "filter_paused", "entertainment_paused", "learning", "unrestricted", "self_entertainment_paused":
	default:
		return false, "Invalid mode"
	}

	s.pauseMu.Lock()
	oldMode := s.serviceMode
	s.serviceMode = mode
	if minutes > 0 {
		s.modeUntil = time.Now().Add(time.Duration(minutes) * time.Minute)
		// Для обратной совместимости с filter_paused.
		if mode == "filter_paused" {
			s.pauseUntil = s.modeUntil
		}
	} else {
		s.modeUntil = time.Time{}
		if mode == "filter_paused" {
			// Бессрочная пауза фильтрации — ставим далёкое время.
			s.pauseUntil = time.Now().Add(24 * time.Hour)
		}
	}
	if mode == "normal" {
		s.pauseUntil = time.Time{}
		s.modeUntil = time.Time{}
	}
	s.pauseMu.Unlock()

	msg := fmt.Sprintf("Mode set to %s", mode)
	if minutes > 0 {
		msg = fmt.Sprintf("Mode set to %s for %d min.", mode, minutes)
	}
	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventInfo,
		Message:   msg,
	})

	// Управление learning collector.
	if mode == "learning" && oldMode != "learning" {
		s.learningCollector.Start()
	} else if mode != "learning" && oldMode == "learning" {
		s.learningCollector.Stop()
	}

	return true, msg
}

// Pause ставит паузу на minutes минут, если пароль верный.
func (s *Service) Pause(password string, minutes int) (bool, string) {
	return s.SetServiceMode(password, "filter_paused", minutes)
}

// Unpause снимает любой режим, возвращая в normal.
func (s *Service) Unpause(password string) (bool, string) {
	return s.SetServiceMode(password, "normal", 0)
}

// AddUsage добавляет использованное время развлечений (играл на другом компьютере).
func (s *Service) AddUsage(password string, minutes int, reason string) (bool, string) {
	if s.passwordHash == "" {
		return false, "Password not configured"
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.passwordHash), []byte(password)); err != nil {
		return false, "Wrong password"
	}
	if minutes < 1 || minutes > 480 {
		return false, "Allowed range: 1 to 480 minutes"
	}

	s.entertainmentSeconds += minutes * 60

	msg := fmt.Sprintf("Manual usage added: %d min.", minutes)
	if reason != "" {
		msg += " Reason: " + reason
	}
	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventInfo,
		Message:   msg,
	})
	return true, msg
}

// AdjustBonus добавляет или убирает бонусное время развлечений.
func (s *Service) AdjustBonus(password string, minutes int, reason string) (bool, string) {
	if s.passwordHash == "" {
		return false, "Password not configured"
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.passwordHash), []byte(password)); err != nil {
		return false, "Wrong password"
	}
	if minutes == 0 {
		return false, "Minutes cannot be zero"
	}

	s.pauseMu.Lock()
	s.bonusSeconds += minutes * 60
	s.pauseMu.Unlock()

	action := "added"
	if minutes < 0 {
		action = "removed"
	}
	msg := fmt.Sprintf("Bonus time %s: %+d min. Total bonus: %+d min.", action, minutes, s.bonusSeconds/60)
	if reason != "" {
		msg += " Reason: " + reason
	}
	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventInfo,
		Message:   msg,
	})
	return true, msg
}

// AdjustSleep изменяет время сна на сегодня.
func (s *Service) AdjustSleep(password string, newStart, newEnd, reason string) (bool, string) {
	if s.passwordHash == "" {
		return false, "Password not configured"
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.passwordHash), []byte(password)); err != nil {
		return false, "Wrong password"
	}
	if newStart == "" && newEnd == "" {
		return false, "At least one of start or end must be specified"
	}

	today := time.Now().Format("2006-01-02")
	s.pauseMu.Lock()
	s.sleepOverride = &sleepOverrideData{
		Date:     today,
		NewStart: newStart,
		NewEnd:   newEnd,
	}
	s.pauseMu.Unlock()

	msg := fmt.Sprintf("Sleep time adjusted for %s.", today)
	if newStart != "" {
		msg += fmt.Sprintf(" Start: %s.", newStart)
	}
	if newEnd != "" {
		msg += fmt.Sprintf(" End: %s.", newEnd)
	}
	if reason != "" {
		msg += " Reason: " + reason
	}
	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventInfo,
		Message:   msg,
	})
	return true, msg
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

// SelfPauseEntertainment — пауза развлечений без пароля (от ребёнка).
func (s *Service) SelfPauseEntertainment() (bool, string) {
	s.pauseMu.Lock()
	s.serviceMode = "self_entertainment_paused"
	s.modeUntil = time.Time{} // бессрочно до ручной отмены
	s.pauseMu.Unlock()

	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventInfo,
		Message:   "Entertainment self-paused by user",
	})
	return true, "Entertainment paused"
}

// SelfUnpauseEntertainment — снятие паузы развлечений без пароля.
func (s *Service) SelfUnpauseEntertainment() (bool, string) {
	s.pauseMu.Lock()
	if s.serviceMode != "self_entertainment_paused" {
		s.pauseMu.Unlock()
		return false, "Not in self-pause mode"
	}
	s.serviceMode = "normal"
	s.modeUntil = time.Time{}
	s.pauseMu.Unlock()

	_ = s.logger.LogEvent(logger.LogEntry{
		Timestamp: time.Now(),
		EventType: logger.EventInfo,
		Message:   "Entertainment self-pause removed by user",
	})
	return true, "Entertainment resumed"
}

// QueueNotification добавляет уведомление в очередь для tray.
func (s *Service) QueueNotification(title, message string) {
	s.notifMu.Lock()
	s.notifQueue = append(s.notifQueue, httplog.Notification{Title: title, Message: message})
	s.notifMu.Unlock()
}

// DrainNotifications возвращает и очищает очередь уведомлений.
func (s *Service) DrainNotifications() []httplog.Notification {
	s.notifMu.Lock()
	defer s.notifMu.Unlock()
	if len(s.notifQueue) == 0 {
		return nil
	}
	result := s.notifQueue
	s.notifQueue = nil
	return result
}

// GetLearningCollector возвращает learning collector для HTTP API.
func (s *Service) GetLearningCollector() *learning.Collector {
	return s.learningCollector
}

// GetCurrentReport implements httplog.LearningProvider.
func (s *Service) GetCurrentReport() interface{} {
	if s.learningCollector == nil {
		return nil
	}
	return s.learningCollector.GetCurrentReport()
}

// ListReports implements httplog.LearningProvider.
func (s *Service) ListReports() []string {
	if s.learningCollector == nil {
		return nil
	}
	return s.learningCollector.ListReports()
}

// ReadReport implements httplog.LearningProvider.
func (s *Service) ReadReport(name string) ([]byte, error) {
	if s.learningCollector == nil {
		return nil, fmt.Errorf("not available")
	}
	return s.learningCollector.ReadReport(name)
}

// ClearReports implements httplog.LearningProvider.
func (s *Service) ClearReports() error {
	if s.learningCollector == nil {
		return fmt.Errorf("not available")
	}
	return s.learningCollector.ClearReports()
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
	// Если пауза активна или режим обучения — не блокируем ничего.
	// В режиме обучения логируем сайты.
	if s.IsLearningMode() {
		cfg := s.configManager.Current()
		for _, u := range urls {
			// Не логируем системные сайты (localhost и т.д.).
			if cfg != nil && browser.IsSystemSite(u.URL, cfg.AllowedSites.Sites) {
				continue
			}
			_ = s.logger.LogEvent(logger.LogEntry{
				Timestamp: time.Now(),
				EventType: logger.EventInfo,
				URL:       u.URL,
				Browser:   u.Browser,
				Message:   fmt.Sprintf("Learning: site visited %s (%s)", u.URL, u.Browser),
			})
		}
		return nil, ""
	}
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
