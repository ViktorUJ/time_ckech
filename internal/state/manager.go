package state

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const defaultStatePath = `C:\ProgramData\ParentalControlService\state.json`

// StateManager handles persisting and restoring ServiceState to/from disk.
type StateManager struct {
	filePath string
	mu       sync.Mutex
}

// NewStateManager creates a StateManager. If filePath is empty, the default
// path (C:\ProgramData\ParentalControlService\state.json) is used.
func NewStateManager(filePath string) *StateManager {
	if filePath == "" {
		filePath = defaultStatePath
	}
	return &StateManager{filePath: filePath}
}

// Save atomically writes the given state to disk as JSON.
// It sets LastSaveTime to the current time before writing.
func (sm *StateManager) Save(state *ServiceState) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	state.LastSaveTime = time.Now()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal error: %w", err)
	}

	dir := filepath.Dir(sm.filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("state: create directory: %w", err)
	}

	// Atomic write: write to temp file, then rename.
	tmpPath := sm.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("state: write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, sm.filePath); err != nil {
		// Clean up temp file on rename failure.
		_ = os.Remove(tmpPath)
		return fmt.Errorf("state: rename temp file: %w", err)
	}

	return nil
}

// Load reads the service state from disk.
// If the file does not exist, it returns a zero-value state (no error).
// If the file is corrupted (invalid JSON), it returns a zero-value state and logs a warning.
func (sm *StateManager) Load() (*ServiceState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	data, err := os.ReadFile(sm.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &ServiceState{}, nil
		}
		return nil, fmt.Errorf("state: read file: %w", err)
	}

	var s ServiceState
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("[WARN] state: corrupted state file %s, starting fresh: %v", sm.filePath, err)
		return &ServiceState{}, nil
	}

	return &s, nil
}

// Restore loads the persisted state and decides whether to keep the saved
// entertainment counter or reset it to zero.
//
// If now falls within the saved window (WindowStart <= now < WindowEnd),
// the counter is preserved. Otherwise the counter is reset to zero.
func (sm *StateManager) Restore(now time.Time) *ServiceState {
	loaded, err := sm.Load()
	if err != nil {
		log.Printf("[WARN] state: could not load state, starting fresh: %v", err)
		return &ServiceState{}
	}

	// Проверяем что state сохранён сегодня.
	if loaded.LastSaveTime.IsZero() || loaded.LastSaveTime.Format("2006-01-02") != now.Format("2006-01-02") {
		// Другой день — сбрасываем счётчики, но сохраняем режим и бонус.
		log.Printf("[state] new day detected, resetting entertainment/computer counters")
		return &ServiceState{
			ServiceMode: loaded.ServiceMode,
			ModeUntil:   loaded.ModeUntil,
		}
	}

	// Тот же день — возвращаем всё.
	return loaded
}
