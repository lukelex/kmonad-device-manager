package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type doctorOutput struct {
	failures int
	waiting  int
	json     bool
	checks   []doctorCheck
	green    string
	red      string
	yellow   string
	reset    string
}

type doctorCheck struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type doctorReport struct {
	Command   string        `json:"command"`
	ConfigDir string        `json:"config_dir"`
	Healthy   bool          `json:"healthy"`
	Failures  int           `json:"failures"`
	Waiting   int           `json:"waiting"`
	Checks    []doctorCheck `json:"checks"`
}

func newDoctorOutput(jsonOutput bool) *doctorOutput {
	d := &doctorOutput{json: jsonOutput}
	if !jsonOutput && (os.Getenv("KMONAD_DOCTOR_COLOR") == "always" || (os.Getenv("KMONAD_DOCTOR_COLOR") != "never" && isTerminal(os.Stdout))) {
		d.green, d.red, d.yellow, d.reset = "\033[32m", "\033[31m", "\033[33m", "\033[0m"
	}
	return d
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (d *doctorOutput) ok(message string) {
	d.checks = append(d.checks, doctorCheck{Status: "ok", Message: message})
	if d.json {
		return
	}
	fmt.Fprintf(os.Stdout, "%s[ok]%s %s\n", d.green, d.reset, message)
}
func (d *doctorOutput) wait(message string) {
	d.waiting++
	d.checks = append(d.checks, doctorCheck{Status: "waiting", Message: message})
	if d.json {
		return
	}
	fmt.Fprintf(os.Stdout, "%s[wait]%s %s\n", d.yellow, d.reset, message)
}
func (d *doctorOutput) bad(message string) {
	d.failures++
	d.checks = append(d.checks, doctorCheck{Status: "error", Message: message})
	if d.json {
		return
	}
	fmt.Fprintf(os.Stdout, "%s[bad]%s %s\n", d.red, d.reset, message)
}

func (d *doctorOutput) finish(configDir string) int {
	if d.json {
		report := doctorReport{
			Command:   "doctor",
			ConfigDir: configDir,
			Healthy:   d.failures == 0,
			Failures:  d.failures,
			Waiting:   d.waiting,
			Checks:    d.checks,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return 1
		}
	}
	if d.failures > 255 {
		return 255
	}
	return d.failures
}

func doctor(s settings, jsonOutput bool) int {
	d := newDoctorOutput(jsonOutput)
	if !jsonOutput {
		fmt.Fprintln(os.Stdout, "KMonad Device Manager doctor")
		fmt.Fprintf(os.Stdout, "Configuration directory: %s\n", s.configDir)
	}

	if path, err := exec.LookPath(s.kmonadCommand); err == nil {
		d.ok("KMonad: " + path)
	} else {
		d.bad("KMonad: not found on PATH")
	}
	if s.pollInterval == 0 {
		d.bad(fmt.Sprintf("Poll interval: '%s' is invalid", s.pollIntervalRaw))
	} else {
		d.ok(fmt.Sprintf("Poll interval: %ss", s.pollIntervalRaw))
	}
	if s.stopTimeout == 0 {
		d.bad(fmt.Sprintf("Stop timeout: '%s' is invalid", s.stopTimeoutRaw))
	} else {
		d.ok(fmt.Sprintf("Stop timeout: %ss", s.stopTimeoutRaw))
	}
	if s.dryRunTimeout == 0 {
		d.bad(fmt.Sprintf("Dry-run timeout: '%s' is invalid", s.dryRunTimeoutRaw))
	} else {
		d.ok(fmt.Sprintf("Dry-run timeout: %ss", s.dryRunTimeoutRaw))
	}
	if s.maxConfigs == 0 {
		d.bad(fmt.Sprintf("Configuration limit: '%s' is invalid", s.maxConfigsRaw))
	} else {
		d.ok(fmt.Sprintf("Configuration limit: %s", s.maxConfigsRaw))
	}
	if s.maxConfigBytes == 0 {
		d.bad(fmt.Sprintf("Configuration size limit: '%s' is invalid", s.maxConfigBytesRaw))
	} else {
		d.ok(fmt.Sprintf("Configuration size limit: %s bytes", s.maxConfigBytesRaw))
	}
	if s.watchdogTimeout == 0 {
		d.bad(fmt.Sprintf("Watchdog timeout: '%s' is invalid", s.watchdogTimeoutRaw))
	} else {
		d.ok(fmt.Sprintf("Watchdog timeout: %ss", s.watchdogTimeoutRaw))
	}
	if inGroup("input") {
		d.ok("Group membership: input")
	} else {
		d.bad("Group membership: input is missing")
	}
	if inGroup("uinput") {
		d.ok("Group membership: uinput")
	} else {
		d.bad("Group membership: uinput is missing")
	}
	if _, err := os.Stat("/sys/module/uinput"); err == nil {
		d.ok("Kernel module: uinput is loaded")
	} else {
		d.bad("Kernel module: uinput is not loaded")
	}
	if uinputReady("/dev/uinput") {
		d.ok("Device access: /dev/uinput is readable and writable")
	} else {
		d.bad("Device access: /dev/uinput is unavailable or inaccessible")
	}

	entries, err := os.ReadDir(s.configDir)
	if err != nil {
		d.bad("Configuration directory does not exist")
	} else {
		if unsafe, securityErr := worldWritable(s.configDir); securityErr == nil && unsafe {
			d.bad("Configuration directory is writable by other users")
		} else {
			d.ok("Configuration directory exists")
		}
		configs := make([]string, 0)
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".kbd") {
				configs = append(configs, filepath.Join(s.configDir, entry.Name()))
			}
		}
		sort.Strings(configs)
		if len(configs) == 0 {
			d.bad("Configuration files: no .kbd files found")
		}
		for _, config := range configs {
			device, readErr := readDeviceFileWithLimit(config, s.maxConfigBytes)
			name := filepath.Base(config)
			if unsafe, securityErr := worldWritable(config); securityErr == nil && unsafe {
				d.bad(fmt.Sprintf("Configuration %s: writable by other users", name))
			}
			if readErr != nil {
				d.bad(fmt.Sprintf("Configuration %s: cannot read configuration", name))
				continue
			}
			if device == "" {
				d.bad(fmt.Sprintf("Configuration %s: no device-file input", name))
			} else if _, statErr := os.Stat(device); os.IsNotExist(statErr) {
				d.wait(fmt.Sprintf("Input %s: %s is not connected", name, device))
			} else if !deviceReady(device) {
				d.bad(fmt.Sprintf("Input %s: %s is inaccessible or not a character device", name, device))
			} else {
				d.ok(fmt.Sprintf("Input %s: %s", name, device))
			}
			if _, err := exec.LookPath(s.kmonadCommand); err == nil {
				cmd := exec.Command(s.kmonadCommand, "--dry-run", config)
				cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
				if err := cmd.Run(); err == nil {
					d.ok(fmt.Sprintf("Configuration %s: parses successfully", name))
				} else {
					d.bad(fmt.Sprintf("Configuration %s: KMonad validation failed", name))
				}
			}
		}
	}

	if _, err := exec.LookPath("systemctl"); err != nil {
		d.bad("Service check: systemctl is missing")
	} else {
		if exec.Command("systemctl", "--user", "is-enabled", "kmonad-device-manager.service").Run() == nil {
			d.ok("Service: enabled")
		} else {
			d.bad("Service: not enabled")
		}
		if exec.Command("systemctl", "--user", "is-active", "kmonad-device-manager.service").Run() == nil {
			d.ok("Service: active")
		} else {
			d.bad("Service: inactive")
		}
	}
	return d.finish(s.configDir)
}
