// Package enforcer applies rules: terminates processes and shows notifications.
package enforcer

import (
	"context"
	"fmt"
	"time"

	"parental-control-service/internal/scheduler"
)

// Enforcer applies blocking rules by terminating processes and showing notifications.
type Enforcer struct {
	processKiller ProcessKiller
	notifier      Notifier
}

// NewEnforcer creates a new Enforcer with the given dependencies.
func NewEnforcer(processKiller ProcessKiller, notifier Notifier) *Enforcer {
	return &Enforcer{
		processKiller: processKiller,
		notifier:      notifier,
	}
}

// TerminateProcess attempts a graceful kill first, waits up to 5 seconds,
// then falls back to a force kill if the process is still running.
func (e *Enforcer) TerminateProcess(ctx context.Context, pid uint32) error {
	err := e.processKiller.GracefulKill(pid)
	if err == nil {
		return nil
	}

	// Graceful kill failed or process still running — wait up to 5 seconds then force kill.
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		return fmt.Errorf("context cancelled while waiting to force kill pid %d: %w", pid, ctx.Err())
	}

	if err := e.processKiller.ForceKill(pid); err != nil {
		return fmt.Errorf("force kill failed for pid %d: %w", pid, err)
	}
	return nil
}

// BlockWithWarning shows a notification with the given message and then terminates the process.
func (e *Enforcer) BlockWithWarning(ctx context.Context, pid uint32, message string) error {
	// Best-effort notification — don't fail the whole operation if notification fails.
	_ = e.notifier.ShowNotification("Родительский контроль", message)

	return e.TerminateProcess(ctx, pid)
}

// ShouldBlock decides whether a restricted application should be blocked based on
// the current scheduler mode, accumulated entertainment seconds, and the window limit.
//
//   - ModeSleepTime     → always block
//   - ModeOutsideWindow → always block
//   - ModeInsideWindow  → block only if entertainmentSeconds/60 >= limitMinutes
//   - any other mode    → allow (don't block)
func ShouldBlock(mode scheduler.Mode, entertainmentSeconds int, limitMinutes int) bool {
	switch mode {
	case scheduler.ModeSleepTime:
		return true
	case scheduler.ModeOutsideWindow:
		return true
	case scheduler.ModeInsideWindow:
		return entertainmentSeconds/60 >= limitMinutes
	default:
		return false
	}
}
