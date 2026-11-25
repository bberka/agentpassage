package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	systemdUnitName = "apx.service"
	launchdLabel    = "com.bberka.agentpassage"
	winTaskName     = "agentpassage-gateway"
)

type StatusInfo struct {
	Manager   string // "systemd", "launchd", "schtasks"
	Installed bool
	Enabled   bool // auto-start on login/boot enabled
	Running   bool
	UnitPath  string
	Detail    string
}

func binaryPath() (string, error) {
	if p, err := exec.LookPath("apx"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, nil
		}
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func UnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "linux":
		cfgHome := os.Getenv("XDG_CONFIG_HOME")
		if cfgHome == "" {
			cfgHome = filepath.Join(home, ".config")
		}
		return filepath.Join(cfgHome, "systemd", "user", systemdUnitName), nil
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
	case "windows":
		return winTaskName, nil
	default:
		return "", fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
}

// RenderSystemdUnit generates the systemd --user unit file content.
func RenderSystemdUnit(binPath, dataDir, pathEnv string) string {
	var envLines []string
	if pathEnv != "" {
		envLines = append(envLines, fmt.Sprintf("Environment=\"PATH=%s\"", pathEnv))
	}
	if dataDir != "" {
		envLines = append(envLines, fmt.Sprintf("Environment=\"APX_HOME=%s\"", dataDir))
	}
	envBlock := ""
	if len(envLines) > 0 {
		envBlock = strings.Join(envLines, "\n") + "\n"
	}
	return fmt.Sprintf(`[Unit]
Description=AgentPassage (apx) local ACP and API-key gateway
After=network.target

[Service]
Type=simple
ExecStart=%s serve
Restart=on-failure
RestartSec=2
%sPassEnvironment=HOME DBUS_SESSION_BUS_ADDRESS XDG_RUNTIME_DIR SSH_AUTH_SOCK

[Install]
WantedBy=default.target
`, binPath, envBlock)
}

// RenderLaunchdPlist generates the macOS LaunchAgent plist content.
func RenderLaunchdPlist(binPath, dataDir, pathEnv string, autoStart bool) string {
	logPath := filepath.Join(dataDir, "logs", "service.log")
	runAtLoad := "<true/>"
	if !autoStart {
		runAtLoad = "<false/>"
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>%s</string>
    <key>APX_HOME</key>
    <string>%s</string>
  </dict>
  <key>RunAtLoad</key>
  %s
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, launchdLabel, binPath, pathEnv, dataDir, runAtLoad, logPath, logPath)
}

func Install(dataDir string, autoStart bool) (string, error) {
	bin, err := binaryPath()
	if err != nil {
		return "", fmt.Errorf("resolve apx binary: %w", err)
	}
	pathEnv := os.Getenv("PATH")

	switch runtime.GOOS {
	case "linux":
		unitPath, err := UnitPath()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
			return "", err
		}
		unit := RenderSystemdUnit(bin, dataDir, pathEnv)
		if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
			return "", err
		}
		if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
			return unitPath, fmt.Errorf("systemctl daemon-reload failed: %s (%w)", strings.TrimSpace(string(out)), err)
		}
		if autoStart {
			if out, err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnitName).CombinedOutput(); err != nil {
				return unitPath, fmt.Errorf("systemctl enable --now failed: %s (%w)", strings.TrimSpace(string(out)), err)
			}
		} else {
			_ = exec.Command("systemctl", "--user", "disable", systemdUnitName).Run()
			if out, err := exec.Command("systemctl", "--user", "start", systemdUnitName).CombinedOutput(); err != nil {
				return unitPath, fmt.Errorf("systemctl start failed: %s (%w)", strings.TrimSpace(string(out)), err)
			}
		}
		return unitPath, nil

	case "darwin":
		plistPath, err := UnitPath()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
			return "", err
		}
		_ = os.MkdirAll(filepath.Join(dataDir, "logs"), 0o700)
		plist := RenderLaunchdPlist(bin, dataDir, pathEnv, autoStart)
		if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
			return "", err
		}
		_ = exec.Command("launchctl", "unload", plistPath).Run()
		if out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
			return plistPath, fmt.Errorf("launchctl load failed: %s (%w)", strings.TrimSpace(string(out)), err)
		}
		return plistPath, nil

	case "windows":
		tr := fmt.Sprintf(`"%s" serve`, bin)
		if out, err := exec.Command("schtasks", "/Create", "/F", "/SC", "ONLOGON", "/TN", winTaskName, "/TR", tr).CombinedOutput(); err != nil {
			return winTaskName, fmt.Errorf("schtasks create failed: %s (%w)", strings.TrimSpace(string(out)), err)
		}
		if !autoStart {
			_ = exec.Command("schtasks", "/Change", "/TN", winTaskName, "/DISABLE").Run()
		}
		_ = exec.Command("schtasks", "/Run", "/TN", winTaskName).Run()
		return winTaskName, nil

	default:
		return "", fmt.Errorf("background service not supported on %s", runtime.GOOS)
	}
}

// SetAutoStart enables or disables starting the apx service automatically on login/boot.
func SetAutoStart(dataDir string, enable bool) error {
	st := Status()
	if !st.Installed {
		if enable {
			_, err := Install(dataDir, true)
			return err
		}
		return nil
	}

	switch runtime.GOOS {
	case "linux":
		action := "disable"
		if enable {
			action = "enable"
		}
		if out, err := exec.Command("systemctl", "--user", action, systemdUnitName).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s failed: %s (%w)", action, strings.TrimSpace(string(out)), err)
		}
		return nil

	case "darwin":
		bin, err := binaryPath()
		if err != nil {
			return err
		}
		plistPath, err := UnitPath()
		if err != nil {
			return err
		}
		plist := RenderLaunchdPlist(bin, dataDir, os.Getenv("PATH"), enable)
		if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
			return err
		}
		_ = exec.Command("launchctl", "unload", plistPath).Run()
		if out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl reload failed: %s (%w)", strings.TrimSpace(string(out)), err)
		}
		return nil

	case "windows":
		flag := "/DISABLE"
		if enable {
			flag = "/ENABLE"
		}
		if out, err := exec.Command("schtasks", "/Change", "/TN", winTaskName, flag).CombinedOutput(); err != nil {
			return fmt.Errorf("schtasks %s failed: %s (%w)", flag, strings.TrimSpace(string(out)), err)
		}
		return nil

	default:
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
}

func Uninstall() error {
	switch runtime.GOOS {
	case "linux":
		unitPath, err := UnitPath()
		if err != nil {
			return err
		}
		_ = exec.Command("systemctl", "--user", "disable", "--now", systemdUnitName).Run()
		_ = os.Remove(unitPath)
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		return nil

	case "darwin":
		plistPath, err := UnitPath()
		if err != nil {
			return err
		}
		_ = exec.Command("launchctl", "unload", "-w", plistPath).Run()
		_ = os.Remove(plistPath)
		return nil

	case "windows":
		_ = exec.Command("schtasks", "/End", "/TN", winTaskName).Run()
		if out, err := exec.Command("schtasks", "/Delete", "/F", "/TN", winTaskName).CombinedOutput(); err != nil {
			return fmt.Errorf("schtasks delete failed: %s (%w)", strings.TrimSpace(string(out)), err)
		}
		return nil

	default:
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
}

func Start() error {
	switch runtime.GOOS {
	case "linux":
		if out, err := exec.Command("systemctl", "--user", "start", systemdUnitName).CombinedOutput(); err != nil {
			return fmt.Errorf("%s (%w)", strings.TrimSpace(string(out)), err)
		}
		return nil
	case "darwin":
		p, err := UnitPath()
		if err != nil {
			return err
		}
		if out, err := exec.Command("launchctl", "load", "-w", p).CombinedOutput(); err != nil {
			return fmt.Errorf("%s (%w)", strings.TrimSpace(string(out)), err)
		}
		return nil
	case "windows":
		if out, err := exec.Command("schtasks", "/Run", "/TN", winTaskName).CombinedOutput(); err != nil {
			return fmt.Errorf("%s (%w)", strings.TrimSpace(string(out)), err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
}

func Stop() error {
	switch runtime.GOOS {
	case "linux":
		if out, err := exec.Command("systemctl", "--user", "stop", systemdUnitName).CombinedOutput(); err != nil {
			return fmt.Errorf("%s (%w)", strings.TrimSpace(string(out)), err)
		}
		return nil
	case "darwin":
		p, err := UnitPath()
		if err != nil {
			return err
		}
		if out, err := exec.Command("launchctl", "unload", p).CombinedOutput(); err != nil {
			return fmt.Errorf("%s (%w)", strings.TrimSpace(string(out)), err)
		}
		return nil
	case "windows":
		if out, err := exec.Command("schtasks", "/End", "/TN", winTaskName).CombinedOutput(); err != nil {
			return fmt.Errorf("%s (%w)", strings.TrimSpace(string(out)), err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
}

func Restart() error {
	switch runtime.GOOS {
	case "linux":
		if out, err := exec.Command("systemctl", "--user", "restart", systemdUnitName).CombinedOutput(); err != nil {
			return fmt.Errorf("%s (%w)", strings.TrimSpace(string(out)), err)
		}
		return nil
	default:
		_ = Stop()
		return Start()
	}
}

func Status() StatusInfo {
	unitPath, _ := UnitPath()
	info := StatusInfo{UnitPath: unitPath}

	switch runtime.GOOS {
	case "linux":
		info.Manager = "systemd"
		if _, err := os.Stat(unitPath); err == nil {
			info.Installed = true
		}
		if enOut, err := exec.Command("systemctl", "--user", "is-enabled", systemdUnitName).CombinedOutput(); err == nil {
			info.Enabled = strings.TrimSpace(string(enOut)) == "enabled"
		}
		out, err := exec.Command("systemctl", "--user", "is-active", systemdUnitName).CombinedOutput()
		state := strings.TrimSpace(string(out))
		if err == nil && state == "active" {
			info.Running = true
			info.Detail = "active (running)"
		} else if !info.Installed {
			info.Detail = "not installed"
		} else if state != "" {
			info.Detail = state
		} else {
			info.Detail = "stopped"
		}
	case "darwin":
		info.Manager = "launchd"
		if raw, err := os.ReadFile(unitPath); err == nil {
			info.Installed = true
			info.Enabled = strings.Contains(string(raw), "<key>RunAtLoad</key>\n  <true/>")
		}
		out, err := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
		if err == nil {
			info.Running = true
			info.Detail = "loaded"
		} else if !info.Installed {
			info.Detail = "not installed"
		} else {
			info.Detail = strings.TrimSpace(string(out))
		}
	case "windows":
		info.Manager = "schtasks"
		out, err := exec.Command("schtasks", "/Query", "/TN", winTaskName).CombinedOutput()
		if err == nil {
			info.Installed = true
			s := string(out)
			info.Enabled = !strings.Contains(s, "Disabled")
			info.Running = strings.Contains(s, "Running")
			if info.Running {
				info.Detail = "running"
			} else {
				info.Detail = "scheduled"
			}
		} else {
			info.Detail = "not installed"
		}
	}
	return info
}

func Logs(dataDir string, lines int) (string, error) {
	if lines <= 0 {
		lines = 50
	}
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("journalctl"); err == nil {
			out, err := exec.Command("journalctl", "--user", "-u", systemdUnitName, "-n", fmt.Sprintf("%d", lines), "--no-pager").CombinedOutput()
			if err == nil && len(strings.TrimSpace(string(out))) > 0 {
				return string(out), nil
			}
		}
	}
	logFile := filepath.Join(dataDir, "logs", "apx.log")
	raw, err := os.ReadFile(logFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", logFile, err)
	}
	all := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n"), nil
}
