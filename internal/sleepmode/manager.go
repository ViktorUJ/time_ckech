package sleepmode

import (
	"context"
	"fmt"
	"log"

	"parental-control-service/internal/enforcer"
	"parental-control-service/internal/monitor"
)

// SleepModeManager manages the full lockdown sleep mode.
// During sleep time, ALL user processes (including allowed ones) are terminated;
// only system processes are left running.
type SleepModeManager struct {
	enforcer *enforcer.Enforcer
	notifier enforcer.Notifier
}

// NewSleepModeManager creates a new SleepModeManager with the given dependencies.
func NewSleepModeManager(enf *enforcer.Enforcer, notifier enforcer.Notifier) *SleepModeManager {
	return &SleepModeManager{
		enforcer: enf,
		notifier: notifier,
	}
}

// Enforce applies sleep mode: shows a notification and terminates all user processes
// (both allowed and restricted). System processes are skipped.
// Errors from individual process terminations are logged but do not stop the loop.
func (sm *SleepModeManager) Enforce(ctx context.Context, processes []monitor.ProcessInfo) error {
	_ = sm.notifier.ShowNotification("Родительский контроль", "Сейчас время сна. Компьютер недоступен")

	for _, p := range processes {
		if p.IsSystem {
			continue
		}
		if err := sm.enforcer.TerminateProcess(ctx, p.PID); err != nil {
			log.Printf("sleepmode: failed to terminate process %s (pid %d): %v", p.Name, p.PID, err)
		}
	}
	return nil
}

// WarnUpcoming shows a notification warning the user about upcoming sleep time.
func (sm *SleepModeManager) WarnUpcoming(minutesLeft int) error {
	msg := fmt.Sprintf("До начала времени сна осталось %d мин. Пожалуйста, сохраните свою работу.", minutesLeft)
	return sm.notifier.ShowNotification("Родительский контроль", msg)
}
