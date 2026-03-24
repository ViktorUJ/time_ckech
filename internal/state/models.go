package state

import "time"

// ServiceState — персистентное состояние, сохраняемое на диск каждые 30 сек.
type ServiceState struct {
	EntertainmentSeconds int       `json:"entertainment_seconds"` // Накопленное развлекательное время
	WindowStart          time.Time `json:"window_start"`          // Начало текущего временного окна
	WindowEnd            time.Time `json:"window_end"`            // Конец текущего временного окна
	LastSaveTime         time.Time `json:"last_save_time"`        // Время последнего сохранения
	LastTickTime         time.Time `json:"last_tick_time"`        // Время последнего тика основного цикла
}
