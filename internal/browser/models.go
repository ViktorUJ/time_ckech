package browser

// BrowserActivity — информация об активности в браузере.
type BrowserActivity struct {
	Browser   string `json:"browser"`    // "chrome", "edge", "firefox"
	PID       uint32 `json:"pid"`
	URL       string `json:"url"`
	Domain    string `json:"domain"`
	IsAllowed bool   `json:"is_allowed"`
	TabID     string `json:"tab_id"`
}
