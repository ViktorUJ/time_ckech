package logger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	defaultMaxSize  int64 = 50 * 1024 * 1024 // 50 MB
	defaultMaxFiles       = 5
	rotationWeekDays      = 7
)

// FullLogWriter writes LogEntry records as JSON Lines with size-based
// and weekly time-based rotation.
type FullLogWriter struct {
	filePath    string
	maxSize     int64
	maxFiles    int
	mu          sync.Mutex
	file        *os.File
	currentSize int64
	weekStart   time.Time // start of the current rotation week
}

// NewFullLogWriter creates a FullLogWriter that writes to filePath.
// The file is created (or opened for append) immediately.
func NewFullLogWriter(filePath string) (*FullLogWriter, error) {
	w := &FullLogWriter{
		filePath:  filePath,
		maxSize:   defaultMaxSize,
		maxFiles:  defaultMaxFiles,
		weekStart: weekStartTime(time.Now()),
	}
	if err := w.openOrCreate(); err != nil {
		return nil, err
	}
	return w, nil
}

// Write marshals entry to JSON, appends a newline, and writes to the log file.
// Rotation is triggered when the file exceeds maxSize or a new week has started.
func (w *FullLogWriter) Write(entry LogEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal log entry: %w", err)
	}
	data = append(data, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	// Check weekly rotation.
	now := time.Now()
	currentWeek := weekStartTime(now)
	if currentWeek.After(w.weekStart) {
		if err := w.rotate(); err != nil {
			return fmt.Errorf("weekly rotate log: %w", err)
		}
		w.weekStart = currentWeek
	}

	// Check size-based rotation.
	if w.currentSize+int64(len(data)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return fmt.Errorf("rotate log: %w", err)
		}
	}

	n, err := w.file.Write(data)
	if err != nil {
		return fmt.Errorf("write log entry: %w", err)
	}
	w.currentSize += int64(n)
	return nil
}

// Close closes the underlying file.
func (w *FullLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

// openOrCreate opens the log file for appending (or creates it) and records its size.
func (w *FullLogWriter) openOrCreate() error {
	f, err := os.OpenFile(w.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	w.file = f
	w.currentSize = info.Size()
	return nil
}

// rotate closes the current file and renames it with a date suffix.
// Old files beyond maxFiles are removed.
func (w *FullLogWriter) rotate() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}

	// Rename current file with date suffix: full.log -> full.2026-03-22.log
	dir := filepath.Dir(w.filePath)
	ext := filepath.Ext(w.filePath)
	base := w.filePath[:len(w.filePath)-len(ext)]
	dateSuffix := time.Now().Format("2006-01-02_150405")
	rotatedName := fmt.Sprintf("%s.%s%s", base, dateSuffix, ext)
	os.Rename(w.filePath, rotatedName)

	// Clean up old rotated files beyond maxFiles.
	w.cleanOldFiles(dir, base, ext)

	// Create a fresh file.
	return w.openOrCreate()
}

// cleanOldFiles removes the oldest rotated log files if there are more than maxFiles.
func (w *FullLogWriter) cleanOldFiles(dir, base, ext string) {
	pattern := fmt.Sprintf("%s.*%s", filepath.Base(base), ext)
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil || len(matches) <= w.maxFiles {
		return
	}

	// Files are sorted lexicographically; oldest dates come first.
	// Remove the oldest ones.
	excess := len(matches) - w.maxFiles
	for i := 0; i < excess; i++ {
		os.Remove(matches[i])
	}
}

// weekStartTime returns the Monday 00:00:00 of the week containing t.
func weekStartTime(t time.Time) time.Time {
	weekday := t.Weekday()
	if weekday == time.Sunday {
		weekday = 7
	}
	daysBack := int(weekday) - int(time.Monday)
	monday := t.AddDate(0, 0, -daysBack)
	return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, t.Location())
}

// ReadEntries читает записи из лог-файла с фильтрацией по дате.
// Если date пустой — возвращает все записи. Формат date: "2006-01-02".
// Также проверяет ротированные файлы за указанную дату.
func (w *FullLogWriter) ReadEntries(date string, limit int) []LogEntry {
	w.mu.Lock()
	// Flush current file.
	if w.file != nil {
		w.file.Sync()
	}
	w.mu.Unlock()

	var entries []LogEntry

	// Собираем файлы для чтения: текущий + ротированные.
	files := []string{w.filePath}
	dir := filepath.Dir(w.filePath)
	ext := filepath.Ext(w.filePath)
	base := w.filePath[:len(w.filePath)-len(ext)]
	pattern := fmt.Sprintf("%s.*%s", filepath.Base(base), ext)
	if matches, err := filepath.Glob(filepath.Join(dir, pattern)); err == nil {
		files = append(files, matches...)
	}

	for _, f := range files {
		fileEntries := readLogFile(f, date, limit-len(entries))
		entries = append(entries, fileEntries...)
		if limit > 0 && len(entries) >= limit {
			break
		}
	}

	return entries
}

// readLogFile читает JSON Lines из файла с фильтрацией по дате.
func readLogFile(path string, date string, maxEntries int) []LogEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var entries []LogEntry
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var e LogEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if date != "" {
			entryDate := e.Timestamp.Format("2006-01-02")
			if entryDate != date {
				continue
			}
		}
		entries = append(entries, e)
		if maxEntries > 0 && len(entries) >= maxEntries {
			break
		}
	}
	return entries
}

// splitLines разбивает байты на строки.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := data[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
