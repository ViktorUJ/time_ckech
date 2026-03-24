package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// GitHubURLs holds raw GitHub URLs for configuration files.
// ConfigURL is the preferred single-file URL. If set, AppsURL/SitesURL/ScheduleURL are ignored.
type GitHubURLs struct {
	ConfigURL   string // single combined config.json URL (preferred)
	AppsURL     string // legacy: separate allowed_apps.json
	SitesURL    string // legacy: separate allowed_sites.json
	ScheduleURL string // legacy: separate schedule.json
}

// ConfigManager loads, caches, and serves configuration from GitHub.
type ConfigManager struct {
	githubURLs   GitHubURLs
	httpClient   HTTPClient
	current      *Config
	lastModified map[string]string // URL -> Last-Modified or ETag header value
	mu           sync.RWMutex
	failClosed   bool
	cacheDir     string
}

// NewConfigManager creates a new ConfigManager.
// cacheDir defaults to C:\ProgramData\ParentalControlService\config\ if empty.
func NewConfigManager(urls GitHubURLs, client HTTPClient, cacheDir string) *ConfigManager {
	if cacheDir == "" {
		cacheDir = `C:\ProgramData\ParentalControlService\config`
	}
	return &ConfigManager{
		githubURLs:   urls,
		httpClient:   client,
		lastModified: make(map[string]string),
		failClosed:   true,
		cacheDir:     cacheDir,
	}
}

// Load fetches the three configuration files from GitHub, parses JSON,
// and updates the current config atomically. It uses If-Modified-Since
// headers to avoid redundant downloads. On success the config is also
// persisted to disk. On failure the last successful config is retained.
func (cm *ConfigManager) Load(ctx context.Context) (*Config, error) {
	// Prefer single combined config URL.
	if cm.githubURLs.ConfigURL != "" {
		return cm.loadSingleConfig(ctx)
	}
	return cm.loadSeparateConfigs(ctx)
}

// loadSingleConfig fetches the combined config.json from a single URL.
func (cm *ConfigManager) loadSingleConfig(ctx context.Context) (*Config, error) {
	cfg, changed, err := fetchJSON[Config](ctx, cm, cm.githubURLs.ConfigURL)
	if err != nil {
		log.Printf("[config] error loading config: %v", err)
		return cm.fallbackOrFail("config", err)
	}

	if !changed {
		cm.mu.RLock()
		c := cm.current
		cm.mu.RUnlock()
		if c != nil {
			return c, nil
		}
	}

	if cfg == nil {
		cm.mu.RLock()
		c := cm.current
		cm.mu.RUnlock()
		if c != nil {
			return c, nil
		}
		return nil, fmt.Errorf("config unavailable")
	}

	cm.mu.Lock()
	cm.current = cfg
	cm.failClosed = false
	if err := cm.saveCacheToDisk(); err != nil {
		log.Printf("[config] warning: failed to save cache to disk: %v", err)
	}
	cm.mu.Unlock()

	return cfg, nil
}

// loadSeparateConfigs fetches three separate config files (legacy mode).
func (cm *ConfigManager) loadSeparateConfigs(ctx context.Context) (*Config, error) {
	apps, appsChanged, err := fetchJSON[AllowedAppsConfig](ctx, cm, cm.githubURLs.AppsURL)
	if err != nil {
		log.Printf("[config] error loading allowed apps: %v", err)
		return cm.fallbackOrFail("allowed apps", err)
	}

	sites, sitesChanged, err := fetchJSON[AllowedSitesConfig](ctx, cm, cm.githubURLs.SitesURL)
	if err != nil {
		log.Printf("[config] error loading allowed sites: %v", err)
		return cm.fallbackOrFail("allowed sites", err)
	}

	schedule, scheduleChanged, err := fetchJSON[ScheduleConfig](ctx, cm, cm.githubURLs.ScheduleURL)
	if err != nil {
		log.Printf("[config] error loading schedule: %v", err)
		return cm.fallbackOrFail("schedule", err)
	}

	// If nothing changed and we already have a config, return current.
	if !appsChanged && !sitesChanged && !scheduleChanged {
		cm.mu.RLock()
		cfg := cm.current
		cm.mu.RUnlock()
		if cfg != nil {
			return cfg, nil
		}
	}

	// Build new config, merging unchanged parts from current.
	cm.mu.Lock()
	defer cm.mu.Unlock()

	newCfg := &Config{}
	if apps != nil {
		newCfg.AllowedApps = *apps
	} else if cm.current != nil {
		newCfg.AllowedApps = cm.current.AllowedApps
	}
	if sites != nil {
		newCfg.AllowedSites = *sites
	} else if cm.current != nil {
		newCfg.AllowedSites = cm.current.AllowedSites
	}
	if schedule != nil {
		newCfg.Schedule = *schedule
	} else if cm.current != nil {
		newCfg.Schedule = cm.current.Schedule
	}

	cm.current = newCfg
	cm.failClosed = false

	// Best-effort disk cache.
	if err := cm.saveCacheToDisk(); err != nil {
		log.Printf("[config] warning: failed to save cache to disk: %v", err)
	}

	return newCfg, nil
}

// Current returns the current configuration in a thread-safe manner.
func (cm *ConfigManager) Current() *Config {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.current
}

// SetConfigURL updates the config URL at runtime.
func (cm *ConfigManager) SetConfigURL(url string) {
	cm.mu.Lock()
	cm.githubURLs.ConfigURL = url
	// Reset lastModified so next Load fetches fresh.
	delete(cm.lastModified, url)
	cm.mu.Unlock()
}

// IsFailClosed returns true when no configuration has been loaded yet
// (neither from GitHub nor from disk cache). In this state the service
// should block everything except system processes.
func (cm *ConfigManager) IsFailClosed() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.failClosed
}

// LoadCacheFromDisk attempts to restore configuration from the on-disk
// cache. This is called at startup before the first GitHub fetch.
func (cm *ConfigManager) LoadCacheFromDisk() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.loadCacheFromDisk()
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// ErrUsedCache indicates that a network error occurred but cached config was used.
var ErrUsedCache = fmt.Errorf("used cached config")

// fallbackOrFail returns the current config if one exists, otherwise
// returns the error. This implements the "keep last good config" and
// "fail-closed on first start" behaviours.
// When a cached config is returned after a failure, ErrUsedCache is returned
// so callers can distinguish "no changes" from "network error + cache".
func (cm *ConfigManager) fallbackOrFail(component string, origErr error) (*Config, error) {
	cm.mu.RLock()
	cfg := cm.current
	cm.mu.RUnlock()
	if cfg != nil {
		log.Printf("[config] using cached config after %s load failure", component)
		return cfg, fmt.Errorf("%w: %s: %v", ErrUsedCache, component, origErr)
	}
	return nil, fmt.Errorf("config unavailable (%s): %w", component, origErr)
}

// fetchJSON fetches a single JSON file from url using conditional GET.
// Returns (parsed, changed, error). If the server responds 304 Not Modified,
// changed is false and parsed is nil.
func fetchJSON[T any](ctx context.Context, cm *ConfigManager, url string) (*T, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("create request: %w", err)
	}

	cm.mu.RLock()
	if lm, ok := cm.lastModified[url]; ok {
		// Google Drive не поддерживает conditional GET корректно,
		// поэтому не отправляем If-Modified-Since для drive.google.com.
		if !isGoogleDriveURL(url) {
			req.Header.Set("If-Modified-Since", lm)
		}
	}
	cm.mu.RUnlock()

	resp, err := cm.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, false, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("unexpected status %d for %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read body: %w", err)
	}

	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, false, fmt.Errorf("parse json from %s: %w", url, err)
	}

	// Store conditional-GET header for next request.
	cm.mu.Lock()
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		cm.lastModified[url] = lm
	} else if etag := resp.Header.Get("ETag"); etag != "" {
		cm.lastModified[url] = etag
	}
	cm.mu.Unlock()

	return &result, true, nil
}

// saveCacheToDisk writes the current config as a single config.json to cacheDir.
// Caller must hold cm.mu (at least RLock).
func (cm *ConfigManager) saveCacheToDisk() error {
	if cm.current == nil {
		return nil
	}

	if err := os.MkdirAll(cm.cacheDir, 0o700); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	combined, err := json.MarshalIndent(cm.current, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cm.cacheDir, "config.json"), combined, 0o600); err != nil {
		return fmt.Errorf("write config.json: %w", err)
	}

	// Remove legacy separate files if they exist.
	for _, old := range []string{"allowed_apps.json", "allowed_sites.json", "schedule.json"} {
		os.Remove(filepath.Join(cm.cacheDir, old))
	}

	return nil
}

// loadCacheFromDisk reads cached JSON files from cacheDir and populates
// cm.current. Tries combined config.json first, falls back to separate files.
// Caller must hold cm.mu.
func (cm *ConfigManager) loadCacheFromDisk() error {
	// Try combined config.json first.
	combinedPath := filepath.Join(cm.cacheDir, "config.json")
	if data, err := os.ReadFile(combinedPath); err == nil {
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err == nil {
			cm.current = &cfg
			cm.failClosed = false
			return nil
		}
	}

	// Fallback: load three separate files.
	appsPath := filepath.Join(cm.cacheDir, "allowed_apps.json")
	sitesPath := filepath.Join(cm.cacheDir, "allowed_sites.json")
	schedulePath := filepath.Join(cm.cacheDir, "schedule.json")

	appsData, err := os.ReadFile(appsPath)
	if err != nil {
		return fmt.Errorf("read apps cache: %w", err)
	}
	sitesData, err := os.ReadFile(sitesPath)
	if err != nil {
		return fmt.Errorf("read sites cache: %w", err)
	}
	scheduleData, err := os.ReadFile(schedulePath)
	if err != nil {
		return fmt.Errorf("read schedule cache: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(appsData, &cfg.AllowedApps); err != nil {
		return fmt.Errorf("parse apps cache: %w", err)
	}
	if err := json.Unmarshal(sitesData, &cfg.AllowedSites); err != nil {
		return fmt.Errorf("parse sites cache: %w", err)
	}
	if err := json.Unmarshal(scheduleData, &cfg.Schedule); err != nil {
		return fmt.Errorf("parse schedule cache: %w", err)
	}

	cm.current = &cfg
	cm.failClosed = false
	return nil
}

// isGoogleDriveURL returns true if the URL points to Google Drive.
func isGoogleDriveURL(url string) bool {
	return strings.Contains(url, "drive.google.com")
}
