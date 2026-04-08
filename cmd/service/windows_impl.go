//go:build windows

package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

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

// isProcessRunning проверяет запущен ли процесс с указанным именем.
func isProcessRunning(name string) bool {
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("IMAGENAME eq %s", name), "/NH")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), name)
}

// launchInUserSession запускает процесс в активной пользовательской сессии.
func launchInUserSession(exePath string) error {
	wtsapi32 := windows.NewLazySystemDLL("wtsapi32.dll")
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	advapi32 := windows.NewLazySystemDLL("advapi32.dll")
	userenv := windows.NewLazySystemDLL("userenv.dll")

	procWTSGetActiveConsoleSessionId := kernel32.NewProc("WTSGetActiveConsoleSessionId")
	procWTSQueryUserToken := wtsapi32.NewProc("WTSQueryUserToken")
	procCreateEnvironmentBlock := userenv.NewProc("CreateEnvironmentBlock")
	procDestroyEnvironmentBlock := userenv.NewProc("DestroyEnvironmentBlock")
	procCreateProcessAsUserW := advapi32.NewProc("CreateProcessAsUserW")

	sessionID, _, _ := procWTSGetActiveConsoleSessionId.Call()
	if sessionID == 0xFFFFFFFF {
		return fmt.Errorf("no active session")
	}

	var userToken windows.Handle
	r, _, err := procWTSQueryUserToken.Call(sessionID, uintptr(unsafe.Pointer(&userToken)))
	if r == 0 {
		return fmt.Errorf("WTSQueryUserToken: %v", err)
	}
	defer windows.CloseHandle(userToken)

	var envBlock uintptr
	procCreateEnvironmentBlock.Call(uintptr(unsafe.Pointer(&envBlock)), uintptr(userToken), 0)
	if envBlock != 0 {
		defer procDestroyEnvironmentBlock.Call(envBlock)
	}

	cmdLine, _ := windows.UTF16PtrFromString(`"` + exePath + `"`)

	const CREATE_UNICODE_ENVIRONMENT = 0x00000400
	const CREATE_NO_WINDOW = 0x08000000

	type STARTUPINFO struct {
		Cb            uint32
		_             *uint16
		Desktop       *uint16
		_             *uint16
		_             uint32
		_             uint32
		_             uint32
		_             uint32
		_             uint32
		_             uint32
		_             uint32
		_             uint32
		ShowWindow    uint16
		_             uint16
		_             *byte
		StdInput      windows.Handle
		StdOutput     windows.Handle
		StdError      windows.Handle
	}
	type PROCESS_INFORMATION struct {
		Process   windows.Handle
		Thread    windows.Handle
		ProcessId uint32
		ThreadId  uint32
	}

	desktop, _ := windows.UTF16PtrFromString("winsta0\\default")
	si := STARTUPINFO{Desktop: desktop}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi PROCESS_INFORMATION

	r, _, err = procCreateProcessAsUserW.Call(
		uintptr(userToken),
		0,
		uintptr(unsafe.Pointer(cmdLine)),
		0, 0, 0,
		CREATE_UNICODE_ENVIRONMENT|CREATE_NO_WINDOW,
		envBlock,
		0,
		uintptr(unsafe.Pointer(&si)),
		uintptr(unsafe.Pointer(&pi)),
	)
	if r == 0 {
		return fmt.Errorf("CreateProcessAsUser: %v", err)
	}
	windows.CloseHandle(pi.Process)
	windows.CloseHandle(pi.Thread)
	return nil
}

// getSystemMetrics возвращает CPU%, Memory%, Network MB/s через WMI/системные вызовы.
func getSystemMetrics() (cpuPct, memPct, netMBps float64) {
	// CPU — через GetSystemTimes.
	cpuPct = getCPUUsage()

	// Memory — через GlobalMemoryStatusEx.
	type memStatusEx struct {
		Length               uint32
		MemoryLoad           uint32
		TotalPhys            uint64
		AvailPhys            uint64
		TotalPageFile        uint64
		AvailPageFile        uint64
		TotalVirtual         uint64
		AvailVirtual         uint64
		AvailExtendedVirtual uint64
	}
	var ms memStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx := kernel32.NewProc("GlobalMemoryStatusEx")
	procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	memPct = float64(ms.MemoryLoad)

	// Network — через GetIfTable2 (суммарные байты).
	netMBps = getNetworkMBps()

	return
}

var (
	lastIdleTime   uint64
	lastKernelTime uint64
	lastUserTime   uint64
	cpuInitialized bool
)

func getCPUUsage() float64 {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	proc := kernel32.NewProc("GetSystemTimes")

	var idle, kernel, user [8]byte
	proc.Call(
		uintptr(unsafe.Pointer(&idle[0])),
		uintptr(unsafe.Pointer(&kernel[0])),
		uintptr(unsafe.Pointer(&user[0])),
	)

	idleTime := *(*uint64)(unsafe.Pointer(&idle[0]))
	kernelTime := *(*uint64)(unsafe.Pointer(&kernel[0]))
	userTime := *(*uint64)(unsafe.Pointer(&user[0]))

	if !cpuInitialized {
		lastIdleTime = idleTime
		lastKernelTime = kernelTime
		lastUserTime = userTime
		cpuInitialized = true
		return 0
	}

	idleDiff := idleTime - lastIdleTime
	kernelDiff := kernelTime - lastKernelTime
	userDiff := userTime - lastUserTime

	lastIdleTime = idleTime
	lastKernelTime = kernelTime
	lastUserTime = userTime

	total := kernelDiff + userDiff
	if total == 0 {
		return 0
	}
	return float64(total-idleDiff) / float64(total) * 100
}

var lastNetBytesTotal uint64
var netInitialized bool

func getNetworkMBps() float64 {
	iphlpapi := windows.NewLazySystemDLL("iphlpapi.dll")
	procGetIfTable := iphlpapi.NewProc("GetIfTable")

	var size uint32
	procGetIfTable.Call(0, uintptr(unsafe.Pointer(&size)), 1)
	if size == 0 {
		return 0
	}

	buf := make([]byte, size)
	r, _, _ := procGetIfTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 1)
	if r != 0 {
		return 0
	}

	numEntries := *(*uint32)(unsafe.Pointer(&buf[0]))
	var totalBytes uint64

	// MIB_IFTABLE: 4 bytes numEntries, then MIB_IFROW entries (860 bytes each).
	const ifRowSize = 860
	const inOctetsOffset = 552
	const outOctetsOffset = 576

	for i := uint32(0); i < numEntries; i++ {
		off := 4 + int(i)*ifRowSize
		if off+ifRowSize > len(buf) {
			break
		}
		inOctets := *(*uint32)(unsafe.Pointer(&buf[off+inOctetsOffset]))
		outOctets := *(*uint32)(unsafe.Pointer(&buf[off+outOctetsOffset]))
		totalBytes += uint64(inOctets) + uint64(outOctets)
	}

	if !netInitialized {
		lastNetBytesTotal = totalBytes
		netInitialized = true
		return 0
	}

	diff := totalBytes - lastNetBytesTotal
	lastNetBytesTotal = totalBytes

	// diff за 15 секунд (тик), конвертируем в MB/s.
	mbps := float64(diff) / 15.0 / 1024.0 / 1024.0
	return mbps
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
