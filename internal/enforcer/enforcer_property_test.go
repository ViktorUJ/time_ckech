package enforcer

import (
	"context"
	"errors"
	"testing"
	"time"

	"parental-control-service/internal/scheduler"

	"pgregory.net/rapid"
)

// Feature: parental-control-service, Property 7: Решение о блокировке неразрешённых программ
// **Validates: Requirements 6.2, 6.3, 6.4**

func TestPropertyShouldBlockDecision(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate random mode: ModeOutsideWindow=0, ModeInsideWindow=1, ModeSleepTime=2
		modeInt := rapid.IntRange(0, 2).Draw(t, "mode")
		mode := scheduler.Mode(modeInt)

		// Generate random entertainment seconds (0-36000 = up to 10 hours)
		entertainmentSeconds := rapid.IntRange(0, 36000).Draw(t, "entertainmentSeconds")

		// Generate random limit in minutes (1-600)
		limitMinutes := rapid.IntRange(1, 600).Draw(t, "limitMinutes")

		got := ShouldBlock(mode, entertainmentSeconds, limitMinutes)

		// Model: block == NOT (mode == ModeInsideWindow AND entertainmentSeconds/60 < limitMinutes)
		insideWindowAndUnderLimit := mode == scheduler.ModeInsideWindow && entertainmentSeconds/60 < limitMinutes
		expected := !insideWindowAndUnderLimit

		if got != expected {
			t.Fatalf(
				"ShouldBlock(mode=%d, entertainmentSeconds=%d, limitMinutes=%d): got %v, expected %v "+
					"(insideWindow=%v, usedMinutes=%d, underLimit=%v)",
				mode, entertainmentSeconds, limitMinutes, got, expected,
				mode == scheduler.ModeInsideWindow,
				entertainmentSeconds/60,
				entertainmentSeconds/60 < limitMinutes,
			)
		}
	})
}

// Feature: parental-control-service, Property 10: Протокол завершения процессов (graceful → force)
// **Validates: Requirements 7.4**

// mockProcessKiller tracks calls to GracefulKill and ForceKill for property testing.
type mockProcessKiller struct {
	gracefulCalled   bool
	forceCalled      bool
	gracefulSucceeds bool
}

func (m *mockProcessKiller) GracefulKill(pid uint32) error {
	m.gracefulCalled = true
	if m.gracefulSucceeds {
		return nil
	}
	return errors.New("graceful kill failed")
}

func (m *mockProcessKiller) ForceKill(pid uint32) error {
	m.forceCalled = true
	return nil
}

// mockNotifier is a no-op notifier for testing.
type mockNotifier struct{}

func (m *mockNotifier) ShowNotification(title, message string) error {
	return nil
}

func TestPropertyTerminateProcessProtocol(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate random scenario: graceful kill succeeds or fails
		gracefulSucceeds := rapid.Bool().Draw(t, "gracefulSucceeds")

		// Generate a random PID
		pid := rapid.Uint32Range(1, 65535).Draw(t, "pid")

		killer := &mockProcessKiller{gracefulSucceeds: gracefulSucceeds}
		notifier := &mockNotifier{}
		enf := NewEnforcer(killer, notifier)

		if gracefulSucceeds {
			// Graceful-succeeds path: no wait needed, returns immediately.
			ctx := context.Background()
			err := enf.TerminateProcess(ctx, pid)

			// Property: GracefulKill is ALWAYS called first
			if !killer.gracefulCalled {
				t.Fatal("GracefulKill must always be called first")
			}
			// Property: If GracefulKill succeeds → ForceKill is NOT called
			if killer.forceCalled {
				t.Fatal("ForceKill must NOT be called when GracefulKill succeeds")
			}
			if err != nil {
				t.Fatalf("expected no error when graceful succeeds, got: %v", err)
			}
		} else {
			// Graceful-fails path: TerminateProcess waits on select{time.After(5s), ctx.Done()}.
			// Use a short context timeout (50ms) so ctx.Done() fires quickly instead of
			// waiting the full 5 seconds. This verifies:
			// - GracefulKill is always attempted first
			// - When graceful fails, the system enters the wait-then-force-kill path
			// - Context cancellation is respected (graceful degradation)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			err := enf.TerminateProcess(ctx, pid)

			// Property: GracefulKill is ALWAYS called first
			if !killer.gracefulCalled {
				t.Fatal("GracefulKill must always be called first")
			}
			// With short context, ctx.Done() fires before time.After(5s),
			// so TerminateProcess returns context error without reaching ForceKill.
			if err == nil {
				t.Fatal("expected context error when graceful kill fails and context expires before force timeout")
			}
			// ForceKill should NOT be called because context expired before the 5s timer.
			// The force-kill-after-timeout path is verified in the deterministic test below.
			if killer.forceCalled {
				t.Fatal("ForceKill must NOT be called when context expires before the 5s timeout")
			}
		}
	})

	// Deterministic verification: when graceful fails and context lives long enough,
	// ForceKill IS called after the 5-second timeout.
	t.Run("ForceKillAfterTimeout", func(t *testing.T) {
		killer := &mockProcessKiller{gracefulSucceeds: false}
		notifier := &mockNotifier{}
		enf := NewEnforcer(killer, notifier)

		// Context with 10s timeout — well beyond the 5s internal timer.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		start := time.Now()
		err := enf.TerminateProcess(ctx, 12345)
		elapsed := time.Since(start)

		if !killer.gracefulCalled {
			t.Fatal("GracefulKill must be called first")
		}
		if !killer.forceCalled {
			t.Fatal("ForceKill must be called after graceful kill fails and timeout elapses")
		}
		if err != nil {
			t.Fatalf("expected no error after successful force kill, got: %v", err)
		}
		// Verify the wait was approximately 5 seconds (allow 4-7s tolerance).
		if elapsed < 4*time.Second || elapsed > 7*time.Second {
			t.Fatalf("expected ~5s wait before force kill, got %v", elapsed)
		}
	})
}
