//go:build windows

package monitor

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WindowsProcessEnumerator реализует ProcessEnumerator через Windows API
// (CreateToolhelp32Snapshot, Process32First, Process32Next).
type WindowsProcessEnumerator struct{}

// NewWindowsProcessEnumerator создаёт новый WindowsProcessEnumerator.
func NewWindowsProcessEnumerator() *WindowsProcessEnumerator {
	return &WindowsProcessEnumerator{}
}

// EnumerateProcesses перечисляет все запущенные процессы через Windows Toolhelp API
// и возвращает список RawProcess с PID, именем и путём к исполняемому файлу.
func (e *WindowsProcessEnumerator) EnumerateProcesses() ([]RawProcess, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	err = windows.Process32First(snapshot, &entry)
	if err != nil {
		return nil, fmt.Errorf("Process32First: %w", err)
	}

	var processes []RawProcess
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		exePath := queryProcessImagePath(entry.ProcessID)

		processes = append(processes, RawProcess{
			PID:     entry.ProcessID,
			Name:    name,
			ExePath: exePath,
		})

		err = windows.Process32Next(snapshot, &entry)
		if err != nil {
			break // Конец списка процессов
		}
	}

	return processes, nil
}
