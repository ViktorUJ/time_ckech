//go:build windows

// Package learning collects detailed process information in learning mode.
package learning

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessSummary — агрегированная информация о программе за сессию.
type ProcessSummary struct {
	Name            string   `json:"name"`
	ExePath         string   `json:"exe_path"`
	Username        string   `json:"username"`
	Category        string   `json:"category"`
	Description     string   `json:"description,omitempty"`
	Company         string   `json:"company,omitempty"`
	Version         string   `json:"version,omitempty"`
	FirstSeen       string   `json:"first_seen"`
	LastSeen        string   `json:"last_seen"`
	MemoryMinMB     float64  `json:"memory_min_mb"`
	MemoryMaxMB     float64  `json:"memory_max_mb"`
	ActiveMinutes   float64  `json:"active_minutes"`
	InstancesMin    int      `json:"instances_min"`
	InstancesMax    int      `json:"instances_max"`
	RemoteAddrs     []string `json:"remote_addrs,omitempty"`
	ConnectionCount int      `json:"connection_count,omitempty"`
}

// LearningReport — агрегированный отчёт за сессию обучения.
type LearningReport struct {
	StartTime   time.Time        `json:"start_time"`
	EndTime     time.Time        `json:"end_time"`
	Username    string           `json:"username"`
	DurationMin float64          `json:"duration_minutes"`
	Processes   []ProcessSummary `json:"processes"`
}

// Collector собирает детальную информацию о процессах в режиме обучения.
type Collector struct {
	mu                sync.Mutex
	dataDir           string
	processes         map[string]*processStat
	startTime         time.Time
	lastFlush         time.Time
	totalTicks        int
	active            bool
	allowedApps       map[string]bool
	entertainmentApps map[string]bool
}

// processStat — внутренняя статистика по процессу.
type processStat struct {
	Name            string
	ExePath         string
	Username        string
	Category        string
	Description     string
	Company         string
	Version         string
	FirstSeen       time.Time
	LastSeen        time.Time
	MemoryMin       float64
	MemoryMax       float64
	TickCount       int             // сколько тиков программа была активна (1 за тик, не за экземпляр)
	InstancesMin    int             // минимальное кол-во экземпляров за тик
	InstancesMax    int             // максимальное кол-во экземпляров за тик
	RemoteAddrs     map[string]bool
	ConnectionCount int
}

// NewCollector создаёт новый Collector.
func NewCollector(dataDir string) *Collector {
	return &Collector{
		dataDir:           dataDir,
		allowedApps:       make(map[string]bool),
		entertainmentApps: make(map[string]bool),
	}
}

// UpdateConfig обновляет списки приложений для классификации.
func (c *Collector) UpdateConfig(allowedExes []string, entertainmentExes []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.allowedApps = make(map[string]bool)
	for _, e := range allowedExes {
		c.allowedApps[strings.ToLower(e)] = true
	}
	c.entertainmentApps = make(map[string]bool)
	for _, e := range entertainmentExes {
		c.entertainmentApps[strings.ToLower(e)] = true
	}
}

// classifyProcess определяет категорию процесса.
func (c *Collector) classifyProcess(name string) string {
	lower := strings.ToLower(name)
	if c.entertainmentApps[lower] {
		return "entertainment"
	}
	if c.allowedApps[lower] {
		return "allowed"
	}
	// Системные процессы Windows.
	systemProcs := map[string]bool{
		"svchost.exe": true, "csrss.exe": true, "wininit.exe": true,
		"services.exe": true, "lsass.exe": true, "smss.exe": true,
		"dwm.exe": true, "explorer.exe": true, "sihost.exe": true,
		"taskhostw.exe": true, "ctfmon.exe": true, "conhost.exe": true,
		"runtimebroker.exe": true, "dllhost.exe": true, "unsecapp.exe": true,
		"searchhost.exe": true, "startmenuexperiencehost.exe": true,
		"shellexperiencehost.exe": true, "textinputhost.exe": true,
		"applicationframehost.exe": true, "backgroundtaskhost.exe": true,
		"shellhost.exe": true, "widgetservice.exe": true, "widgets.exe": true,
		"smartscreen.exe": true, "securityhealthsystray.exe": true,
		"phoneexperiencehost.exe": true, "crossdeviceservice.exe": true,
		"crossdeviceresume.exe": true, "lockapp.exe": true,
		"searchprotocolhost.exe": true, "wmiprvse.exe": true,
		"searchapp.exe": true, "useroobobroker.exe": true,
		"automodedetect.exe": true, "comppkgsrv.exe": true,
		"igfxemn.exe": true, "tposd.exe": true,
		"smartsensecontroller.exe": true, "rtkauduservice64.exe": true,
		"fnhotkeyutility.exe": true, "fnhotkeycapslknumlk.exe": true,
		"userssctrl.exe": true, "nahimicapo4volume.exe": true,
		"monotificationux.exe": true, "systemsettings.exe": true,
		"cmd.exe": true, "powershell.exe": true, "openconsole.exe": true,
		"browser-agent.exe": true, "service.exe": true, "tray.exe": true,
	}
	if systemProcs[lower] {
		return "system"
	}
	// Паттерны для системных процессов.
	if strings.HasPrefix(lower, "lenovovantage-") ||
		strings.HasPrefix(lower, "msedgewebview2") ||
		strings.HasPrefix(lower, "ipf_") ||
		strings.Contains(lower, "updater") {
		return "system"
	}
	return "unknown"
}

// Start начинает сессию сбора данных.
func (c *Collector) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active {
		return
	}
	c.active = true
	c.startTime = time.Now()
	c.lastFlush = time.Now()
	c.totalTicks = 0
	c.processes = make(map[string]*processStat)
}

// Stop завершает сессию и сохраняет отчёт на диск.
func (c *Collector) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return
	}
	c.active = false
	c.flushLocked()
}

const flushInterval = 30 * time.Second

// CollectTick собирает снимок всех процессов и агрегирует.
func (c *Collector) CollectTick() {
	c.mu.Lock()
	if !c.active {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	snapshots := c.enumerateDetailed()
	now := time.Now()

	c.mu.Lock()
	c.totalTicks++

	// Группируем по ключу (exe_path) за этот тик.
	type tickGroup struct {
		count    int
		memTotal float64
		memMin   float64
		memMax   float64
		addrs    []string
		first    rawProcess // первый экземпляр для метаданных
	}
	groups := make(map[string]*tickGroup)
	for _, ps := range snapshots {
		key := ps.ExePath
		if key == "" {
			key = ps.Name
		}
		if g, ok := groups[key]; ok {
			g.count++
			g.memTotal += ps.MemoryMB
			if ps.MemoryMB < g.memMin {
				g.memMin = ps.MemoryMB
			}
			if ps.MemoryMB > g.memMax {
				g.memMax = ps.MemoryMB
			}
			g.addrs = append(g.addrs, ps.RemoteAddrs...)
		} else {
			groups[key] = &tickGroup{
				count: 1, memTotal: ps.MemoryMB,
				memMin: ps.MemoryMB, memMax: ps.MemoryMB,
				addrs: ps.RemoteAddrs, first: ps,
			}
		}
	}

	// Обновляем статистику — один раз за тик на ключ.
	for key, g := range groups {
		totalMem := g.memTotal // суммарная память всех экземпляров
		if stat, ok := c.processes[key]; ok {
			stat.LastSeen = now
			stat.TickCount++
			if totalMem < stat.MemoryMin {
				stat.MemoryMin = totalMem
			}
			if totalMem > stat.MemoryMax {
				stat.MemoryMax = totalMem
			}
			if g.count < stat.InstancesMin {
				stat.InstancesMin = g.count
			}
			if g.count > stat.InstancesMax {
				stat.InstancesMax = g.count
			}
			for _, addr := range g.addrs {
				stat.RemoteAddrs[addr] = true
			}
			stat.ConnectionCount += len(g.addrs)
		} else {
			addrs := make(map[string]bool)
			for _, a := range g.addrs {
				addrs[a] = true
			}
			c.processes[key] = &processStat{
				Name:            g.first.Name,
				ExePath:         g.first.ExePath,
				Username:        g.first.Username,
				Category:        c.classifyProcess(g.first.Name),
				Description:     g.first.Desc,
				Company:         g.first.Company,
				Version:         g.first.Version,
				FirstSeen:       now,
				LastSeen:        now,
				MemoryMin:       totalMem,
				MemoryMax:       totalMem,
				TickCount:       1,
				InstancesMin:    g.count,
				InstancesMax:    g.count,
				RemoteAddrs:     addrs,
				ConnectionCount: len(g.addrs),
			}
		}
	}
	if time.Since(c.lastFlush) >= flushInterval {
		c.flushLocked()
	}
	c.mu.Unlock()
}

// buildReport строит отчёт из текущих данных (вызывать под мьютексом).
func (c *Collector) buildReport() *LearningReport {
	procs := make([]ProcessSummary, 0, len(c.processes))
	for _, s := range c.processes {
		var addrs []string
		for addr := range s.RemoteAddrs {
			addrs = append(addrs, addr)
		}
		procs = append(procs, ProcessSummary{
			Name:            s.Name,
			ExePath:         s.ExePath,
			Username:        s.Username,
			Category:        s.Category,
			Description:     s.Description,
			Company:         s.Company,
			Version:         s.Version,
			FirstSeen:       s.FirstSeen.Format(time.RFC3339),
			LastSeen:        s.LastSeen.Format(time.RFC3339),
			ActiveMinutes:   math.Round(float64(s.TickCount)*15.0/60.0*100) / 100,
			InstancesMin:    s.InstancesMin,
			InstancesMax:    s.InstancesMax,
			MemoryMinMB:     math.Round(s.MemoryMin*100) / 100,
			MemoryMaxMB:     math.Round(s.MemoryMax*100) / 100,
			RemoteAddrs:     addrs,
			ConnectionCount: s.ConnectionCount,
		})
	}
	now := time.Now()
	return &LearningReport{
		StartTime:   c.startTime,
		EndTime:     now,
		Username:    getCurrentUsername(),
		DurationMin: math.Round(now.Sub(c.startTime).Minutes()*100) / 100,
		Processes:   procs,
	}
}

// flushLocked сохраняет текущие данные на диск (вызывать под мьютексом).
func (c *Collector) flushLocked() {
	if len(c.processes) == 0 {
		return
	}
	report := c.buildReport()

	dir := filepath.Join(c.dataDir, "learning")
	_ = os.MkdirAll(dir, 0o700)

	filename := fmt.Sprintf("learning_%s.json", c.startTime.Format("2006-01-02_15-04-05"))
	path := filepath.Join(dir, filename)

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
	c.lastFlush = time.Now()
}

// IsActive возвращает true если сбор данных активен.
func (c *Collector) IsActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// GetCurrentReport возвращает текущий отчёт (для API).
func (c *Collector) GetCurrentReport() *LearningReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active && len(c.processes) == 0 {
		return nil
	}
	return c.buildReport()
}

// ListReports возвращает список сохранённых отчётов.
func (c *Collector) ListReports() []string {
	dir := filepath.Join(c.dataDir, "learning")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	return names
}

// ReadReport читает сохранённый отчёт по имени файла.
func (c *Collector) ReadReport(name string) ([]byte, error) {
	path := filepath.Join(c.dataDir, "learning", filepath.Base(name))
	return os.ReadFile(path)
}

// ClearReports удаляет все сохранённые отчёты.
func (c *Collector) ClearReports() error {
	dir := filepath.Join(c.dataDir, "learning")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// rawProcess — сырые данные о процессе для агрегации.
type rawProcess struct {
	Name        string
	ExePath     string
	Username    string
	Desc        string
	Company     string
	Version     string
	MemoryMB    float64
	RemoteAddrs []string // remote IP:port текущих соединений
}

// enumerateDetailed собирает информацию о процессах текущего пользователя.
func (c *Collector) enumerateDetailed() []rawProcess {
	currentUser := getCurrentUsername()

	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil
	}

	var result []rawProcess
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		pid := entry.ProcessID

		if pid == 0 || name == "[System Process]" {
			if err := windows.Process32Next(snapshot, &entry); err != nil {
				break
			}
			continue
		}

		procUser := getProcessUsername(pid)
		if currentUser != "" && procUser != "" && procUser != currentUser {
			if err := windows.Process32Next(snapshot, &entry); err != nil {
				break
			}
			continue
		}

		rp := rawProcess{Name: name, Username: procUser}

		if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid); err == nil {
			var buf [windows.MAX_PATH]uint16
			size := uint32(len(buf))
			if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err == nil {
				rp.ExePath = windows.UTF16ToString(buf[:size])
			}
			windows.CloseHandle(h)
		}

		if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|0x0010, false, pid); err == nil {
			var memInfo processMemoryCounters
			memSize := uint32(unsafe.Sizeof(memInfo))
			if getProcessMemoryInfo(h, &memInfo, memSize) == nil {
				rp.MemoryMB = float64(memInfo.WorkingSetSize) / (1024 * 1024)
			}
			windows.CloseHandle(h)
		}

		if rp.ExePath != "" {
			rp.Desc, rp.Company, rp.Version = getFileVersionInfo(rp.ExePath)
		}

		// Сетевые соединения.
		rp.RemoteAddrs = getProcessConnections(pid)

		result = append(result, rp)

		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return result
}

// getProcessConnections возвращает список remote адресов TCP-соединений процесса.
func getProcessConnections(pid uint32) []string {
	conns := getTCPConnections()
	var result []string
	seen := make(map[string]bool)
	for _, c := range conns {
		if c.pid == pid && c.remoteAddr != "0.0.0.0:0" && c.remoteAddr != "[::]:0" {
			if !seen[c.remoteAddr] {
				seen[c.remoteAddr] = true
				result = append(result, c.remoteAddr)
			}
		}
	}
	return result
}

type tcpConn struct {
	pid        uint32
	remoteAddr string
	state      uint32
}

var (
	modIphlpapi          = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = modIphlpapi.NewProc("GetExtendedTcpTable")
)

func getTCPConnections() []tcpConn {
	// First call to get buffer size.
	var size uint32
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 1, 2 /*AF_INET*/, 5 /*TCP_TABLE_OWNER_PID_ALL*/, 0)
	if size == 0 {
		return nil
	}

	buf := make([]byte, size)
	r, _, _ := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		1,           // order
		2,           // AF_INET
		5,           // TCP_TABLE_OWNER_PID_ALL
		0,
	)
	if r != 0 {
		return nil
	}

	// Parse MIB_TCPTABLE_OWNER_PID.
	numEntries := *(*uint32)(unsafe.Pointer(&buf[0]))
	const entrySize = 24 // sizeof(MIB_TCPROW_OWNER_PID)
	var conns []tcpConn

	for i := uint32(0); i < numEntries; i++ {
		off := 4 + int(i)*entrySize
		if off+entrySize > len(buf) {
			break
		}
		state := *(*uint32)(unsafe.Pointer(&buf[off]))
		// localAddr := *(*uint32)(unsafe.Pointer(&buf[off+4]))
		// localPort := *(*uint16)(unsafe.Pointer(&buf[off+8]))
		remoteAddrRaw := *(*uint32)(unsafe.Pointer(&buf[off+12]))
		remotePortRaw := *(*uint16)(unsafe.Pointer(&buf[off+16]))
		pid := *(*uint32)(unsafe.Pointer(&buf[off+20]))

		remoteIP := fmt.Sprintf("%d.%d.%d.%d",
			byte(remoteAddrRaw), byte(remoteAddrRaw>>8),
			byte(remoteAddrRaw>>16), byte(remoteAddrRaw>>24))
		// Network byte order for port.
		remotePort := uint16(byte(remotePortRaw>>8)) | uint16(byte(remotePortRaw))<<8

		conns = append(conns, tcpConn{
			pid:        pid,
			remoteAddr: fmt.Sprintf("%s:%d", remoteIP, remotePort),
			state:      state,
		})
	}
	return conns
}

// getCurrentUsername возвращает имя текущего интерактивного пользователя.
func getCurrentUsername() string {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return ""
	}
	for {
		if windows.UTF16ToString(entry.ExeFile[:]) == "explorer.exe" {
			return getProcessUsername(entry.ProcessID)
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return ""
}

// getProcessUsername возвращает "DOMAIN\User" для процесса.
func getProcessUsername(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	var token windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token); err != nil {
		return ""
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil {
		return ""
	}

	account, domain, _, err := user.User.Sid.LookupAccount("")
	if err != nil {
		return ""
	}
	return domain + `\` + account
}

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

var (
	modPsapi                    = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo    = modPsapi.NewProc("GetProcessMemoryInfo")
	modVersion                  = windows.NewLazySystemDLL("version.dll")
	procGetFileVersionInfoSizeW = modVersion.NewProc("GetFileVersionInfoSizeW")
	procGetFileVersionInfoW     = modVersion.NewProc("GetFileVersionInfoW")
	procVerQueryValueW          = modVersion.NewProc("VerQueryValueW")
)

func getProcessMemoryInfo(handle windows.Handle, mem *processMemoryCounters, size uint32) error {
	r, _, err := procGetProcessMemoryInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(mem)), uintptr(size))
	if r == 0 {
		return err
	}
	return nil
}

func getFileVersionInfo(path string) (description, company, version string) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	size, _, _ := procGetFileVersionInfoSizeW.Call(uintptr(unsafe.Pointer(pathPtr)), 0)
	if size == 0 {
		return
	}
	data := make([]byte, size)
	r, _, _ := procGetFileVersionInfoW.Call(uintptr(unsafe.Pointer(pathPtr)), 0, size, uintptr(unsafe.Pointer(&data[0])))
	if r == 0 {
		return
	}
	langs := []string{"040904B0", "040904E4", "000004B0", "040904b0"}
	for _, lang := range langs {
		description = queryStringValue(data, lang, "FileDescription")
		company = queryStringValue(data, lang, "CompanyName")
		version = queryStringValue(data, lang, "FileVersion")
		if description != "" || company != "" {
			return
		}
	}
	return
}

func queryStringValue(data []byte, lang, key string) string {
	subBlock := fmt.Sprintf(`\StringFileInfo\%s\%s`, lang, key)
	subBlockPtr, err := windows.UTF16PtrFromString(subBlock)
	if err != nil {
		return ""
	}
	var valuePtr *uint16
	var valueLen uint32
	r, _, _ := procVerQueryValueW.Call(
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(unsafe.Pointer(subBlockPtr)),
		uintptr(unsafe.Pointer(&valuePtr)),
		uintptr(unsafe.Pointer(&valueLen)),
	)
	if r == 0 || valueLen == 0 {
		return ""
	}
	buf := make([]uint16, valueLen)
	for i := uint32(0); i < valueLen; i++ {
		buf[i] = *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(valuePtr)) + uintptr(i)*2))
	}
	return windows.UTF16ToString(buf)
}
