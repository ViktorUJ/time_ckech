package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Tracker собирает статистику использования программ и сайтов.
type Tracker struct {
	mu       sync.Mutex
	dataDir  string
	today    string            // "2026-03-22"
	usage    map[string]*entry // key = "app:name" или "site:domain"
}

type entry struct {
	Name         string
	Type         string // "app" или "site"
	IsRestricted bool
	Seconds      int
}

// NewTracker создаёт трекер, сохраняющий данные в dataDir/stats/.
// При старте загружает данные текущего дня с диска.
func NewTracker(dataDir string) *Tracker {
	t := &Tracker{
		dataDir: filepath.Join(dataDir, "stats"),
		today:   time.Now().Format("2006-01-02"),
		usage:   make(map[string]*entry),
	}
	// Загружаем данные текущего дня с диска (если есть).
	if existing := t.loadDayLocked(t.today); existing != nil {
		t.usage = existing
	}
	return t
}

// RecordApp записывает использование приложения за elapsed секунд.
func (t *Tracker) RecordApp(name string, isRestricted bool, elapsedSeconds int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.checkDayRollover()

	key := "app:" + name
	e, ok := t.usage[key]
	if !ok {
		e = &entry{Name: name, Type: "app", IsRestricted: isRestricted}
		t.usage[key] = e
	}
	e.Seconds += elapsedSeconds
}

// RecordSite записывает использование сайта за elapsed секунд.
func (t *Tracker) RecordSite(domain string, browser string, isRestricted bool, elapsedSeconds int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.checkDayRollover()

	key := "site:" + domain
	e, ok := t.usage[key]
	if !ok {
		e = &entry{Name: fmt.Sprintf("%s (%s)", domain, browser), Type: "site", IsRestricted: isRestricted}
		t.usage[key] = e
	}
	e.Seconds += elapsedSeconds
}

// checkDayRollover проверяет смену дня и сохраняет предыдущий день.
func (t *Tracker) checkDayRollover() {
	now := time.Now().Format("2006-01-02")
	if now != t.today {
		t.saveDayLocked(t.today)
		t.today = now
		t.usage = make(map[string]*entry)
	}
}

// Flush сохраняет текущий день на диск.
func (t *Tracker) Flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.saveDayLocked(t.today)
}

func (t *Tracker) saveDayLocked(date string) {
	if len(t.usage) == 0 {
		return
	}

	day := buildDayStats(date, t.usage)

	os.MkdirAll(t.dataDir, 0o700)
	path := filepath.Join(t.dataDir, date+".json")
	data, _ := json.MarshalIndent(day, "", "  ")
	os.WriteFile(path, data, 0o600)
}

func (t *Tracker) loadDayLocked(date string) map[string]*entry {
	path := filepath.Join(t.dataDir, date+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var day DayStats
	if err := json.Unmarshal(data, &day); err != nil {
		return nil
	}

	result := make(map[string]*entry, len(day.Apps))
	for _, a := range day.Apps {
		key := a.Type + ":" + a.Name
		result[key] = &entry{
			Name:         a.Name,
			Type:         a.Type,
			IsRestricted: a.IsRestricted,
			Seconds:      a.TotalSeconds,
		}
	}
	return result
}

func mergeUsage(existing, current map[string]*entry) map[string]*entry {
	if existing == nil {
		return current
	}
	for k, v := range current {
		if e, ok := existing[k]; ok {
			e.Seconds += v.Seconds
		} else {
			existing[k] = v
		}
	}
	return existing
}

func buildDayStats(date string, usage map[string]*entry) DayStats {
	day := DayStats{Date: date}
	for _, e := range usage {
		day.Apps = append(day.Apps, AppUsage{
			Name:         e.Name,
			Type:         e.Type,
			IsRestricted: e.IsRestricted,
			TotalSeconds: e.Seconds,
		})
		if e.IsRestricted {
			day.EntertainmentSeconds += e.Seconds
		}
	}
	sort.Slice(day.Apps, func(i, j int) bool {
		return day.Apps[i].TotalSeconds > day.Apps[j].TotalSeconds
	})
	return day
}

// GetDayStats возвращает статистику за указанный день.
func (t *Tracker) GetDayStats(date string) DayStats {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Если запрашивают сегодня — берём из памяти (уже включает данные с диска).
	if date == t.today {
		return buildDayStats(date, t.usage)
	}

	// Прошлый день — читаем с диска.
	path := filepath.Join(t.dataDir, date+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return DayStats{Date: date}
	}
	var day DayStats
	json.Unmarshal(data, &day)
	return day
}

// GetWeekStats возвращает статистику за последние 7 дней.
func (t *Tracker) GetWeekStats() WeekStats {
	now := time.Now()
	var days []DayStats
	for i := 6; i >= 0; i-- {
		date := now.AddDate(0, 0, -i).Format("2006-01-02")
		days = append(days, t.GetDayStats(date))
	}
	return WeekStats{Days: days}
}
