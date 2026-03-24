package monitor

// ProcessClassification — результат классификации процесса.
type ProcessClassification int

const (
	ProcessSystem     ProcessClassification = iota // Системный процесс Windows
	ProcessAllowed                                 // Разрешённая программа
	ProcessRestricted                              // Неразрешённая программа
)

// ProcessInfo — информация о процессе с результатом классификации.
type ProcessInfo struct {
	PID       uint32 `json:"pid"`
	Name      string `json:"name"`
	ExePath   string `json:"exe_path"`
	IsSystem  bool   `json:"is_system"`
	IsAllowed bool   `json:"is_allowed"`
}

// RawProcess — необработанные данные о процессе от ОС.
type RawProcess struct {
	PID     uint32 `json:"pid"`
	Name    string `json:"name"`
	ExePath string `json:"exe_path"`
}
