package browser

import (
	"context"
	"errors"
	"testing"

	"parental-control-service/internal/config"
)

// mockUIAutomation is a test double for the UIAutomation interface.
type mockUIAutomation struct {
	urls     map[string]string // key: "browser-pid"
	redirect []redirectCall
	errGet   error
}

type redirectCall struct {
	browser string
	pid     uint32
	url     string
}

func (m *mockUIAutomation) GetBrowserURL(browser string, pid uint32) (string, error) {
	if m.errGet != nil {
		return "", m.errGet
	}
	key := browser + "-" + uintToStr(pid)
	return m.urls[key], nil
}

func (m *mockUIAutomation) RedirectBrowserTab(browser string, pid uint32, url string) error {
	m.redirect = append(m.redirect, redirectCall{browser, pid, url})
	return nil
}

func uintToStr(n uint32) string {
	if n == 0 {
		return "0"
	}
	buf := [10]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestBrowserMonitor_Scan(t *testing.T) {
	allowed := []config.AllowedSite{
		{Domain: "google.com", IncludeSubdomains: true},
	}

	ua := &mockUIAutomation{
		urls: map[string]string{
			"chrome-100": "https://mail.google.com/inbox",
			"edge-200":   "https://facebook.com/feed",
			"firefox-300": "",
		},
	}

	bm := NewBrowserMonitor(allowed, ua, "file:///blocked.html")

	procs := []BrowserProcess{
		{Browser: "chrome", PID: 100},
		{Browser: "edge", PID: 200},
		{Browser: "firefox", PID: 300},
	}

	activities, err := bm.Scan(context.Background(), procs)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}

	if len(activities) != 2 {
		t.Fatalf("expected 2 activities, got %d", len(activities))
	}

	// chrome → google.com subdomain → allowed
	if activities[0].Browser != "chrome" || !activities[0].IsAllowed {
		t.Errorf("chrome activity: got browser=%s allowed=%v", activities[0].Browser, activities[0].IsAllowed)
	}
	if activities[0].PID != 100 {
		t.Errorf("chrome PID: got %d, want 100", activities[0].PID)
	}

	// edge → facebook.com → not allowed
	if activities[1].Browser != "edge" || activities[1].IsAllowed {
		t.Errorf("edge activity: got browser=%s allowed=%v", activities[1].Browser, activities[1].IsAllowed)
	}
}

func TestBrowserMonitor_Scan_UIAutomationError(t *testing.T) {
	ua := &mockUIAutomation{errGet: errors.New("ui automation unavailable")}
	bm := NewBrowserMonitor(nil, ua, "")

	procs := []BrowserProcess{{Browser: "chrome", PID: 1}}
	activities, err := bm.Scan(context.Background(), procs)
	if err != nil {
		t.Fatalf("Scan should not return error on GetBrowserURL failure, got: %v", err)
	}
	if len(activities) != 0 {
		t.Errorf("expected 0 activities when UI Automation fails, got %d", len(activities))
	}
}

func TestBrowserMonitor_RedirectTab(t *testing.T) {
	ua := &mockUIAutomation{}
	bm := NewBrowserMonitor(nil, ua, "file:///blocked.html")

	activity := BrowserActivity{Browser: "edge", PID: 42}
	err := bm.RedirectTab(context.Background(), activity)
	if err != nil {
		t.Fatalf("RedirectTab returned error: %v", err)
	}

	if len(ua.redirect) != 1 {
		t.Fatalf("expected 1 redirect call, got %d", len(ua.redirect))
	}
	r := ua.redirect[0]
	if r.browser != "edge" || r.pid != 42 || r.url != "file:///blocked.html" {
		t.Errorf("redirect call = %+v, want browser=edge pid=42 url=file:///blocked.html", r)
	}
}
