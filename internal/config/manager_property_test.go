package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

// Feature: parental-control-service, Property 12: Откат конфигурации при ошибке загрузки
// **Validates: Requirements 2.4**

// mockHTTPClient is a configurable HTTP client for testing.
// When shouldFail is true, Do returns an error. Otherwise it returns
// valid JSON responses built from the provided Config.
type mockHTTPClient struct {
	shouldFail bool
	config     *Config
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if m.shouldFail {
		return nil, fmt.Errorf("simulated network error")
	}

	var body []byte
	var err error

	url := req.URL.String()
	switch {
	case url == "http://test/apps.json":
		body, err = json.Marshal(m.config.AllowedApps)
	case url == "http://test/sites.json":
		body, err = json.Marshal(m.config.AllowedSites)
	case url == "http://test/schedule.json":
		body, err = json.Marshal(m.config.Schedule)
	default:
		return nil, fmt.Errorf("unknown URL: %s", url)
	}
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}, nil
}

// genAllowedApp generates a random AllowedApp.
func genAllowedApp() *rapid.Generator[AllowedApp] {
	return rapid.Custom(func(t *rapid.T) AllowedApp {
		return AllowedApp{
			Name:       rapid.StringMatching(`[a-zA-Z]{1,10}`).Draw(t, "appName"),
			Executable: rapid.StringMatching(`[a-z]{1,8}\.exe`).Draw(t, "executable"),
			Path:       rapid.StringMatching(`C:\\[a-zA-Z]{1,5}\\[a-z]{1,8}\.exe`).Draw(t, "path"),
		}
	})
}

// genAllowedSite generates a random AllowedSite.
func genAllowedSite() *rapid.Generator[AllowedSite] {
	return rapid.Custom(func(t *rapid.T) AllowedSite {
		return AllowedSite{
			Domain:            rapid.StringMatching(`[a-z]{2,8}\.(com|org|net)`).Draw(t, "domain"),
			IncludeSubdomains: rapid.Bool().Draw(t, "includeSubdomains"),
		}
	})
}

// genConfig generates a random valid Config.
func genConfig() *rapid.Generator[Config] {
	return rapid.Custom(func(t *rapid.T) Config {
		numApps := rapid.IntRange(0, 5).Draw(t, "numApps")
		apps := make([]AllowedApp, numApps)
		for i := range apps {
			apps[i] = genAllowedApp().Draw(t, fmt.Sprintf("app_%d", i))
		}

		numSites := rapid.IntRange(0, 5).Draw(t, "numSites")
		sites := make([]AllowedSite, numSites)
		for i := range sites {
			sites[i] = genAllowedSite().Draw(t, fmt.Sprintf("site_%d", i))
		}

		schedule := genScheduleConfig().Draw(t, "schedule")

		return Config{
			AllowedApps:  AllowedAppsConfig{Apps: apps},
			AllowedSites: AllowedSitesConfig{Sites: sites},
			Schedule:     schedule,
		}
	})
}

// loadOp represents a single load operation in a sequence.
// If Fail is true, the HTTP client will return an error.
// Otherwise, Cfg is the config to serve.
type loadOp struct {
	Fail bool
	Cfg  Config
}

// genLoadOps generates a sequence of load operations where at least
// the first operation succeeds (so there is always a "last successful" config).
func genLoadOps() *rapid.Generator[[]loadOp] {
	return rapid.Custom(func(t *rapid.T) []loadOp {
		// First op always succeeds to establish a baseline config.
		first := loadOp{
			Fail: false,
			Cfg:  genConfig().Draw(t, "firstCfg"),
		}

		n := rapid.IntRange(1, 20).Draw(t, "numOps")
		ops := make([]loadOp, 1, 1+n)
		ops[0] = first

		for i := 0; i < n; i++ {
			fail := rapid.Bool().Draw(t, fmt.Sprintf("fail_%d", i))
			cfg := genConfig().Draw(t, fmt.Sprintf("cfg_%d", i))
			ops = append(ops, loadOp{Fail: fail, Cfg: cfg})
		}
		return ops
	})
}

func TestPropertyConfigRollbackOnError(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ops := genLoadOps().Draw(t, "ops")

		cacheDir, err := os.MkdirTemp("", "config-rollback-test-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(cacheDir)

		urls := GitHubURLs{
			AppsURL:     "http://test/apps.json",
			SitesURL:    "http://test/sites.json",
			ScheduleURL: "http://test/schedule.json",
		}

		mock := &mockHTTPClient{}
		cm := NewConfigManager(urls, mock, cacheDir)

		var lastSuccessful *Config

		for i, op := range ops {
			mock.shouldFail = op.Fail
			mock.config = &op.Cfg

			// Clear lastModified so each load does a full fetch
			// (avoids 304 Not Modified short-circuit).
			cm.mu.Lock()
			cm.lastModified = make(map[string]string)
			cm.mu.Unlock()

			_, err := cm.Load(context.Background())

			if !op.Fail {
				// Successful load — update our tracking variable.
				if err != nil {
					t.Fatalf("op %d: expected success but got error: %v", i, err)
				}
				snapshot := op.Cfg // copy
				lastSuccessful = &snapshot
			}

			// After every operation (success or failure), Current()
			// must equal the last successfully loaded config.
			current := cm.Current()

			if lastSuccessful == nil {
				// Should not happen because first op always succeeds.
				t.Fatalf("op %d: lastSuccessful is nil", i)
			}

			if !reflect.DeepEqual(*current, *lastSuccessful) {
				t.Fatalf("op %d (fail=%v): Current() differs from last successful config\ncurrent:  %+v\nexpected: %+v",
					i, op.Fail, *current, *lastSuccessful)
			}
		}
	})
}

// Feature: parental-control-service, Property 13: Горячая перезагрузка конфигурации
// **Validates: Requirements 2.3**

func TestPropertyConfigHotReload(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfgA := genConfig().Draw(t, "configA")
		cfgB := genConfig().Draw(t, "configB")

		cacheDir, err := os.MkdirTemp("", "config-hotreload-test-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(cacheDir)

		urls := GitHubURLs{
			AppsURL:     "http://test/apps.json",
			SitesURL:    "http://test/sites.json",
			ScheduleURL: "http://test/schedule.json",
		}

		mock := &mockHTTPClient{config: &cfgA}
		cm := NewConfigManager(urls, mock, cacheDir)

		// Load config A.
		_, err = cm.Load(context.Background())
		if err != nil {
			t.Fatalf("failed to load config A: %v", err)
		}

		currentA := cm.Current()
		if currentA == nil {
			t.Fatal("Current() is nil after loading config A")
		}
		if !reflect.DeepEqual(*currentA, cfgA) {
			t.Fatalf("after loading A, Current() != A\ncurrent: %+v\nexpected: %+v", *currentA, cfgA)
		}

		// Switch mock to serve config B and clear lastModified to avoid 304.
		mock.config = &cfgB
		cm.mu.Lock()
		cm.lastModified = make(map[string]string)
		cm.mu.Unlock()

		// Load config B (hot reload, no restart).
		_, err = cm.Load(context.Background())
		if err != nil {
			t.Fatalf("failed to load config B: %v", err)
		}

		currentB := cm.Current()
		if currentB == nil {
			t.Fatal("Current() is nil after loading config B")
		}
		if !reflect.DeepEqual(*currentB, cfgB) {
			t.Fatalf("after hot reload, Current() != B\ncurrent: %+v\nexpected: %+v", *currentB, cfgB)
		}
	})
}
