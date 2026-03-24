package enforcer

// ProcessKiller abstracts process termination for testability.
type ProcessKiller interface {
	// GracefulKill sends a graceful shutdown signal to the process.
	GracefulKill(pid uint32) error
	// ForceKill forcefully terminates the process.
	ForceKill(pid uint32) error
}

// Notifier abstracts system notification display for testability.
type Notifier interface {
	// ShowNotification displays a system notification with the given title and message.
	ShowNotification(title, message string) error
}
