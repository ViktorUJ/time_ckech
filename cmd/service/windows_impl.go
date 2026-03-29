//go:build windows

package main

import (
	"fmt"
	"log"
	"os/exec"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/eventlog"
)

// windowsEventLogWriter implements logger.EventLogWriter using the Windows Event Log.
type windowsEventLogWriter struct {
	elog *eventlog.Log
}

func newEventLogWriter(source string) (*windowsEventLogWriter, error) {
	elog, err := eventlog.Open(source)
	if err != nil {
		return nil, fmt.Errorf("open event log source %q: %w", source, err)
	}
	return &windowsEventLogWriter{elog: elog}, nil
}

func (w *windowsEventLogWriter) Info(eventID uint32, msg string) error {
	return w.elog.Info(eventID, msg)
}

func (w *windowsEventLogWriter) Warning(eventID uint32, msg string) error {
	return w.elog.Warning(eventID, msg)
}

func (w *windowsEventLogWriter) Error(eventID uint32, msg string) error {
	return w.elog.Error(eventID, msg)
}

func (w *windowsEventLogWriter) Close() error {
	return w.elog.Close()
}

// windowsProcessKiller implements enforcer.ProcessKiller using Windows API.
type windowsProcessKiller struct{}

func newWindowsProcessKiller() *windowsProcessKiller {
	return &windowsProcessKiller{}
}

func (k *windowsProcessKiller) GracefulKill(pid uint32) error {
	// Send WM_CLOSE to the process's main window via a helper.
	// For console processes, generate a CTRL_CLOSE_EVENT.
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	// Attempt graceful: use taskkill without /F first.
	cmd := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", pid))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("graceful kill pid %d: %w", pid, err)
	}

	// Wait briefly for the process to exit.
	event, _ := windows.WaitForSingleObject(handle, uint32((3 * time.Second).Milliseconds()))
	if event != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("process %d did not exit after graceful kill", pid)
	}
	return nil
}

func (k *windowsProcessKiller) ForceKill(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("open process %d for force kill: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	if err := windows.TerminateProcess(handle, 1); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	return nil
}

// windowsNotifier implements enforcer.Notifier — складывает уведомления в очередь.
type windowsNotifier struct {
	queue func(title, message string)
}

func newWindowsNotifier() *windowsNotifier {
	return &windowsNotifier{}
}

func (n *windowsNotifier) ShowNotification(title, message string) error {
	if n.queue != nil {
		n.queue(title, message)
	}
	return nil
}

// setLowPriority sets the current process priority to BELOW_NORMAL_PRIORITY_CLASS
// so the service doesn't compete with user applications for CPU time.
func setLowPriority() {
	const BELOW_NORMAL_PRIORITY_CLASS = 0x00004000

	handle, err := windows.GetCurrentProcess()
	if err != nil {
		log.Printf("[service] warning: could not get current process handle: %v", err)
		return
	}

	mod := windows.NewLazySystemDLL("kernel32.dll")
	proc := mod.NewProc("SetPriorityClass")

	ret, _, err := proc.Call(uintptr(handle), uintptr(BELOW_NORMAL_PRIORITY_CLASS))
	if ret == 0 {
		log.Printf("[service] warning: SetPriorityClass failed: %v", err)
	}
}
