//go:build windows

package monitor

import "golang.org/x/sys/windows"

// queryProcessImagePath получает полный путь к исполняемому файлу процесса
// через OpenProcess + QueryFullProcessImageName.
// Возвращает пустую строку, если путь не удалось получить (например, для системных процессов
// с ограниченным доступом).
func queryProcessImagePath(pid uint32) string {
	// Пропускаем PID 0 (System Idle Process)
	if pid == 0 {
		return ""
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)

	var buf [windows.MAX_PATH]uint16
	size := uint32(len(buf))
	err = windows.QueryFullProcessImageName(handle, 0, &buf[0], &size)
	if err != nil {
		return ""
	}

	return windows.UTF16ToString(buf[:size])
}
