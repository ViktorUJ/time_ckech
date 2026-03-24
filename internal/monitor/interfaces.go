package monitor

// ProcessEnumerator перечисляет запущенные процессы ОС.
type ProcessEnumerator interface {
	EnumerateProcesses() ([]RawProcess, error)
}

// ProcessClassifier классифицирует процесс как системный, разрешённый или неразрешённый.
type ProcessClassifier interface {
	Classify(pid uint32, name string, exePath string) ProcessInfo
}

// SignatureChecker проверяет цифровую подпись исполняемого файла.
type SignatureChecker interface {
	IsMicrosoftSigned(exePath string) bool
}
