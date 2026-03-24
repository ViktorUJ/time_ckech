package logger

// EventLogWriter abstracts the Windows Event Log for testability.
type EventLogWriter interface {
	Info(eventID uint32, msg string) error
	Warning(eventID uint32, msg string) error
	Error(eventID uint32, msg string) error
	Close() error
}
