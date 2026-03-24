// Package monitor handles process enumeration and classification.
package monitor

import "context"

// ProcessMonitor сканирует запущенные процессы и классифицирует их.
type ProcessMonitor struct {
	enumerator ProcessEnumerator
	classifier ProcessClassifier
}

// NewProcessMonitor создаёт новый ProcessMonitor.
func NewProcessMonitor(enumerator ProcessEnumerator, classifier ProcessClassifier) *ProcessMonitor {
	return &ProcessMonitor{
		enumerator: enumerator,
		classifier: classifier,
	}
}

// SetClassifier заменяет текущий классификатор.
func (pm *ProcessMonitor) SetClassifier(c ProcessClassifier) {
	pm.classifier = c
}

// Scan перечисляет запущенные процессы и возвращает только пользовательские
// (не системные) процессы с результатом классификации.
func (pm *ProcessMonitor) Scan(ctx context.Context) ([]ProcessInfo, error) {
	rawProcesses, err := pm.enumerator.EnumerateProcesses()
	if err != nil {
		return nil, err
	}

	var userProcesses []ProcessInfo
	for _, rp := range rawProcesses {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		info := pm.classifier.Classify(rp.PID, rp.Name, rp.ExePath)
		if info.IsSystem {
			continue
		}
		userProcesses = append(userProcesses, info)
	}

	return userProcesses, nil
}
