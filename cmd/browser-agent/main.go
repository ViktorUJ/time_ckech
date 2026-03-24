//go:build windows

// browser-agent runs in the user session and monitors browser URLs.
// It reads the real URL from the address bar via UI Automation,
// sends it to the service for classification, and closes blocked windows.
// This process has no GUI and runs independently of the tray app.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const (
	scanInterval    = 10 * time.Second
	warnGracePeriod = 30 * time.Second
	defaultPort     = 8080
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	oleaut32 = syscall.NewLazyDLL("oleaut32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")

	pEnumWindows                = user32.NewProc("EnumWindows")
	pGetClassNameW              = user32.NewProc("GetClassNameW")
	pGetWindowTextLengthW       = user32.NewProc("GetWindowTextLengthW")
	pGetWindowTextW             = user32.NewProc("GetWindowTextW")
	pGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	pPostMessageW               = user32.NewProc("PostMessageW")
	pMessageBoxW                = user32.NewProc("MessageBoxW")
	pOpenProcess                = kernel32.NewProc("OpenProcess")
	pQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	pCloseHandle                = kernel32.NewProc("CloseHandle")
	pCreateMutexW               = kernel32.NewProc("CreateMutexW")
	pCoInitializeEx             = ole32.NewProc("CoInitializeEx")
	pCoUninitialize             = ole32.NewProc("CoUninitialize")
	pSysFreeString              = oleaut32.NewProc("SysFreeString")
)

const mutexName = "Global\\ParentalControl_BrowserAgent_SingleInstance"

func main() {
	// Single instance check.
	name, _ := syscall.UTF16PtrFromString(mutexName)
	h, _, err := pCreateMutexW.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if h != 0 && err == syscall.ERROR_ALREADY_EXISTS {
		os.Exit(0) // already running
	}

	port := readPort()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 5 * time.Second}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pCoInitializeEx.Call(0, 0x2) // COINIT_APARTMENTTHREADED
	defer pCoUninitialize.Call()

	warned := make(map[uintptr]time.Time)

	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()

	for range ticker.C {
		urls := scanBrowserWindows()
		if len(urls) == 0 {
			// Очищаем warned если нет окон.
			warned = make(map[uintptr]time.Time)
			continue
		}
		processResponse(client, baseURL, urls, warned)
	}
}

func readPort() int {
	if key, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\ParentalControlService`, registry.QUERY_VALUE); err == nil {
		defer key.Close()
		if val, _, err := key.GetIntegerValue("HTTPPort"); err == nil && val > 0 {
			return int(val)
		}
	}
	// Fallback: read from settings.json.
	settingsPath := filepath.Join(os.Getenv("ProgramData"), "ParentalControlService", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var s struct{ HTTPPort int `json:"http_port"` }
		if json.Unmarshal(data, &s) == nil && s.HTTPPort > 0 {
			return s.HTTPPort
		}
	}
	return defaultPort
}

// --- HTTP communication with service ---

type browserURLReport struct {
	URLs []browserURLEntry `json:"urls"`
}

type browserURLEntry struct {
	Browser string  `json:"browser"`
	PID     uint32  `json:"pid"`
	URL     string  `json:"url"`
	HWND    uintptr `json:"hwnd"`
}

type browserCloseResponse struct {
	CloseHWNDs []uintptr `json:"close_hwnds"`
	Reason     string    `json:"reason"`
}

func processResponse(client *http.Client, baseURL string, urls []browserURLEntry, warned map[uintptr]time.Time) {
	data, err := json.Marshal(browserURLReport{URLs: urls})
	if err != nil {
		return
	}
	resp, err := client.Post(baseURL+"/browser-activity", "application/json", bytes.NewReader(data))
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var closeResp browserCloseResponse
	if err := json.NewDecoder(resp.Body).Decode(&closeResp); err != nil {
		return
	}

	if len(closeResp.CloseHWNDs) == 0 {
		warned = clearWarned(warned, nil)
		return
	}

	now := time.Now()
	reason := closeResp.Reason
	if reason == "" {
		reason = "Access to this site is currently blocked"
	}

	activeSet := make(map[uintptr]bool, len(closeResp.CloseHWNDs))
	for _, h := range closeResp.CloseHWNDs {
		activeSet[h] = true
	}
	// Remove stale entries.
	for h := range warned {
		if !activeSet[h] {
			delete(warned, h)
		}
	}

	for _, hwnd := range closeResp.CloseHWNDs {
		warnedAt, wasWarned := warned[hwnd]

		if !wasWarned {
			warned[hwnd] = now
			go showNotification(reason)
			continue
		}

		if now.Sub(warnedAt) >= warnGracePeriod {
			pPostMessageW.Call(hwnd, 0x0010, 0, 0) // WM_CLOSE
			delete(warned, hwnd)
		}
	}
}

func clearWarned(warned map[uintptr]time.Time, keep map[uintptr]bool) map[uintptr]time.Time {
	if keep == nil {
		return make(map[uintptr]time.Time)
	}
	for h := range warned {
		if !keep[h] {
			delete(warned, h)
		}
	}
	return warned
}

func showNotification(text string) {
	lang := readLang()
	title := "Parental Control"
	warnFmt := "%s\n\nClose the tab within 30 seconds or the browser window will be closed."
	if lang == "ru" {
		title = "Родительский контроль"
		text = translateReason(text)
		warnFmt = "%s\n\nЗакройте вкладку в течение 30 секунд, иначе окно браузера будет закрыто."
	}
	msg := fmt.Sprintf(warnFmt, text)
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	msgPtr, _ := syscall.UTF16PtrFromString(msg)
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(msgPtr)), uintptr(unsafe.Pointer(titlePtr)), 0x00000040)
}

// translateReason переводит английские причины блокировки на русский.
func translateReason(reason string) string {
	translations := map[string]string{
		"Sleep time. Entertainment sites are blocked.":              "Время сна. Развлекательные сайты заблокированы.",
		"No entertainment window now. Site is blocked.":             "Сейчас нет окна развлечений. Сайт заблокирован.",
		"Entertainment time is over for today. Site is blocked.":    "Время развлечений на сегодня закончилось. Сайт заблокирован.",
		"Access to this site is currently blocked.":                 "Доступ к этому сайту сейчас заблокирован.",
		"Access to this site is currently blocked":                  "Доступ к этому сайту сейчас заблокирован.",
	}
	if v, ok := translations[reason]; ok {
		return v
	}
	return reason
}

func readLang() string {
	langPath := filepath.Join(os.Getenv("ProgramData"), "ParentalControlService", "lang.txt")
	if data, err := os.ReadFile(langPath); err == nil {
		l := strings.TrimSpace(string(data))
		if l == "ru" || l == "en" {
			return l
		}
	}
	return "ru"
}

// --- UI Automation: read real URL from browser address bar ---

var (
	clsidCUIAutomation = syscall.GUID{0xFF48DBA4, 0x60EF, 0x4201, [8]byte{0xAA, 0x87, 0x54, 0x10, 0x3E, 0xEF, 0x59, 0x4E}}
	iidIUIAutomation   = syscall.GUID{0x30CBE57D, 0xD9D0, 0x452A, [8]byte{0xAB, 0x13, 0x7A, 0xC5, 0xAC, 0x48, 0x25, 0xEE}}
)

const (
	uiaControlTypePropertyId = 30003
	uiaValueValuePropertyId  = 30045
	uiaControlTypeEdit       = 50004
	vtElementFromHandle      = 6
	vtCreatePropertyCondition = 23
	vtFindFirst              = 5
	vtGetCurrentPropertyValue = 10
	treeScopeDescendants     = 0x4
)

type comObj struct {
	ptr uintptr
}

func (c *comObj) release() {
	if c.ptr == 0 {
		return
	}
	vtbl := *(*uintptr)(unsafe.Pointer(c.ptr))
	releaseAddr := *(*uintptr)(unsafe.Pointer(vtbl + 2*unsafe.Sizeof(uintptr(0))))
	syscall.SyscallN(releaseAddr, c.ptr)
	c.ptr = 0
}

func (c *comObj) call(vtIndex int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(c.ptr))
	methodAddr := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(vtIndex)*unsafe.Sizeof(uintptr(0))))
	allArgs := append([]uintptr{c.ptr}, args...)
	ret, _, _ := syscall.SyscallN(methodAddr, allArgs...)
	return ret
}

func createUIAutomation() *comObj {
	var pAuto uintptr
	hr, _, _ := syscall.SyscallN(
		ole32.NewProc("CoCreateInstance").Addr(),
		uintptr(unsafe.Pointer(&clsidCUIAutomation)),
		0,
		0x1|0x4,
		uintptr(unsafe.Pointer(&iidIUIAutomation)),
		uintptr(unsafe.Pointer(&pAuto)),
	)
	if hr != 0 || pAuto == 0 {
		return nil
	}
	return &comObj{ptr: pAuto}
}

func getURLFromBrowserWindow(auto *comObj, hwnd uintptr) string {
	var pElem uintptr
	hr := auto.call(vtElementFromHandle, hwnd, uintptr(unsafe.Pointer(&pElem)))
	if hr != 0 || pElem == 0 {
		return ""
	}
	elem := &comObj{ptr: pElem}
	defer elem.release()

	// Condition: ControlType == Edit.
	var pCond uintptr
	var vtEdit [2]uint64
	vtEdit[0] = 3 // VT_I4
	vtEdit[1] = uiaControlTypeEdit
	hr = auto.call(vtCreatePropertyCondition,
		uintptr(uiaControlTypePropertyId),
		uintptr(unsafe.Pointer(&vtEdit[0])),
		uintptr(unsafe.Pointer(&pCond)))
	if hr != 0 || pCond == 0 {
		return ""
	}
	cond := &comObj{ptr: pCond}
	defer cond.release()

	// FindFirst.
	var pFound uintptr
	hr = elem.call(vtFindFirst, treeScopeDescendants, cond.ptr, uintptr(unsafe.Pointer(&pFound)))
	if hr != 0 || pFound == 0 {
		return ""
	}
	found := &comObj{ptr: pFound}
	defer found.release()

	// GetCurrentPropertyValue(ValueValue).
	var vtVal [4]uint64
	hr = found.call(vtGetCurrentPropertyValue,
		uintptr(uiaValueValuePropertyId),
		uintptr(unsafe.Pointer(&vtVal[0])))
	if hr != 0 {
		return ""
	}

	vt := uint16(vtVal[0])
	if vt != 8 { // VT_BSTR
		return ""
	}
	bstr := (*uint16)(unsafe.Pointer(uintptr(vtVal[1])))
	if bstr == nil {
		return ""
	}
	url := bstrToString(bstr)
	pSysFreeString.Call(uintptr(unsafe.Pointer(bstr)))
	return url
}

func bstrToString(bstr *uint16) string {
	if bstr == nil {
		return ""
	}
	lenPtr := (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(bstr)) - 4))
	byteLen := *lenPtr
	if byteLen == 0 {
		return ""
	}
	charLen := byteLen / 2
	slice := unsafe.Slice(bstr, charLen)
	return syscall.UTF16ToString(slice)
}

// --- Window scanning ---

func scanBrowserWindows() []browserURLEntry {
	auto := createUIAutomation()
	if auto == nil {
		return nil
	}
	defer auto.release()

	var results []browserURLEntry

	type winInfo struct {
		hwnd uintptr
		pid  uint32
	}

	var mu sync.Mutex
	var windows []winInfo

	cb := syscall.NewCallback(func(hwnd uintptr, lParam uintptr) uintptr {
		var cls [256]uint16
		pGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), 256)
		className := syscall.UTF16ToString(cls[:])

		if className != "Chrome_WidgetWin_1" {
			return 1
		}

		tLen, _, _ := pGetWindowTextLengthW.Call(hwnd)
		if tLen == 0 {
			return 1
		}

		var pid uint32
		pGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))

		mu.Lock()
		windows = append(windows, winInfo{hwnd: hwnd, pid: pid})
		mu.Unlock()
		return 1
	})

	pEnumWindows.Call(cb, 0)

	for _, w := range windows {
		browser := detectBrowserByPID(w.pid)
		if browser != "chrome" && browser != "edge" {
			continue
		}

		rawURL := getURLFromBrowserWindow(auto, w.hwnd)
		if rawURL == "" {
			continue
		}

		url := rawURL
		if !strings.Contains(url, "://") {
			url = "https://" + url
		}

		results = append(results, browserURLEntry{
			Browser: browser,
			PID:     w.pid,
			URL:     url,
			HWND:    w.hwnd,
		})
	}

	return results
}

func detectBrowserByPID(pid uint32) string {
	h, _, _ := pOpenProcess.Call(0x1000, 0, uintptr(pid))
	if h == 0 {
		return "unknown"
	}
	defer pCloseHandle.Call(h)

	var buf [260]uint16
	size := uint32(260)
	ret, _, _ := pQueryFullProcessImageNameW.Call(h, 0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)))
	if ret == 0 {
		return "unknown"
	}

	name := strings.ToLower(syscall.UTF16ToString(buf[:size]))
	if strings.Contains(name, "chrome") {
		return "chrome"
	}
	if strings.Contains(name, "msedge") {
		return "edge"
	}
	if strings.Contains(name, "firefox") {
		return "firefox"
	}
	return "unknown"
}
