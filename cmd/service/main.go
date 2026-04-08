//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"parental-control-service/internal/browser"
	"parental-control-service/internal/config"
	"parental-control-service/internal/enforcer"
	"parental-control-service/internal/httplog"
	"parental-control-service/internal/logger"
	"parental-control-service/internal/monitor"
	"parental-control-service/internal/scheduler"
	"parental-control-service/internal/service"
	"parental-control-service/internal/sleepmode"
	"parental-control-service/internal/state"
	"parental-control-service/internal/stats"
)

const serviceName = "ParentalControlService"

const installDir = `C:\Program Files\ParentalControlService`

// Paths.
const (
	dataDir         = `C:\ProgramData\ParentalControlService`
	statePath       = dataDir + `\state.json`
	fullLogDir      = dataDir + `\logs`
	fullLogPath     = dataDir + `\logs\full.log`
	blockedPagePath = dataDir + `\blocked.html`
	defaultHTTPPort = 8080
)

func main() {
	// Set process priority to Below Normal so we don't compete with user apps.
	setLowPriority()

	// Run as a Windows service via svc.Run.
	if err := svc.Run(serviceName, &parentalControlHandler{}); err != nil {
		log.Fatalf("[service] svc.Run failed: %v", err)
	}
}

// parentalControlHandler implements svc.Handler for the Windows Service Control Manager.
type parentalControlHandler struct{}

// Execute is the main entry point called by the Windows SCM.
// It handles Start, Stop, Interrogate, and Shutdown commands.
func (h *parentalControlHandler) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (svcSpecificEC bool, exitCode uint32) {
	// Accept Stop, Shutdown, and Interrogate commands.
	const acceptedCmds = svc.AcceptStop | svc.AcceptShutdown

	// Report that we are starting.
	status <- svc.Status{State: svc.StartPending}

	// Initialize all components.
	svcInstance, lgr, err := initService()
	if err != nil {
		log.Printf("[service] initialization failed: %v", err)
		return true, 1
	}
	defer func() {
		if lgr != nil {
			lgr.Close()
		}
	}()

	// Create a cancellable context for the service main loop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the service main loop in a goroutine.
	done := make(chan error, 1)
	go func() {
		done <- svcInstance.Run(ctx)
	}()

	// Report that we are now running.
	status <- svc.Status{State: svc.Running, Accepts: acceptedCmds}

	// Watchdog: проверяем tray и browser-agent каждые 30 секунд.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ensureUserProcesses()
			}
		}
	}()

	// Process SCM commands.
	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				// Respond with current status.
				status <- req.CurrentStatus

			case svc.Stop, svc.Shutdown:
				// Report that we are stopping.
				status <- svc.Status{State: svc.StopPending}

				// Cancel the context to trigger graceful shutdown.
				cancel()

				// Wait for the service loop to finish (with timeout).
				select {
				case <-done:
				case <-time.After(30 * time.Second):
					log.Printf("[service] shutdown timed out after 30 seconds")
				}

				return false, 0

			default:
				log.Printf("[service] unexpected SCM command: %v", req.Cmd)
			}

		case err := <-done:
			// Service loop exited unexpectedly.
			if err != nil {
				log.Printf("[service] main loop error: %v", err)
				return true, 2
			}
			return false, 0
		}
	}
}

// initService creates and wires all service components.
func initService() (*service.Service, *logger.Logger, error) {
	// 0. Load GitHub URLs from settings.json (written by installer).
	settings, err := config.LoadSettings(dataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load settings: %w (was the service installed with installer.exe?)", err)
	}

	// 1. Logger: Event Log + Full Log.
	eventLog, err := newEventLogWriter(serviceName)
	if err != nil {
		return nil, nil, fmt.Errorf("open event log: %w", err)
	}

	// Ensure log directory exists.
	if err := os.MkdirAll(fullLogDir, 0o700); err != nil {
		eventLog.Close()
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}

	fullLog, err := logger.NewFullLogWriter(fullLogPath)
	if err != nil {
		eventLog.Close()
		return nil, nil, fmt.Errorf("create full log writer: %w", err)
	}

	lgr := logger.NewLogger(eventLog, fullLog, false)

	// 2. Config Manager — URLs from settings.json.
	urls := config.GitHubURLs{
		ConfigURL:   settings.ConfigURL,
		AppsURL:     settings.AppsURL,
		SitesURL:    settings.SitesURL,
		ScheduleURL: settings.ScheduleURL,
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	configMgr := config.NewConfigManager(urls, httpClient, "")

	// Try to load cached config from disk first.
	_ = configMgr.LoadCacheFromDisk()

	// 3. Process Monitor.
	enumerator := monitor.NewWindowsProcessEnumerator()
	// Start with empty allowed apps; will be updated when config loads.
	classifier := monitor.NewDefaultClassifier(nil, nil, nil)
	procMon := monitor.NewProcessMonitor(enumerator, classifier)

	// 4. Browser Monitor.
	// UIAutomation will be a real Windows implementation; for now use a placeholder.
	browserMon := browser.NewBrowserMonitor(nil, nil, blockedPagePath)

	// 5. Scheduler.
	sched := scheduler.NewScheduler(config.ScheduleConfig{})

	// 6. Enforcer.
	killer := newWindowsProcessKiller()
	notifier := newWindowsNotifier()
	enf := enforcer.NewEnforcer(killer, notifier)

	// 7. Sleep Mode Manager.
	sleepMgr := sleepmode.NewSleepModeManager(enf, notifier)

	// 8. State Manager.
	stateMgr := state.NewStateManager(statePath)

	// 9. HTTP Log Server — use port from settings, fallback to default.
	httpPort := settings.HTTPPort
	if httpPort == 0 {
		httpPort = defaultHTTPPort
	}
	httpSrv := httplog.NewHTTPLogServer(httpPort, nil, nil)

	// 10. Stats Tracker.
	statsTracker := stats.NewTracker(dataDir)

	// 11. Wire everything into the Service orchestrator.
	svcInstance := service.NewService(
		configMgr,
		procMon,
		browserMon,
		sched,
		enf,
		sleepMgr,
		stateMgr,
		lgr,
		httpSrv,
		notifier,
		statsTracker,
		settings.PasswordHash,
		dataDir,
	)

	// Подключаем очередь уведомлений: notifier → service → tray (через HTTP).
	notifier.queue = svcInstance.QueueNotification

	// Подключаем системные метрики.
	svcInstance.SetMetricsFunc(getSystemMetrics)
	// Прогреваем счётчики CPU и сети (первый вызов инициализирует).
	getSystemMetrics()

	// Set the service as the status provider for the HTTP server.
	httpSrv.SetStatusProvider(svcInstance)
	httpSrv.SetConfigProvider(svcInstance)
	httpSrv.SetBrowserReceiver(svcInstance)
	httpSrv.SetStatsProvider(svcInstance)
	httpSrv.SetPauseProvider(svcInstance)
	httpSrv.SetConfigReloader(svcInstance)
	httpSrv.SetPasswordChanger(svcInstance)
	httpSrv.SetConfigURLChanger(svcInstance)
	httpSrv.SetLearningProvider(svcInstance)
	httpSrv.SetAdjustmentProvider(svcInstance)
	httpSrv.SetFileLogProvider(fullLog)

	return svcInstance, lgr, nil
}

// ensureUserProcesses проверяет что tray.exe и browser-agent.exe запущены.
func ensureUserProcesses() {
	for _, name := range []string{"tray.exe", "browser-agent.exe"} {
		if !isProcessRunning(name) {
			exePath := filepath.Join(installDir, name)
			if _, err := os.Stat(exePath); err != nil {
				continue
			}
			if err := launchInUserSession(exePath); err != nil {
				log.Printf("[watchdog] failed to launch %s: %v", name, err)
			} else {
				log.Printf("[watchdog] relaunched %s", name)
			}
		}
	}
}

// configureRecovery sets the service recovery options so that Windows
// automatically restarts the service on failure.
func configureRecovery() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()

	// Configure 3 restart attempts with increasing delays.
	recoveryActions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}

	return s.SetRecoveryActions(recoveryActions, 86400) // Reset failure count after 24 hours
}
