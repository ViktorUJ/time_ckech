package browser

import (
	"context"
	"fmt"

	"parental-control-service/internal/config"
)

// BrowserProcess represents a running browser process discovered externally.
type BrowserProcess struct {
	Browser string // "chrome", "edge", "firefox"
	PID     uint32
}

// BrowserMonitor monitors active URLs in supported browsers and classifies
// them against the allowed sites list.
type BrowserMonitor struct {
	allowedSites   []config.AllowedSite
	uiAutomation   UIAutomation
	blockedPageURL string
}

// NewBrowserMonitor creates a new BrowserMonitor.
func NewBrowserMonitor(
	allowedSites []config.AllowedSite,
	uiAutomation UIAutomation,
	blockedPageURL string,
) *BrowserMonitor {
	return &BrowserMonitor{
		allowedSites:   allowedSites,
		uiAutomation:   uiAutomation,
		blockedPageURL: blockedPageURL,
	}
}

// Scan retrieves the active URL from each supplied browser process and
// classifies it against the allowed sites list.
func (bm *BrowserMonitor) Scan(_ context.Context, processes []BrowserProcess) ([]BrowserActivity, error) {
	var activities []BrowserActivity

	for _, proc := range processes {
		rawURL, err := bm.uiAutomation.GetBrowserURL(proc.Browser, proc.PID)
		if err != nil {
			// Skip browsers where we cannot read the URL (e.g. minimised).
			continue
		}
		if rawURL == "" {
			continue
		}

		domain := ExtractDomain(rawURL)
		allowed := IsURLAllowed(rawURL, bm.allowedSites)

		activities = append(activities, BrowserActivity{
			Browser:   proc.Browser,
			PID:       proc.PID,
			URL:       rawURL,
			Domain:    domain,
			IsAllowed: allowed,
			TabID:     fmt.Sprintf("%s-%d", proc.Browser, proc.PID),
		})
	}

	return activities, nil
}

// RedirectTab navigates the browser tab described by activity to the blocked
// page URL via the UI Automation interface.
func (bm *BrowserMonitor) RedirectTab(_ context.Context, activity BrowserActivity) error {
	return bm.uiAutomation.RedirectBrowserTab(activity.Browser, activity.PID, bm.blockedPageURL)
}
