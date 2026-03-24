package browser

// UIAutomation abstracts the Windows UI Automation API for browser URL extraction.
type UIAutomation interface {
	// GetBrowserURL retrieves the current URL from the browser's address bar.
	GetBrowserURL(browserName string, pid uint32) (string, error)

	// RedirectBrowserTab navigates the browser tab to the specified URL.
	RedirectBrowserTab(browserName string, pid uint32, url string) error
}
