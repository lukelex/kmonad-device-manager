package manager

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type doctorOutput struct {
	failures int
	waiting  int
	json     bool
	checks   []Diagnostic
	green    string
	red      string
	yellow   string
	reset    string
}

type doctorReport struct {
	Command   string       `json:"command"`
	ConfigDir string       `json:"config_dir"`
	Healthy   bool         `json:"healthy"`
	Failures  int          `json:"failures"`
	Waiting   int          `json:"waiting"`
	Checks    []Diagnostic `json:"checks"`
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
	d.add(Diagnostic{ID: doctorDiagnosticID(message), Severity: DiagnosticOK, ReasonCode: ReasonCapabilityAvailable, Summary: message, Remediation: "No action is required."})
}
func (d *doctorOutput) wait(message string) {
	d.add(Diagnostic{ID: doctorDiagnosticID(message), Severity: DiagnosticTemporary, ReasonCode: ReasonRuntimeWaitingForDevice, Summary: message, Remediation: "Wait for the temporary condition to recover, then run doctor again."})
}
func (d *doctorOutput) bad(message string) {
	d.add(Diagnostic{ID: doctorDiagnosticID(message), Severity: DiagnosticError, ReasonCode: ReasonDependencyUnavailable, Summary: message, Remediation: "Correct the reported prerequisite, then run doctor again."})
}

func (d *doctorOutput) add(diagnostic Diagnostic) {
	switch diagnostic.Severity {
	case DiagnosticError:
		d.failures++
	case DiagnosticTemporary:
		d.waiting++
	}
	d.checks = append(d.checks, diagnostic)
	if d.json {
		return
	}
	switch diagnostic.Severity {
	case DiagnosticOK:
		fmt.Fprintf(os.Stdout, "%s[ok]%s %s\n", d.green, d.reset, diagnostic.Summary)
	case DiagnosticTemporary:
		fmt.Fprintf(os.Stdout, "%s[wait]%s %s\n", d.yellow, d.reset, diagnostic.Summary)
	case DiagnosticWarning:
		fmt.Fprintf(os.Stdout, "%s[warn]%s %s\n", d.yellow, d.reset, diagnostic.Summary)
	default:
		fmt.Fprintf(os.Stdout, "%s[bad]%s %s\n", d.red, d.reset, diagnostic.Summary)
	}
}

func doctorDiagnosticID(summary string) string {
	digest := sha256.Sum256([]byte(summary))
	return fmt.Sprintf("doctor.%x", digest[:8])
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
	if !host.Supported() {
		d.add(Diagnostic{ID: "doctor.platform", Severity: DiagnosticError, ReasonCode: ReasonPlatformUnsupported, Summary: "Platform: Linux evdev is required", Remediation: "Run KMonad Device Manager on Linux with the evdev backend."})
		return d.finish(s.configDir)
	}

	if host.KMonadAvailable(s.kmonadCommand) {
		d.ok("KMonad: available")
		diagnostic := kmonadDiagnostic(probeKMonadInfo(context.Background(), s.kmonadCommand))
		diagnostic.ID = "doctor.kmonad.version"
		d.add(diagnostic)
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
	if host.UinputModuleLoaded() {
		d.ok("Kernel module: uinput is loaded")
	} else {
		d.bad("Kernel module: uinput is not loaded")
	}
	if uinputReady(host.UinputDevice()) {
		d.ok("Device access: " + host.UinputDevice() + " is readable and writable")
	} else {
		d.bad("Device access: " + host.UinputDevice() + " is unavailable or inaccessible")
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
			} else if availability, _, reason := configuredDeviceAvailability(device); availability == DeviceDisconnected {
				d.wait(fmt.Sprintf("Input %s: %s", name, reason))
			} else if availability != DeviceConnected {
				d.bad(fmt.Sprintf("Input %s: %s", name, reason))
			} else {
				d.ok(fmt.Sprintf("Input %s: %s", name, device))
			}
			if host.KMonadAvailable(s.kmonadCommand) {
				child, err := host.StartKMonad(s.kmonadCommand, []string{"--dry-run", config}, io.Discard, io.Discard)
				if err == nil && child.Wait() == nil {
					d.ok(fmt.Sprintf("Configuration %s: parses successfully", name))
				} else {
					d.bad(fmt.Sprintf("Configuration %s: KMonad validation failed", name))
				}
			}
		}
	}

	if available, enabled, active := host.UserServiceStatus("kmonad-device-manager.service"); !available {
		d.bad("Service check: systemctl is missing")
	} else {
		if enabled {
			d.ok("Service: enabled")
		} else {
			d.bad("Service: not enabled")
		}
		if active {
			d.ok("Service: active")
		} else {
			d.bad("Service: inactive")
		}
	}
	return d.finish(s.configDir)
}
