//go:build windows

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"parental-control-service/internal/config"
)

const (
	serviceName        = "ParentalControlService"
	serviceDisplayName = "Parental Control Service"
	serviceDescription = "Monitors and restricts application and browser usage according to parental control schedule."
	serviceExeName     = "service.exe"
	trayExeName        = "tray.exe"
	agentExeName       = "browser-agent.exe"
	trayRegKey         = `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`
	trayRegValue       = "ParentalControlTray"
	agentRegValue      = "ParentalControlBrowserAgent"
	dataDir            = `C:\ProgramData\ParentalControlService`
	installDir         = `C:\Program Files\ParentalControlService`
	defaultHTTPPort    = 8080
	firewallRuleName   = "ParentalControlService HTTP"
)

func main() {
	cleanFlag := flag.Bool("clean", false, "Remove data directory on uninstall")
	configURL := flag.String("config-url", "", "GitHub raw URL for combined config.json (required for silent install)")
	password := flag.String("password", "", "Password for pause/unpause feature (required for silent install)")
	silentFlag := flag.Bool("silent", false, "Silent mode (no GUI, requires --config-url and --password)")
	flag.Parse()

	args := flag.Args()

	// Если нет аргументов и не silent — показываем GUI.
	if len(args) == 0 && !*silentFlag {
		params := showInstallerGUI()
		if params.Cancelled {
			fmt.Println("Installation cancelled.")
			os.Exit(0)
		}
		// Хешируем пароль.
		hash, err := bcrypt.GenerateFromPassword([]byte(params.Password), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("Failed to hash password: %v", err)
		}
		settings := &config.ServiceSettings{
			ConfigURL:    params.ConfigURL,
			PasswordHash: string(hash),
		}
		if err := doInstall(settings); err != nil {
			log.Fatalf("Install failed: %v", err)
		}
		fmt.Println("\nService installed successfully.")
		return
	}

	// Silent mode или явная команда.
	if len(args) == 0 {
		args = []string{"install"} // --silent без команды = install
	}

	switch args[0] {
	case "install":
		if *configURL == "" || *password == "" {
			log.Fatalf("Silent install requires --config-url and --password flags.")
		}
		// Хешируем пароль через bcrypt (необратимый хеш).
		hash, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("Failed to hash password: %v", err)
		}
		settings := &config.ServiceSettings{
			ConfigURL:    *configURL,
			PasswordHash: string(hash),
		}
		if err := doInstall(settings); err != nil {
			log.Fatalf("Install failed: %v", err)
		}
		fmt.Println("\nService installed successfully.")
	case "uninstall":
		if err := doUninstall(*cleanFlag); err != nil {
			log.Fatalf("Uninstall failed: %v", err)
		}
		fmt.Println("Service uninstalled successfully.")
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", args[0])
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  installer                                    (GUI mode)\n")
		fmt.Fprintf(os.Stderr, "  installer --silent --config-url=URL --password=PWD install\n")
		fmt.Fprintf(os.Stderr, "  installer [--clean] uninstall\n")
		os.Exit(1)
	}
}

// doInstall performs the full installation sequence.
// If a previous installation exists, it stops and removes the old service first.
func doInstall(settings *config.ServiceSettings) error {
	// 0. Stop and remove previous installation if it exists.
	cleanupPreviousInstall()

	// 1. Verify config URLs are accessible and download configs.
	fmt.Println("Checking config URLs...")
	configs, err := downloadConfigs(settings)
	if err != nil {
		return fmt.Errorf("config download failed: %w", err)
	}
	fmt.Println("All config files downloaded successfully")

	// 2. Find a free port for the HTTP status server.
	port := findFreePort(defaultHTTPPort)
	settings.HTTPPort = port
	fmt.Printf("Selected HTTP port: %d\n", port)

	// 3. If directory already exists, reset ACL so we can write into it.
	if _, err := os.Stat(dataDir); err == nil {
		if err := resetACL(dataDir); err != nil {
			return fmt.Errorf("reset ACL on existing directory: %w", err)
		}
		fmt.Println("Reset ACL on existing directory")
	}

	// 4. Create data directory and subdirectories.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	fmt.Printf("Created directory: %s\n", dataDir)

	for _, sub := range []string{"config", "logs"} {
		dir := filepath.Join(dataDir, sub)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create subdirectory %s: %w", sub, err)
		}
	}

	// 5. Save settings (URLs + port) to settings.json.
	if err := config.SaveSettings(dataDir, settings); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	fmt.Println("Saved settings.json")

	// 6. Save downloaded configs as initial cache.
	configDir := filepath.Join(dataDir, "config")
	for name, data := range configs {
		path := filepath.Join(configDir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("save config %s: %w", name, err)
		}
		fmt.Printf("Saved config cache: %s\n", name)
	}

	// 7. Create blocked.html.
	if err := createBlockedPage(); err != nil {
		return fmt.Errorf("create blocked page: %w", err)
	}
	fmt.Println("Created blocked.html")

	// 8. Create install directory and copy binaries.
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create install directory: %w", err)
	}

	if err := copyBinary(serviceExeName); err != nil {
		return fmt.Errorf("copy service.exe: %w", err)
	}
	fmt.Println("Copied service.exe")

	if err := copyBinary(trayExeName); err != nil {
		fmt.Printf("Warning: could not copy tray.exe: %v\n", err)
	} else {
		fmt.Println("Copied tray.exe")
	}

	if err := copyBinary(agentExeName); err != nil {
		fmt.Printf("Warning: could not copy browser-agent.exe: %v\n", err)
	} else {
		fmt.Println("Copied browser-agent.exe")
	}

	// 9. Apply SYSTEM-only ACL to protect the data directory.
	if err := applyProtectedACL(dataDir); err != nil {
		return fmt.Errorf("apply protected ACL: %w", err)
	}
	fmt.Printf("Applied protected ACL to: %s\n", dataDir)

	// 10. Add firewall rule for the HTTP port.
	if err := addFirewallRule(port); err != nil {
		fmt.Printf("Warning: could not add firewall rule: %v\n", err)
	} else {
		fmt.Printf("Added firewall rule for port %d\n", port)
	}

	// 11. Save port to registry (for tray app access).
	if err := savePortToRegistry(port); err != nil {
		fmt.Printf("Warning: could not save port to registry: %v\n", err)
	} else {
		fmt.Println("Saved HTTP port to registry")
	}

	// 12. Register event log source.
	if err := installEventLogSource(); err != nil {
		fmt.Printf("Event log source: %v (skipped)\n", err)
	} else {
		fmt.Println("Registered event log source")
	}

	// 12. Install the Windows service.
	if err := installService(); err != nil {
		fmt.Printf("Windows service: %v (skipped)\n", err)
	} else {
		fmt.Println("Installed Windows service")
	}

	// 13. Configure recovery actions.
	if err := configureRecovery(); err != nil {
		fmt.Printf("Warning: could not configure recovery: %v\n", err)
	} else {
		fmt.Println("Configured recovery actions")
	}

	// 14. Register tray autostart.
	if err := registerTrayAutostart(); err != nil {
		fmt.Printf("Warning: could not register tray autostart: %v\n", err)
	} else {
		fmt.Println("Registered tray autostart")
	}

	// 14b. Register browser-agent autostart.
	if err := registerAgentAutostart(); err != nil {
		fmt.Printf("Warning: could not register browser-agent autostart: %v\n", err)
	} else {
		fmt.Println("Registered browser-agent autostart")
	}

	// 15. Start the service.
	if err := startService(); err != nil {
		fmt.Printf("Warning: could not start service: %v\n", err)
	} else {
		fmt.Println("Service started")
	}

	// 16. Launch tray and browser-agent in the user session.
	// Инсталлер работает от админа (UAC), поэтому используем explorer.exe
	// как посредник — он запускает процесс от имени залогиненного пользователя.
	launchAsUser(filepath.Join(installDir, agentExeName))
	fmt.Println("Launched browser-agent.exe")
	launchAsUser(filepath.Join(installDir, trayExeName))
	fmt.Println("Launched tray.exe")

	return nil
}

// launchAsUser запускает exe от имени текущего пользователя (не админа).
// Используем explorer.exe как посредник: explorer запускает процесс
// в сессии залогиненного пользователя, даже если вызывающий — админ.
func launchAsUser(exePath string) {
	cmd := exec.Command("explorer.exe", exePath)
	_ = cmd.Start()
}

// cleanupPreviousInstall stops the old service, kills tray processes,
// removes the old service registration, and deletes old binaries.
func cleanupPreviousInstall() {
	// Stop the running service via SCM.
	if err := stopService(); err != nil {
		fmt.Printf("No running service to stop via SCM: %v\n", err)
	} else {
		fmt.Println("Stopped previous service via SCM")
	}

	// Force-kill service process in case SCM stop didn't work.
	cmd := exec.Command("taskkill", "/F", "/IM", serviceExeName)
	if out, err := cmd.CombinedOutput(); err == nil {
		fmt.Printf("Force-killed service process: %s\n", string(out))
	}

	// Kill any running tray.exe and browser-agent.exe processes.
	killTrayProcesses()
	killAgentProcesses()

	// Small delay to let processes fully terminate and release file locks.
	time.Sleep(2 * time.Second)

	// Delete the old service registration from SCM.
	if err := deleteService(); err != nil {
		fmt.Printf("No previous service to remove: %v\n", err)
	} else {
		fmt.Println("Removed previous service registration")
	}

	// Remove old binaries from install directory.
	for _, name := range []string{serviceExeName, trayExeName, agentExeName} {
		old := filepath.Join(installDir, name)
		if err := os.Remove(old); err == nil {
			fmt.Printf("Removed old %s\n", name)
		}
	}
}

// killTrayProcesses terminates all running tray.exe instances.
func killTrayProcesses() {
	cmd := exec.Command("taskkill", "/F", "/IM", trayExeName)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Println("No tray processes to kill")
	} else {
		fmt.Printf("Killed tray processes: %s\n", string(out))
	}
}

// killAgentProcesses terminates all running browser-agent.exe instances.
func killAgentProcesses() {
	cmd := exec.Command("taskkill", "/F", "/IM", agentExeName)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Println("No browser-agent processes to kill")
	} else {
		fmt.Printf("Killed browser-agent processes: %s\n", string(out))
	}
}

// findFreePort checks if the preferred port is available, and if not,
// scans upward until a free port is found.
func findFreePort(preferred int) int {
	for port := preferred; port < preferred+100; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			return port
		}
	}
	// Fallback: let OS pick a port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return preferred // last resort
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// addFirewallRule creates a Windows Firewall inbound rule allowing
// localhost TCP traffic on the given port.
func addFirewallRule(port int) error {
	// Remove old rule first (ignore errors).
	_ = removeFirewallRule()

	cmd := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		fmt.Sprintf("name=%s", firewallRuleName),
		"dir=in",
		"action=allow",
		"protocol=TCP",
		fmt.Sprintf("localport=%d", port),
		"remoteip=localsubnet",
		"profile=any",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh: %s: %w", string(out), err)
	}
	return nil
}

// removeFirewallRule deletes the firewall rule created during installation.
func removeFirewallRule() error {
	cmd := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule",
		fmt.Sprintf("name=%s", firewallRuleName),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh: %s: %w", string(out), err)
	}
	return nil
}

// downloadConfigs fetches the combined config.json from GitHub and validates
// that it contains valid JSON with the expected structure.
// Returns a map with a single "config.json" -> raw JSON bytes entry.
func downloadConfigs(settings *config.ServiceSettings) (map[string][]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	ctx := context.Background()

	fmt.Printf("  Downloading config.json from %s ... ", settings.ConfigURL)
	data, err := fetchAndValidate(ctx, client, settings.ConfigURL)
	if err != nil {
		fmt.Println("FAILED")
		return nil, fmt.Errorf("config.json: %w", err)
	}

	// Validate structure: must have allowed_apps, allowed_sites, schedule.
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Println("FAILED")
		return nil, fmt.Errorf("config.json: invalid structure: %w", err)
	}
	fmt.Println("OK")

	return map[string][]byte{"config.json": data}, nil
}

// fetchAndValidate downloads a URL and checks that the response is valid JSON.
func fetchAndValidate(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d (expected 200)", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// Validate JSON.
	if !json.Valid(data) {
		return nil, fmt.Errorf("response is not valid JSON")
	}

	return data, nil
}

// copyBinary copies a binary from the installer's directory to installDir.
func copyBinary(name string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("eval symlinks: %w", err)
	}

	srcPath := filepath.Join(filepath.Dir(exePath), name)
	if _, err := os.Stat(srcPath); err != nil {
		return fmt.Errorf("%s not found next to installer (%s): %w", name, srcPath, err)
	}

	dstPath := filepath.Join(installDir, name)

	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}

// doUninstall performs the full uninstallation sequence.
func doUninstall(clean bool) error {
	// 1. Stop the service via SCM.
	if err := stopService(); err != nil {
		fmt.Printf("Warning: could not stop service via SCM: %v\n", err)
	} else {
		fmt.Println("Stopped service via SCM")
	}

	// 2. Force-kill service process (fallback if SCM stop didn't work).
	cmd := exec.Command("taskkill", "/F", "/IM", serviceExeName)
	if out, err := cmd.CombinedOutput(); err == nil {
		fmt.Printf("Force-killed service process: %s\n", string(out))
	}

	// 3. Kill tray and browser-agent processes so files are unlocked.
	killTrayProcesses()
	killAgentProcesses()

	// Даём процессам время завершиться и отпустить файлы.
	time.Sleep(2 * time.Second)

	// 4. Delete the service registration.
	if err := deleteService(); err != nil {
		fmt.Printf("Warning: could not delete service: %v\n", err)
	} else {
		fmt.Println("Deleted Windows service")
	}

	// 5. Remove event log source.
	if err := removeEventLogSource(); err != nil {
		fmt.Printf("Warning: could not remove event log source: %v\n", err)
	} else {
		fmt.Println("Removed event log source")
	}

	// 6. Remove tray autostart from registry.
	if err := removeTrayAutostart(); err != nil {
		fmt.Printf("Warning: could not remove tray autostart: %v\n", err)
	} else {
		fmt.Println("Removed tray autostart")
	}

	// 6b. Remove browser-agent autostart from registry.
	if err := removeAgentAutostart(); err != nil {
		fmt.Printf("Warning: could not remove browser-agent autostart: %v\n", err)
	} else {
		fmt.Println("Removed browser-agent autostart")
	}

	// 7. Remove firewall rule.
	if err := removeFirewallRule(); err != nil {
		fmt.Printf("Warning: could not remove firewall rule: %v\n", err)
	} else {
		fmt.Println("Removed firewall rule")
	}

	// 8. Remove port from registry.
	if err := removePortFromRegistry(); err != nil {
		fmt.Printf("Warning: could not remove port registry key: %v\n", err)
	} else {
		fmt.Println("Removed port registry key")
	}

	// 9. Remove install directory (binaries).
	if err := os.RemoveAll(installDir); err != nil {
		fmt.Printf("Warning: could not remove install directory: %v\n", err)
	} else {
		fmt.Printf("Removed install directory: %s\n", installDir)
	}

	// 10. Optionally clean up data directory.
	if clean {
		// Reset ACL first so we can delete.
		_ = resetACL(dataDir)
		if err := os.RemoveAll(dataDir); err != nil {
			return fmt.Errorf("remove data directory: %w", err)
		}
		fmt.Printf("Removed data directory: %s\n", dataDir)
	}

	return nil
}

// installService creates the Windows service pointing to the copied service.exe.
func installService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	binPath := filepath.Join(installDir, serviceExeName)

	s, err := m.CreateService(serviceName, binPath, mgr.Config{
		DisplayName:      serviceDisplayName,
		Description:      serviceDescription,
		StartType:        mgr.StartAutomatic,
		ServiceStartName: "", // LocalSystem account.
	})
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	return nil
}

// configureRecovery sets recovery actions: 3 restarts with increasing delays.
func configureRecovery() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()

	recoveryActions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	return s.SetRecoveryActions(recoveryActions, 86400)
}

// startService starts the Windows service after installation.
func startService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()

	return s.Start()
}

// stopService attempts to stop the running service.
func stopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()

	status, err := s.Control(windows.SERVICE_CONTROL_STOP)
	if err != nil {
		return fmt.Errorf("send stop control: %w", err)
	}

	timeout := time.Now().Add(30 * time.Second)
	for status.State != windows.SERVICE_STOPPED {
		if time.Now().After(timeout) {
			return fmt.Errorf("timeout waiting for service to stop")
		}
		time.Sleep(1 * time.Second)
		status, err = s.Query()
		if err != nil {
			return fmt.Errorf("query service status: %w", err)
		}
	}
	return nil
}

// deleteService removes the Windows service registration.
func deleteService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()

	return s.Delete()
}

// installEventLogSource registers the event log source for the service.
func installEventLogSource() error {
	return eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info)
}

// removeEventLogSource removes the event log source registration.
func removeEventLogSource() error {
	return eventlog.Remove(serviceName)
}

// registerTrayAutostart registers tray.exe in HKLM Run for all users.
func registerTrayAutostart() error {
	trayPath := filepath.Join(installDir, trayExeName)
	if _, err := os.Stat(trayPath); err != nil {
		return fmt.Errorf("tray.exe not found at %s: %w", trayPath, err)
	}

	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, trayRegKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open registry key: %w", err)
	}
	defer key.Close()

	return key.SetStringValue(trayRegValue, `"`+trayPath+`"`)
}

// removeTrayAutostart removes the tray autostart registry entry.
func removeTrayAutostart() error {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, trayRegKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.DeleteValue(trayRegValue)
}

// registerAgentAutostart registers browser-agent.exe in HKLM Run for all users.
func registerAgentAutostart() error {
	agentPath := filepath.Join(installDir, agentExeName)
	if _, err := os.Stat(agentPath); err != nil {
		return fmt.Errorf("browser-agent.exe not found at %s: %w", agentPath, err)
	}

	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, trayRegKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open registry key: %w", err)
	}
	defer key.Close()

	return key.SetStringValue(agentRegValue, `"`+agentPath+`"`)
}

// removeAgentAutostart removes the browser-agent autostart registry entry.
func removeAgentAutostart() error {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, trayRegKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.DeleteValue(agentRegValue)
}


// savePortToRegistry writes the HTTP port to registry so the tray app
// (running as regular user) can read it without accessing SYSTEM-only files.
func savePortToRegistry(port int) error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE,
		`SOFTWARE\ParentalControlService`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("create registry key: %w", err)
	}
	defer key.Close()
	return key.SetDWordValue("HTTPPort", uint32(port))
}

// removePortFromRegistry removes the service registry key.
func removePortFromRegistry() error {
	return registry.DeleteKey(registry.LOCAL_MACHINE, `SOFTWARE\ParentalControlService`)
}

// resetACL temporarily grants Administrators + SYSTEM full access to the directory.
func resetACL(dir string) error {
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parse SDDL: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("get DACL: %w", err)
	}
	dirPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("convert path: %w", err)
	}
	return setNamedSecurityInfo(dirPtr, dacl)
}

// applyProtectedACL applies a protected ACL to the data directory:
// SYSTEM — full access (read/write/execute), Users (BU) — read & execute only.
// This allows the tray app (running as regular user) to read settings.json,
// while preventing the child from modifying configs.
func applyProtectedACL(dir string) error {
	// SDDL: D:P = protected DACL (no inheritance)
	//   (A;OICI;FA;;;SY)     — SYSTEM: Full Access
	//   (A;OICI;0x1200a9;;;BU) — Built-in Users: Read & Execute
	//     0x1200a9 = FILE_GENERIC_READ | FILE_GENERIC_EXECUTE
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;0x1200a9;;;BU)"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parse SDDL: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("get DACL from SD: %w", err)
	}
	dirPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("convert path: %w", err)
	}
	return setNamedSecurityInfo(dirPtr, dacl)
}

// setNamedSecurityInfo applies a DACL to a file/directory using the Windows API.
func setNamedSecurityInfo(objectName *uint16, dacl *windows.ACL) error {
	const (
		SE_FILE_OBJECT                      = 1
		DACL_SECURITY_INFORMATION           = 0x00000004
		PROTECTED_DACL_SECURITY_INFORMATION = 0x80000000
	)
	mod := windows.NewLazySystemDLL("advapi32.dll")
	proc := mod.NewProc("SetNamedSecurityInfoW")
	ret, _, err := proc.Call(
		uintptr(unsafe.Pointer(objectName)),
		uintptr(SE_FILE_OBJECT),
		uintptr(DACL_SECURITY_INFORMATION|PROTECTED_DACL_SECURITY_INFORMATION),
		0, 0,
		uintptr(unsafe.Pointer(dacl)),
		0,
	)
	if ret != 0 {
		return fmt.Errorf("SetNamedSecurityInfoW returned %d: %w", ret, err)
	}
	return nil
}

// createBlockedPage writes the blocked.html file to the data directory.
func createBlockedPage() error {
	path := filepath.Join(dataDir, "blocked.html")
	return os.WriteFile(path, []byte(blockedPageHTML), 0o644)
}

const blockedPageHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Доступ заблокирован</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            display: flex; justify-content: center; align-items: center;
            min-height: 100vh; color: #fff;
        }
        .container { text-align: center; padding: 2rem; max-width: 500px; }
        .icon { font-size: 4rem; margin-bottom: 1rem; }
        h1 { font-size: 1.8rem; margin-bottom: 0.5rem; }
        p { font-size: 1.1rem; opacity: 0.9; line-height: 1.6; }
    </style>
</head>
<body>
    <div class="container">
        <div class="icon" role="img" aria-label="Blocked">&#128683;</div>
        <h1>Доступ заблокирован</h1>
        <p>Этот сайт недоступен в текущее время согласно настройкам родительского контроля.</p>
    </div>
</body>
</html>
`
