package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

func fakeKMonad(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kmonad-test")
	script := `#!/bin/sh
if [ "${1:-}" = --dry-run ]; then
  case "$(basename "$2")" in
	    *invalid.kbd*) exit 1 ;;
  esac
  exit 0
fi
case "$(basename "$1")" in
	  *crash.kbd*) exit 1 ;;
esac
trap 'exit 0' TERM INT
while :; do sleep 0.01; done
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeKBD(t *testing.T, path, device string) {
	t.Helper()
	content := "(defcfg\n  input (device-file \"" + device + "\")\n)\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testManager(t *testing.T, configDir, command string) *manager {
	t.Helper()
	m := &manager{
		configDir:      configDir,
		kmonadCommand:  command,
		stopTimeout:    100 * time.Millisecond,
		dryRunTimeout:  100 * time.Millisecond,
		maxConfigs:     128,
		maxConfigBytes: defaultMaxConfigBytes,
		states:         make(map[string]*configState),
		duplicates:     make(map[string]string),
		commands:       make(chan managerCommand, managerCommandQueueSize),
	}
	t.Cleanup(m.cleanup)
	return m
}

func TestManagerCommandsRunOnlyThroughTheOwnerLoop(t *testing.T) {
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.run(ctx, time.Hour)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("manager owner loop did not stop")
		}
	})
	var executed atomic.Bool
	result := m.submitCommand(context.Background(), func(_ context.Context, owner *manager) commandResult {
		executed.Store(owner == m)
		return commandResult{result: "executed"}
	})
	if result.err != nil || result.result != "executed" || !executed.Load() {
		t.Fatalf("command did not execute through owner: %#v", result)
	}
}

func TestManagerCommandQueueIsBounded(t *testing.T) {
	m := &manager{commands: make(chan managerCommand, 1)}
	first := managerCommand{ctx: context.Background(), execute: func(context.Context, *manager) commandResult { return commandResult{} }, reply: make(chan commandResult, 1)}
	m.commands <- first
	result := m.submitCommand(context.Background(), func(context.Context, *manager) commandResult { return commandResult{} })
	if result.err == nil || result.err.Code != "resource_exhausted" {
		t.Fatalf("full command queue returned %#v", result)
	}
	m.executeCommand(<-m.commands)
	if result := <-first.reply; result.err != nil {
		t.Fatalf("queued command failed: %#v", result)
	}
}

func TestExpiredManagerCommandIsNotQueued(t *testing.T) {
	m := &manager{commands: make(chan managerCommand, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	result := m.submitCommand(ctx, func(context.Context, *manager) commandResult {
		called = true
		return commandResult{}
	})
	if result.err == nil || result.err.Code != "deadline_exceeded" || called || len(m.commands) != 0 {
		t.Fatalf("expired command was not rejected before enqueue: %#v", result)
	}
}

func TestDeviceRegistryRetainsDisconnectedDeviceAcrossReload(t *testing.T) {
	previous := listKeyboards
	defer func() { listKeyboards = previous }()
	identity := "serial:046d:c31c:ABC123"
	listKeyboards = func() ([]platform.KeyboardDevice, error) {
		return []platform.KeyboardDevice{{Identity: identity, FallbackIdentity: "topology:test", IdentityStability: "serial", DisplayName: "Keyboard"}}, nil
	}
	path := filepath.Join(t.TempDir(), "devices.json")
	m := &manager{devices: make(map[string]Device), deviceRegistryPath: path}
	m.refreshDevices()
	if len(m.deviceList()) != 1 {
		t.Fatalf("connected keyboard was not retained")
	}
	listKeyboards = func() ([]platform.KeyboardDevice, error) { return nil, nil }
	m.refreshDevices()
	device := m.deviceList()[0]
	if device.Availability != DeviceDisconnected || device.ReasonCode != ReasonDeviceDisconnected {
		t.Fatalf("unexpected disconnected device: %#v", device)
	}
	restarted := &manager{devices: make(map[string]Device), deviceRegistryPath: path}
	restarted.loadDeviceRegistry()
	if got := restarted.deviceList(); len(got) != 1 || got[0].ID != device.ID || got[0].Availability != DeviceDisconnected {
		t.Fatalf("registry did not survive reload: %#v", got)
	}
}

func TestDiscoverDevicesReportsPlatformAvailability(t *testing.T) {
	previous := listKeyboards
	defer func() { listKeyboards = previous }()
	listKeyboards = func() ([]platform.KeyboardDevice, error) {
		return []platform.KeyboardDevice{{
			Identity: "topology:test", IdentityStability: "topology", DisplayName: "Keyboard",
			Availability: platform.DeviceInaccessible,
		}}, nil
	}
	devices, err := discoverDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Availability != DeviceInaccessible || devices[0].ReasonCode != ReasonDeviceInaccessible {
		t.Fatalf("unexpected discovered device: %#v", devices)
	}
}

func TestDeviceRegistryReportsConflictingClaims(t *testing.T) {
	previous := listKeyboards
	defer func() { listKeyboards = previous }()
	configDir := t.TempDir()
	writeKBD(t, filepath.Join(configDir, "one.kbd"), "/dev/null")
	writeKBD(t, filepath.Join(configDir, "two.kbd"), "/dev/null")
	listKeyboards = func() ([]platform.KeyboardDevice, error) {
		return []platform.KeyboardDevice{{
			Identity: "topology:test", IdentityStability: "topology", DisplayName: "Keyboard",
			NodePath: "/dev/null", Availability: platform.DeviceConnected,
		}}, nil
	}
	m := testManager(t, configDir, fakeKMonad(t))
	m.refreshDevices()
	devices := m.deviceList()
	if len(devices) != 1 || devices[0].Availability != DeviceConflicting || devices[0].ReasonCode != ReasonDeviceConflicting || !devices[0].RuntimeConflict {
		t.Fatalf("unexpected conflicting device: %#v", devices)
	}
	if got, want := devices[0].ConfiguredBy, []string{"one.kbd", "two.kbd"}; !slices.Equal(got, want) {
		t.Fatalf("configured-by = %q, want %q", got, want)
	}
}

func TestStatusReportsUnsupportedConfiguredDevice(t *testing.T) {
	configDir := t.TempDir()
	input := filepath.Join(t.TempDir(), "not-a-device")
	if err := os.WriteFile(input, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeKBD(t, filepath.Join(configDir, "keyboard.kbd"), input)
	m := testManager(t, configDir, fakeKMonad(t))
	m.statusPath = filepath.Join(t.TempDir(), "status.json")
	m.writeStatus()
	status := readStatusFile(m.statusPath)
	if status == nil || len(status.Configurations) != 1 {
		t.Fatalf("missing status: %#v", status)
	}
	config := status.Configurations[0]
	if config.Connected || config.Availability != DeviceUnsupported || config.AvailabilityReasonCode != ReasonDeviceUnsupported || config.ReasonCode != ReasonDeviceUnsupported {
		t.Fatalf("unexpected configured-device status: %#v", config)
	}
}

func TestIdentificationPausesOnlyTheSelectedConfiguration(t *testing.T) {
	configDir := t.TempDir()
	selected := filepath.Join(configDir, "selected.kbd")
	unrelated := filepath.Join(configDir, "unrelated.kbd")
	writeKBD(t, selected, "/dev/null")
	writeKBD(t, unrelated, "/dev/zero")
	m := testManager(t, configDir, fakeKMonad(t))
	m.identification = &identificationSession{platformID: deviceIdentityForTest(t, "/dev/null")}
	m.reconcile(time.Now())
	if m.states[selected] != nil {
		t.Fatal("selected configuration started during identification")
	}
	if state := m.states[unrelated]; state == nil || state.process == nil {
		t.Fatalf("unrelated configuration did not keep running: %#v", state)
	}
	m.identification = nil
	m.reconcile(time.Now())
	if state := m.states[selected]; state == nil || state.process == nil {
		t.Fatalf("selected configuration was not restored: %#v", state)
	}
}

func TestRenderManagedConfigurationOwnsInputTarget(t *testing.T) {
	previous := listKeyboards
	defer func() { listKeyboards = previous }()
	keyboard := platform.KeyboardDevice{
		Identity: "topology:render", IdentityStability: "topology", NodePath: "/dev/null",
		Availability: platform.DeviceConnected, DisplayName: "Keyboard",
	}
	listKeyboards = func() ([]platform.KeyboardDevice, error) { return []platform.KeyboardDevice{keyboard}, nil }
	m := &manager{devices: make(map[string]Device)}
	content, result := m.renderManagedConfiguration(ManagedConfigurationModel{
		DeviceID: opaqueDeviceID(keyboard.Identity), Behavior: "(defsrc a)\n(deflayer base a)",
	})
	if result.Outcome != ValidationValid {
		t.Fatalf("unexpected render result: %#v", result)
	}
	want := "(defcfg\n  input (device-file \"/dev/null\")\n)\n(defsrc a)\n(deflayer base a)\n"
	if string(content) != want {
		t.Fatalf("rendered content = %q, want %q", content, want)
	}

	_, result = m.renderManagedConfiguration(ManagedConfigurationModel{DeviceID: opaqueDeviceID(keyboard.Identity), Behavior: "(defcfg input (device-file \"/dev/wrong\"))"})
	if result.Outcome != ValidationRejected || result.ReasonCode != ReasonCandidateUnsupported {
		t.Fatalf("input override was accepted: %#v", result)
	}
}

func TestRenderManagedConfigurationBlocksStaleAndAmbiguousDevices(t *testing.T) {
	previous := listKeyboards
	defer func() { listKeyboards = previous }()
	staleID := opaqueDeviceID("topology:stale")
	m := &manager{devices: map[string]Device{staleID: {ID: staleID, Availability: DeviceConnected}}}
	listKeyboards = func() ([]platform.KeyboardDevice, error) { return nil, nil }
	_, result := m.renderManagedConfiguration(ManagedConfigurationModel{DeviceID: staleID})
	if result.Outcome != ValidationBlocked || result.ReasonCode != ReasonDeviceDisconnected {
		t.Fatalf("stale device was not blocked: %#v", result)
	}

	first := platform.KeyboardDevice{Identity: "serial:duplicate", FallbackIdentity: "topology:duplicate", IdentityStability: "serial", NodePath: "/dev/null", Availability: platform.DeviceConnected}
	second := first
	listKeyboards = func() ([]platform.KeyboardDevice, error) { return []platform.KeyboardDevice{first, second}, nil }
	ambiguousID := opaqueDeviceID(first.FallbackIdentity)
	_, result = m.renderManagedConfiguration(ManagedConfigurationModel{DeviceID: ambiguousID})
	if result.Outcome != ValidationBlocked || result.ReasonCode != ReasonDeviceIdentityAmbiguous {
		t.Fatalf("ambiguous device was not blocked: %#v", result)
	}
}

func TestValidationPreviewUsesRuntimeSnapshotAndDetectsConflicts(t *testing.T) {
	previous := listKeyboards
	defer func() { listKeyboards = previous }()
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	keyboard := platform.KeyboardDevice{Identity: "topology:validation", IdentityStability: "topology", NodePath: "/dev/null", Availability: platform.DeviceConnected}
	listKeyboards = func() ([]platform.KeyboardDevice, error) { return []platform.KeyboardDevice{keyboard}, nil }
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	result := m.prepareValidationPreview(context.Background(), validationPreviewParams{Model: &ManagedConfigurationModel{DeviceID: opaqueDeviceID(keyboard.Identity), Behavior: "(defsrc a)"}})
	preparation, ok := result.result.(validationPreparation)
	if result.err != nil || !ok {
		t.Fatalf("validation was not prepared: %#v", result)
	}
	if filepath.Dir(preparation.snapshotPath) != runtime {
		t.Fatalf("validation snapshot escaped runtime directory: %q", preparation.snapshotPath)
	}
	if validation := runPreparedValidation(context.Background(), preparation); validation.Outcome != ValidationValid {
		t.Fatalf("prepared validation failed: %#v", validation)
	}
	if _, err := os.Stat(preparation.snapshotPath); !os.IsNotExist(err) {
		t.Fatalf("validation snapshot was not removed: %v", err)
	}

	configDir := t.TempDir()
	writeKBD(t, filepath.Join(configDir, "claimed.kbd"), "/dev/null")
	m = testManager(t, configDir, fakeKMonad(t))
	content := "(defcfg input (device-file \"/dev/null\"))"
	result = m.prepareValidationPreview(context.Background(), validationPreviewParams{Content: &content})
	validation, ok := result.result.(ValidationResult)
	if result.err != nil || !ok || validation.Outcome != ValidationBlocked || validation.ReasonCode != ReasonDeviceConflicting {
		t.Fatalf("claimed device was not blocked: %#v", result)
	}
}

func TestValidationPreviewRejectsOversizedCandidate(t *testing.T) {
	m := &manager{maxConfigBytes: 4}
	content := "oversized"
	result := m.prepareValidationPreview(context.Background(), validationPreviewParams{Content: &content})
	validation, ok := result.result.(ValidationResult)
	if result.err != nil || !ok || validation.Outcome != ValidationRejected || validation.ReasonCode != ReasonConfigurationTooLarge {
		t.Fatalf("oversized candidate result = %#v", result)
	}
}

func scriptCommand(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kmonad-test")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestEnvironmentValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte("# comment\r\n KMONAD_CONFIG_DIR = \"/tmp/kmonad\"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := environmentValue(path, "KMONAD_CONFIG_DIR")
	if err != nil {
		t.Fatal(err)
	}
	if value != "/tmp/kmonad" {
		t.Fatalf("expected /tmp/kmonad, got %q", value)
	}
}

func TestParseCLIInvocationAcceptsJSONForEveryPosition(t *testing.T) {
	tests := []struct {
		arguments []string
		expected  []string
	}{
		{arguments: []string{"--json", "--doctor"}, expected: []string{"--doctor"}},
		{arguments: []string{"--doctor", "--json"}, expected: []string{"--doctor"}},
		{arguments: []string{"ps", "--json"}, expected: []string{"ps"}},
		{arguments: []string{"--completion", "bash", "--json"}, expected: []string{"--completion", "bash"}},
		{arguments: []string{"--status=json"}, expected: []string{"--status"}},
		{arguments: []string{"--json"}, expected: []string{}},
	}
	for _, test := range tests {
		invocation, err := parseCLIInvocation(test.arguments)
		if err != nil {
			t.Fatalf("%v: %v", test.arguments, err)
		}
		if !invocation.jsonOutput || strings.Join(invocation.args, "\x00") != strings.Join(test.expected, "\x00") {
			t.Fatalf("%v: unexpected invocation %#v", test.arguments, invocation)
		}
	}
	if _, err := parseCLIInvocation([]string{"--json", "--json"}); err == nil {
		t.Fatal("duplicate --json was accepted")
	}
}

func TestEveryPublicCommandDocumentsJSONOutput(t *testing.T) {
	document := helpDocument()
	if len(document.Commands) == 0 {
		t.Fatal("command index is empty")
	}
	for _, command := range document.Commands {
		found := false
		for _, option := range command.Options {
			if option.Syntax == "--json" {
				found = true
				break
			}
		}
		if !found || command.JSONOutput == "" {
			t.Fatalf("command %q does not fully document JSON output", command.Name)
		}
	}
}

func TestPublicCommandDocumentationSurfacesStayIndexed(t *testing.T) {
	wiki, err := os.ReadFile(filepath.Join("..", "..", "wiki", "Command-Reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	manual, err := os.ReadFile(filepath.Join("..", "..", "docs", "kmonad-device-manager.1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range commandHelp {
		if !bytes.Contains(wiki, []byte(command.Invocation)) {
			t.Errorf("GitHub Wiki command index is missing invocation %q", command.Invocation)
		}
		manualSection := ".SS " + strings.ToUpper(command.Name)
		if !bytes.Contains(manual, []byte(manualSection)) {
			t.Errorf("man page is missing section %q", manualSection)
		}
	}
	for name, data := range map[string][]byte{"GitHub Wiki": wiki, "man page": manual} {
		if !bytes.Contains(data, []byte("--json")) {
			t.Errorf("%s does not document --json", name)
		}
	}
}

func TestManagerAPIV1ContractDefinesCoreSafetyRequirements(t *testing.T) {
	contract, err := os.ReadFile(filepath.Join("..", "..", "wiki", "Manager-API-v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range []string{
		"# Manager API v1",
		"`session.hello`",
		"`snapshot.get`",
		"`events.subscribe`",
		"`idempotency_key`",
		"`expected_revision`",
		"`stale_revision`",
		"Unix peer credentials",
		"api.sock",
		"The manager must build, start, reconcile, supervise, recover, and stop",
		"must not block reconciliation",
		"Breaking changes to this contract or its implementation are acceptable",
		"established systemd service behavior",
		"## Stable domain schema",
		"### ValidationResult",
		"### Diagnostic",
		"### Capability",
		"### Operation",
		"### Event",
		"runtime_pending_update_rejected",
		"reason_code",
	} {
		if !bytes.Contains(contract, []byte(requirement)) {
			t.Errorf("Manager API v1 contract is missing %q", requirement)
		}
	}
}

func TestDomainTypesKeepMachineStateSeparateFromDisplayText(t *testing.T) {
	retryAt := time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)
	configuration := Configuration{
		ID: "cfg_01J", Name: "Laptop keyboard", Ownership: ConfigurationManaged,
		Enabled: true, DeviceID: "dev_01J", DesiredRevision: 7, ActiveRevision: 6,
		Runtime: RuntimeState{
			Phase: RuntimeRunning, ReasonCode: ReasonRuntimePendingUpdateRejected,
			Reason: "display text", Connected: true, Healthy: true, RetryAt: &retryAt, FailureCount: 1,
		},
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"\"phase\":\"running\"", "\"reason_code\":\"runtime_pending_update_rejected\"", "\"reason\":\"display text\"", "\"retry_at\":"} {
		if !bytes.Contains(data, []byte(field)) {
			t.Errorf("domain JSON is missing %s: %s", field, data)
		}
	}
	if bytes.Contains(data, []byte("/dev/")) || bytes.Contains(data, []byte("process_id")) {
		t.Fatalf("public domain JSON leaked a platform locator: %s", data)
	}
}

func TestManagerCoreDoesNotContainPlatformPrimitives(t *testing.T) {
	files := []string{
		"api_client.go", "api_transport.go", "commands.go", "devices.go", "domain.go", "identify.go", "render.go", "service.go", "settings.go", "state.go", "process.go", "supervisor.go", "validation.go", "validation_api.go",
		"runtime.go", "doctor.go", "status.go",
	}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, primitive := range []string{
			"syscall", "golang.org/x/sys/unix", "/proc", "/dev/uinput",
			"NOTIFY_SOCKET", "WATCHDOG_USEC", "os/user", "DialUnix", "SysProcAttr",
			"/sys/module/uinput",
		} {
			if bytes.Contains(data, []byte(primitive)) {
				t.Errorf("%s contains platform primitive %q; use internal/platform", name, primitive)
			}
		}
	}
}

func TestAPITransportDoesNotAccessManagerStateDirectly(t *testing.T) {
	data, err := os.ReadFile("api_transport.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, directAccess := range []string{".states", ".duplicates", ".watchPaths"} {
		if bytes.Contains(data, []byte(directAccess)) {
			t.Errorf("API transport accesses manager state directly through %q", directAccess)
		}
	}
}

func TestExecutableIsThinManagerBootstrap(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "cmd", "kmonad-device-manager", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"manager.Run", "signal.NotifyContext", "platform.Default"} {
		if !bytes.Contains(data, []byte(required)) {
			t.Errorf("executable bootstrap is missing %q", required)
		}
	}
}

func TestHelpVersionAndCompletionJSON(t *testing.T) {
	var output bytes.Buffer
	if err := writeHelp(&output, true); err != nil {
		t.Fatal(err)
	}
	var help cliHelpDocument
	if err := json.Unmarshal(output.Bytes(), &help); err != nil || help.Program != "kmonad-device-manager" || len(help.Commands) != len(commandHelp) {
		t.Fatalf("unexpected JSON help: %s, %v", output.String(), err)
	}

	output.Reset()
	if err := writeVersion(&output, true); err != nil {
		t.Fatal(err)
	}
	var versionOutput map[string]string
	if err := json.Unmarshal(output.Bytes(), &versionOutput); err != nil || versionOutput["version"] != version {
		t.Fatalf("unexpected JSON version: %s, %v", output.String(), err)
	}

	output.Reset()
	if err := writeCompletion(&output, "bash", "complete-definition", true); err != nil {
		t.Fatal(err)
	}
	var completionOutput map[string]string
	if err := json.Unmarshal(output.Bytes(), &completionOutput); err != nil || completionOutput["shell"] != "bash" || completionOutput["completion"] != "complete-definition" {
		t.Fatalf("unexpected JSON completion: %s, %v", output.String(), err)
	}
}

func TestDoctorJSONIsStructuredAndUncolored(t *testing.T) {
	t.Setenv("KMONAD_DOCTOR_COLOR", "always")
	s := settings{
		configDir:          filepath.Join(t.TempDir(), "missing"),
		kmonadCommand:      "definitely-missing-kmonad",
		pollIntervalRaw:    "2",
		stopTimeoutRaw:     "5",
		dryRunTimeoutRaw:   "30",
		watchdogTimeoutRaw: "60",
		maxConfigsRaw:      "128",
		maxConfigBytesRaw:  "1048576",
		pollInterval:       2 * time.Second,
		stopTimeout:        5 * time.Second,
		dryRunTimeout:      30 * time.Second,
		watchdogTimeout:    60 * time.Second,
		maxConfigs:         128,
		maxConfigBytes:     defaultMaxConfigBytes,
	}
	output := captureStdout(t, func() { _ = doctor(s, true) })
	var report doctorReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("doctor did not emit JSON: %q: %v", output, err)
	}
	if report.Command != "doctor" || report.Healthy || report.Failures == 0 || len(report.Checks) == 0 {
		t.Fatalf("unexpected doctor report: %#v", report)
	}
	if strings.Contains(output, "\033[") {
		t.Fatalf("JSON doctor output contains ANSI color: %q", output)
	}
}

func TestLoadSettingsReadsEnvironment(t *testing.T) {
	t.Setenv("KMONAD_CONFIG_DIR", "/tmp/kmonad")
	t.Setenv("KMONAD_COMMAND", "kmonad-test")
	t.Setenv("KMONAD_POLL_INTERVAL", "3")
	t.Setenv("KMONAD_STOP_TIMEOUT", "4")
	t.Setenv("KMONAD_DRY_RUN_TIMEOUT", "5")
	t.Setenv("KMONAD_WATCHDOG_TIMEOUT", "6")
	t.Setenv("KMONAD_MAX_CONFIGS", "7")
	t.Setenv("KMONAD_MAX_CONFIG_BYTES", "2048")
	t.Setenv("KMONAD_METRICS_ADDR", "127.0.0.1:9090")
	t.Setenv("KMONAD_CGROUP_ROOT", "/tmp/cgroup")
	t.Setenv("KMONAD_PROCESS_MEMORY_MAX", "64M")
	t.Setenv("KMONAD_PROCESS_CPU_MAX", "50%")
	s := loadSettings()
	if s.configDir != "/tmp/kmonad" || s.kmonadCommand != "kmonad-test" || s.pollInterval != 3*time.Second || s.stopTimeout != 4*time.Second || s.dryRunTimeout != 5*time.Second || s.watchdogTimeout != 6*time.Second || s.maxConfigs != 7 || s.maxConfigBytes != 2048 || s.metricsAddr != "127.0.0.1:9090" || s.cgroupRoot != "/tmp/cgroup" || s.processMemoryMax != "64M" || s.processCPUQuota != "50%" {
		t.Fatalf("unexpected settings: %#v", s)
	}
}

func TestValidateSettingsRejectsInvalidValues(t *testing.T) {
	valid := settings{pollInterval: time.Second, stopTimeout: time.Second, dryRunTimeout: time.Second, watchdogTimeout: time.Second, maxConfigs: 1, maxConfigBytes: 1}
	if err := validateSettings(valid); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	for _, field := range []string{"poll", "stop", "dry-run", "watchdog", "configs", "config-bytes"} {
		s := valid
		switch field {
		case "poll":
			s.pollInterval = 0
		case "stop":
			s.stopTimeout = 0
		case "dry-run":
			s.dryRunTimeout = 0
		case "watchdog":
			s.watchdogTimeout = 0
		case "configs":
			s.maxConfigs = 0
		case "config-bytes":
			s.maxConfigBytes = 0
		}
		if err := validateSettings(s); err == nil {
			t.Fatalf("%s setting was accepted", field)
		}
	}
}

func TestRetryDelayUsesBoundedJitter(t *testing.T) {
	previous := retryJitter
	defer func() { retryJitter = previous }()

	retryJitter = func(max time.Duration) time.Duration { return 0 }
	minimum := retryDelay(1)
	retryJitter = func(max time.Duration) time.Duration { return max }
	maximum := retryDelay(1)
	base := 2 * time.Second
	if minimum < base-base/8 || maximum > base+base/8 || minimum >= maximum {
		t.Fatalf("retry jitter escaped bounds: minimum=%s maximum=%s", minimum, maximum)
	}
	if retryDelay(6) < 45*time.Second || retryDelay(6) > 75*time.Second {
		t.Fatal("maximum backoff jitter escaped its bounds")
	}
}

func TestReadConfigRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.kbd")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readConfigWithLimit(path, 5); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size-limit error, got %v", err)
	}
}

func TestValidateSettingsRestrictsMetricsToLoopback(t *testing.T) {
	valid := settings{pollInterval: time.Second, stopTimeout: time.Second, dryRunTimeout: time.Second, watchdogTimeout: time.Second, maxConfigs: 1, maxConfigBytes: 1}
	for _, address := range []string{"127.0.0.1:9090", "[::1]:9090", "localhost:9090"} {
		valid.metricsAddr = address
		if err := validateSettings(valid); err != nil {
			t.Fatalf("loopback metrics address %q rejected: %v", address, err)
		}
	}
	valid.metricsAddr = ":9090"
	if err := validateSettings(valid); err == nil {
		t.Fatal("wildcard metrics address was accepted without opt-in")
	}
	valid.metricsAllowRemote = true
	if err := validateSettings(valid); err != nil {
		t.Fatalf("explicit remote metrics opt-in rejected: %v", err)
	}
}

func TestRuntimeDirUsesEnvironment(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/runtime")
	path, err := runtimeDir()
	if err != nil || path != "/tmp/runtime" {
		t.Fatalf("unexpected runtime directory: %q, %v", path, err)
	}
}

func TestShowStatusJSONVerifiesManagerIdentity(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	command := exec.Command("bash", "-c", "exec -a kmonad-device-manager sleep 10")
	host.ConfigureChild(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		signalProcessID(command.Process.Pid, platform.SignalKill)
		_ = command.Wait()
	})
	pid := command.Process.Pid
	status := statusFile{PID: pid, ProcessStart: processStartTime(pid), UpdatedAt: time.Now(), ConfigDir: "/tmp/kmonad"}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime, "status.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		if code := showStatus(true); code != 0 {
			t.Fatalf("unexpected status exit code: %d", code)
		}
	})
	var actual statusFile
	if err := json.Unmarshal([]byte(output), &actual); err != nil || actual.PID != pid || actual.ProcessStart != status.ProcessStart {
		t.Fatalf("unexpected JSON status: %q, %v", output, err)
	}
	status.ProcessStart++
	data, err = json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime, "status.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := showStatus(false); code != 3 {
		t.Fatalf("stale manager identity returned %d", code)
	}
}

func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = previous })
	run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDryRunTimesOut(t *testing.T) {
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then sleep 10; fi`)
	m := testManager(t, t.TempDir(), command)
	m.dryRunTimeout = 20 * time.Millisecond
	started := time.Now()
	err := m.dryRun("config.kbd")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected dry-run timeout, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("dry-run timeout took too long")
	}
}

func TestDryRunTimeoutKillsDescendants(t *testing.T) {
	childPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("KMONAD_CHILD_PID_FILE", childPath)
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then
  (trap '' TERM; while :; do sleep 1; done) &
  echo $! > "$KMONAD_CHILD_PID_FILE"
  while :; do sleep 1; done
fi`)
	m := testManager(t, t.TempDir(), command)
	m.dryRunTimeout = 20 * time.Millisecond
	if err := m.dryRun("config.kbd"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected dry-run timeout, got %v", err)
	}
	data, err := os.ReadFile(childPath)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !pidExists(childPID) })
}

func TestValidationHelper(t *testing.T) {
	if os.Getenv("KMONAD_VALIDATION_HELPER") != "1" {
		return
	}
	m := &manager{
		kmonadCommand: os.Getenv("KMONAD_VALIDATION_COMMAND"),
		dryRunTimeout: 10 * time.Second,
	}
	_ = m.dryRun(os.Getenv("KMONAD_VALIDATION_CONFIG"))
}

func TestManagerTerminationDuringValidationStopsDryRun(t *testing.T) {
	childPath := filepath.Join(t.TempDir(), "validation.pid")
	t.Setenv("KMONAD_VALIDATION_PID_FILE", childPath)
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then
  echo $$ > "$KMONAD_VALIDATION_PID_FILE"
  while :; do :; done
fi`)
	helper := exec.Command(os.Args[0], "-test.run=TestValidationHelper", "-test.v")
	helper.Env = append(os.Environ(),
		"KMONAD_VALIDATION_HELPER=1",
		"KMONAD_VALIDATION_COMMAND="+command,
		"KMONAD_VALIDATION_CONFIG="+filepath.Join(t.TempDir(), "validation.kbd"),
	)
	host.ConfigureChild(helper)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if helper.ProcessState == nil || !helper.ProcessState.Exited() {
			_ = helper.Process.Kill()
		}
		_ = helper.Wait()
	})
	waitFor(t, func() bool {
		_, err := os.Stat(childPath)
		return err == nil
	})
	data, err := os.ReadFile(childPath)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	waitFor(t, func() bool { return !pidExists(childPID) })
}

func TestStopProcessKillsTERMResistantProcessGroup(t *testing.T) {
	command := scriptCommand(t, `trap '' TERM INT; while :; do sleep 1; done`)
	m := testManager(t, t.TempDir(), command)
	config := filepath.Join(m.configDir, "stubborn.kbd")
	cmd := exec.Command(command, config)
	host.ConfigureChild(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := &processState{cmd: cmd, done: make(chan struct{}), startTick: processStartTime(cmd.Process.Pid)}
	state := &configState{phase: phaseRunning, process: process}
	waitFor(t, func() bool { return processCommandLine(cmd.Process.Pid) != "" })
	if !m.ownsProcess(config, process) {
		t.Fatalf("expected test process to be owned: start=%d current=%d cmdline=%q", process.startTick, processStartTime(cmd.Process.Pid), processCommandLine(cmd.Process.Pid))
	}
	go func() { _ = cmd.Wait(); close(process.done) }()
	m.stopProcess(config, state, time.Now().Add(20*time.Millisecond))
	if pidExists(cmd.Process.Pid) {
		t.Fatal("TERM-resistant process group was not killed")
	}
}

func TestStopProcessRefusesReplacedProcessIdentity(t *testing.T) {
	command := scriptCommand(t, `trap '' TERM INT; while :; do sleep 1; done`)
	m := testManager(t, t.TempDir(), command)
	config := filepath.Join(m.configDir, "replaced.kbd")
	cmd := exec.Command(command, config)
	host.ConfigureChild(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		signalProcessID(cmd.Process.Pid, platform.SignalKill)
		_ = cmd.Wait()
	})
	process := newProcessState(cmd)
	process.startTick++
	state := &configState{phase: phaseRunning, process: process}
	m.stopProcess(config, state, time.Now().Add(20*time.Millisecond))
	if !pidExists(cmd.Process.Pid) {
		t.Fatal("identity mismatch caused the replacement process to be signaled")
	}
	signalProcessID(cmd.Process.Pid, platform.SignalKill)
	_ = cmd.Wait()
}

func TestStopAllUsesOneGlobalDeadline(t *testing.T) {
	command := scriptCommand(t, `trap '' TERM INT; while :; do sleep 1; done`)
	m := testManager(t, t.TempDir(), command)
	m.stopTimeout = 50 * time.Millisecond
	for _, name := range []string{"one.kbd", "two.kbd"} {
		config := filepath.Join(m.configDir, name)
		cmd := exec.Command(command, config)
		host.ConfigureChild(cmd)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		process := &processState{cmd: cmd, done: make(chan struct{}), startTick: processStartTime(cmd.Process.Pid)}
		state := &configState{phase: phaseRunning, process: process}
		m.states[config] = state
		go func() {
			_ = cmd.Wait()
			close(process.done)
		}()
	}
	started := time.Now()
	m.stopAll(started.Add(m.stopTimeout))
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("stopAll exceeded shared deadline: %s", elapsed)
	}
	if len(m.states) != 0 {
		t.Fatalf("stopAll left states behind: %#v", m.states)
	}
}

func TestExitedProcessKillsDescendants(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "descendant.kbd")
	writeKBD(t, config, "/dev/null")
	childPath := filepath.Join(root, "child.pid")
	t.Setenv("KMONAD_CHILD_PID_FILE", childPath)
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then exit 0; fi
(trap '' TERM; while :; do sleep 1; done) &
echo $! > "$KMONAD_CHILD_PID_FILE"
exit 1`)
	m := testManager(t, root, command)
	m.reconcile(time.Now())
	state := m.states[config]
	if state == nil || state.process == nil {
		t.Fatal("expected descendant test process to start")
	}
	waitFor(t, func() bool { return !processStillRunning(state.process) })
	data, err := os.ReadFile(childPath)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	m.reconcile(time.Now())
	waitFor(t, func() bool { return !pidExists(childPID) })
}

func TestPidfdSignalTracksTheStartedProcess(t *testing.T) {
	cmd := exec.Command("sleep", "10")
	host.ConfigureChild(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	processInfo := host.StartedProcess(cmd.Process.Pid)
	process := &processState{cmd: cmd, pidfd: processInfo.Handle}
	if process.pidfd == nil {
		signalProcessID(cmd.Process.Pid, platform.SignalKill)
		_ = cmd.Wait()
		t.Skip("pidfds are unavailable on this kernel")
	}
	t.Cleanup(func() {
		signalProcess(process, platform.SignalKill)
		_ = cmd.Wait()
		closeProcessFD(process)
	})
	if err := signalProcessHandle(process.pidfd, platform.SignalTerminate); err != nil {
		t.Fatalf("pidfd signal failed: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		if !strings.Contains(err.Error(), "signal: terminated") {
			t.Fatalf("unexpected process exit: %v", err)
		}
	}
}

func TestConfigurationStateMachineReachesRunning(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	if state := m.states[config]; state == nil || state.phase != phaseRunning {
		t.Fatalf("expected running phase, got %#v", state)
	}
}

func TestStateMachineRejectsInvalidTransition(t *testing.T) {
	state := &configState{phase: phaseRunning}
	if transitionPhase(state, phaseValidating) {
		t.Fatal("running configuration must not transition directly to validating")
	}
	if state.phase != phaseRunning {
		t.Fatalf("invalid transition changed phase to %q", state.phase)
	}
}

func TestReconcileStopsProcessAfterPermissionChange(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	state := &configState{phase: phaseRunning}
	m.states[config] = state
	if err := os.Chmod(config, 0o666); err != nil {
		t.Fatal(err)
	}
	m.reconcile(time.Now())
	if _, ok := m.states[config]; ok {
		t.Fatal("permission change should stop and remove the configuration state")
	}
}

func TestRunFallsBackWhenWatcherInitializationFails(t *testing.T) {
	previous := newWatcher
	newWatcher = func() (*fsnotify.Watcher, error) { return nil, errors.New("injected watcher failure") }
	defer func() { newWatcher = previous }()
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	m.run(ctx, time.Millisecond)
}

func TestMetricsExposeCounters(t *testing.T) {
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	m.reconciles.Store(3)
	m.starts.Store(2)
	m.failures.Store(1)
	m.metricsServerUp.Store(true)
	m.metricsFailures.Store(2)
	m.statusFailures.Store(4)
	record := httptest.NewRecorder()
	metricsHandler(m)(record, httptest.NewRequest("GET", "/metrics", nil))
	body := record.Body.String()
	for _, metric := range []string{
		"kmonad_manager_reconciles_total 3",
		"kmonad_manager_process_starts_total 2",
		"kmonad_manager_failures_total 1",
		"kmonad_manager_metrics_server_up 1",
		"kmonad_manager_metrics_server_failures_total 2",
		"kmonad_manager_status_write_failures_total 4",
	} {
		if !strings.Contains(body, metric) {
			t.Fatalf("missing metric %q in %s", metric, body)
		}
	}
}

func TestMetricsServerUsesBoundedHTTPSettings(t *testing.T) {
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	server, err := startMetricsServer(m, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if !m.metricsServerUp.Load() {
		t.Fatal("metrics server was not marked up")
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	if server.ReadHeaderTimeout != 5*time.Second || server.IdleTimeout != 30*time.Second || server.MaxHeaderBytes != 8<<10 {
		t.Fatalf("unexpected metrics server limits: %#v", server)
	}
}

func TestMetricsServerBindFailureIsCounted(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	if _, err := startMetricsServer(m, listener.Addr().String()); err == nil {
		t.Fatal("metrics server unexpectedly bound an occupied address")
	}
	if m.metricsFailures.Load() != 1 {
		t.Fatalf("expected one metrics failure, got %d", m.metricsFailures.Load())
	}
}

func TestRestoreBackoffFromStatus(t *testing.T) {
	m := testManager(t, "/tmp/kmonad-config", fakeKMonad(t))
	when := time.Now().Add(time.Minute).Truncate(time.Second)
	m.restoreBackoff(&statusFile{Configurations: []statusConfig{{
		Name:                   "keyboard.kbd",
		Failures:               3,
		RetryAfter:             when,
		FailureReason:          "validation failed",
		LastKnownGoodSignature: "known-good",
	}}})
	state := m.states[filepath.Join(m.configDir, "keyboard.kbd")]
	if state == nil || state.failures != 3 || !state.retryAfter.Equal(when) || state.failureReason != "validation failed" || state.lastKnownGoodSignature != "known-good" || state.phase != phaseFailed {
		t.Fatalf("backoff was not restored: %#v", state)
	}
}

func TestRecoveryContextIsPersistedWithStatus(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	m.statusPath = filepath.Join(root, "status.json")
	m.states[config] = &configState{
		phase:                  phaseFailed,
		failures:               2,
		failureReason:          "validation failed: syntax error",
		lastKnownGoodSignature: "abc123",
		retryAfter:             time.Now().Add(time.Minute),
	}
	m.writeStatus()
	status := readStatusFile(m.statusPath)
	if status == nil || len(status.Configurations) != 1 {
		t.Fatalf("recovery context was not written: %#v", status)
	}
	item := status.Configurations[0]
	if item.FailureReason != "validation failed: syntax error" || item.LastKnownGoodSignature != "abc123" {
		t.Fatalf("unexpected persisted recovery context: %#v", item)
	}

	restored := testManager(t, configDir, fakeKMonad(t))
	restored.restoreBackoff(status)
	state := restored.states[config]
	if state == nil || state.failureReason != item.FailureReason || state.lastKnownGoodSignature != item.LastKnownGoodSignature {
		t.Fatalf("recovery context was not restored: %#v", state)
	}
}

func TestStatusIncludesConnectionAndHealthDetails(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	m.statusPath = filepath.Join(root, "status.json")
	m.reconcile(time.Now())
	status := readStatusFile(m.statusPath)
	if status == nil || len(status.Configurations) != 1 {
		t.Fatalf("missing status details: %#v", status)
	}
	item := status.Configurations[0]
	if !item.Connected || !item.Healthy || item.State != "running" || item.Reason != "process healthy" || item.LaunchPath == "" || item.LaunchPath == config {
		t.Fatalf("unexpected status details: %#v", item)
	}
}

func TestWriteStatusCreatesDurableAtomicStatus(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, configDir, fakeKMonad(t))
	m.statusPath = filepath.Join(root, "status.json")
	m.writeStatus()
	if m.statusFailures.Load() != 0 {
		t.Fatalf("status write failed: %d", m.statusFailures.Load())
	}
	if readStatusFile(m.statusPath) == nil {
		t.Fatal("status file was not persisted")
	}
	if _, err := os.Stat(m.statusPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary status file remains: %v", err)
	}
}

func TestWriteStatusSurvivesMissingConfigurationDirectory(t *testing.T) {
	root := t.TempDir()
	m := testManager(t, filepath.Join(root, "missing"), fakeKMonad(t))
	m.statusPath = filepath.Join(root, "status.json")
	m.writeStatus()
	if m.statusFailures.Load() != 0 {
		t.Fatalf("missing configuration directory was treated as a write failure: %d", m.statusFailures.Load())
	}
	status := readStatusFile(m.statusPath)
	if status == nil || len(status.Configurations) != 0 {
		t.Fatalf("unexpected status for missing configuration directory: %#v", status)
	}
}

func TestWriteStatusReportsPersistenceFailures(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, configDir, fakeKMonad(t))

	m.statusPath = filepath.Join(root, "missing", "status.json")
	m.writeStatus()
	if m.statusFailures.Load() != 1 {
		t.Fatalf("expected status write failure for missing parent, got %d", m.statusFailures.Load())
	}

	m.statusPath = filepath.Join(root, "status-directory")
	if err := os.Mkdir(m.statusPath, 0o700); err != nil {
		t.Fatal(err)
	}
	m.writeStatus()
	if m.statusFailures.Load() != 2 {
		t.Fatalf("expected status rename failure, got %d", m.statusFailures.Load())
	}
	if _, err := os.Stat(m.statusPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary status file remains after rename failure: %v", err)
	}

	badConfig := filepath.Join(root, "config-file")
	if err := os.WriteFile(badConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.configDir = badConfig
	m.statusPath = filepath.Join(root, "status.json")
	m.writeStatus()
	if m.statusFailures.Load() != 3 {
		t.Fatalf("expected status read-directory failure, got %d", m.statusFailures.Load())
	}
}

func TestProcessOwnershipRejectsChangedIdentity(t *testing.T) {
	command := fakeKMonad(t)
	m := testManager(t, t.TempDir(), command)
	cmd := exec.Command(command, filepath.Join(m.configDir, "keyboard.kbd"))
	host.ConfigureChild(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := &processState{cmd: cmd, startTick: processStartTime(cmd.Process.Pid) + 1}
	if m.ownsProcess("keyboard.kbd", process) {
		t.Fatal("changed process identity should not be owned")
	}
	signalProcessID(cmd.Process.Pid, platform.SignalKill)
	_ = cmd.Wait()
}

func TestProcessOwnershipRejectsSubstringArguments(t *testing.T) {
	command := fakeKMonad(t)
	root := t.TempDir()
	config := filepath.Join(root, "keyboard.kbd")
	cmd := exec.Command(command, config)
	host.ConfigureChild(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := &processState{cmd: cmd, startTick: processStartTime(cmd.Process.Pid)}
	t.Cleanup(func() {
		signalProcessID(cmd.Process.Pid, platform.SignalKill)
		_ = cmd.Wait()
	})
	m := testManager(t, root, command)
	if m.ownsProcess(filepath.Join(root, "board.kbd"), process) {
		t.Fatal("a substring match must not claim a different configuration")
	}
}

func TestConfigDeletionDuringValidationDoesNotStartProcess(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	t.Setenv("KMONAD_TEST_CONFIG_PATH", config)
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then rm -f "$KMONAD_TEST_CONFIG_PATH"; fi`)
	m := testManager(t, root, command)
	state := &configState{phase: phaseDiscovered, deviceID: deviceIdentityForTest(t, "/dev/null")}
	_, signature, err := readConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	m.startConfig(config, state, time.Now(), signature, nil)
	if state.process != nil || state.phase != phaseWaiting {
		t.Fatalf("deleted configuration was started: %#v", state)
	}
}

func TestDeviceRemovalDuringStartupDoesNotStartProcess(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	t.Setenv("KMONAD_TEST_CONFIG_PATH", config)
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then sed -i 's#/dev/null#/dev/does-not-exist#' "$KMONAD_TEST_CONFIG_PATH"; fi`)
	m := testManager(t, root, command)
	state := &configState{phase: phaseDiscovered, deviceID: deviceIdentityForTest(t, "/dev/null")}
	_, signature, err := readConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	m.startConfig(config, state, time.Now(), signature, nil)
	if state.process != nil || state.phase != phaseWaiting {
		t.Fatalf("removed device configuration was started: %#v", state)
	}
}

func TestConfigurationChangeDuringValidationDoesNotStartChangedFile(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	t.Setenv("KMONAD_TEST_CONFIG_PATH", config)
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then printf '(invalid\n' > "$KMONAD_TEST_CONFIG_PATH"; fi`)
	m := testManager(t, root, command)
	state := &configState{phase: phaseDiscovered, deviceID: deviceIdentityForTest(t, "/dev/null")}
	_, signature, err := readConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	m.startConfig(config, state, time.Now(), signature, nil)
	if state.process != nil || state.phase != phaseWaiting {
		t.Fatalf("changed configuration was started: %#v", state)
	}
}

func TestConfigSnapshotRemainsImmutableAfterSourceRewrite(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "keyboard.kbd")
	original := []byte("(defcfg input (device-file \"/dev/null\"))\n")
	if err := os.WriteFile(config, original, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := createConfigSnapshot(config, original)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeConfigSnapshot(snapshot) })
	if err := os.WriteFile(config, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("snapshot changed with source rewrite: %q", data)
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("snapshot permissions are not read-only: %o", info.Mode().Perm())
	}
}

func TestStartConfigLaunchesValidatedSnapshot(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then
  [ "$(stat -c '%a' "$2")" = 400 ]
  exit
fi
trap 'exit 0' TERM INT
while :; do sleep 0.01; done`)
	m := testManager(t, root, command)
	state := &configState{phase: phaseDiscovered, deviceID: deviceIdentityForTest(t, "/dev/null")}
	_, signature, err := readConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	m.startConfig(config, state, time.Now(), signature, nil)
	if state.process == nil {
		t.Fatalf("validated snapshot was not launched: %#v", state)
	}
	if state.process.launchPath == config || !strings.HasPrefix(filepath.Base(state.process.launchPath), configSnapshotPrefix) {
		t.Fatalf("unexpected launch path: %q", state.process.launchPath)
	}
	if _, err := os.Stat(state.process.launchPath); err != nil {
		t.Fatalf("launch snapshot is unavailable: %v", err)
	}
	launchPath := state.process.launchPath
	m.stopProcess(config, state, time.Now().Add(time.Second))
	if _, err := os.Stat(launchPath); !os.IsNotExist(err) {
		t.Fatalf("launch snapshot was not removed: %v", err)
	}
}

func deviceIdentityForTest(t *testing.T, path string) string {
	t.Helper()
	identity, err := deviceID(path)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestCorruptStatusIsIgnoredAndRemovedDuringRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if readStatusFile(path) != nil {
		t.Fatal("corrupt status should not decode")
	}
	recoverOwnedProcesses(path, fakeKMonad(t))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("corrupt status was not removed: %v", err)
	}
}

func TestAcquireLockReportsRuntimeDirectoryFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", path)
	if _, _, err := acquireLock(); err == nil {
		t.Fatal("expected runtime directory creation failure")
	}
}

func TestWatcherLossIsRecoveredByRecreatingWatcher(t *testing.T) {
	previous := newWatcher
	var calls atomic.Int32
	newWatcher = func() (*fsnotify.Watcher, error) {
		if calls.Add(1) == 1 {
			watcher, err := fsnotify.NewWatcher()
			if err == nil {
				_ = watcher.Close()
			}
			return watcher, err
		}
		return nil, errors.New("watcher unavailable")
	}
	defer func() { newWatcher = previous }()
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	m.run(ctx, time.Millisecond)
	if calls.Load() < 2 {
		t.Fatalf("watcher was not recreated after loss: %d attempts", calls.Load())
	}
}

func TestSystemdNotifySendsDatagram(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "notify.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", socketPath)
	systemdNotify("READY=1")
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	n, _, err := listener.ReadFromUnix(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:n]) != "READY=1\n" {
		t.Fatalf("unexpected notification: %q", buffer[:n])
	}
}

func TestSystemdWatchdogSendsHeartbeat(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "notify.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", socketPath)
	t.Setenv("WATCHDOG_USEC", "20000")
	var progress atomic.Int64
	progress.Store(time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go systemdWatchdog(ctx, &progress)
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	n, _, err := listener.ReadFromUnix(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:n]) != "WATCHDOG=1\n" {
		t.Fatalf("unexpected watchdog notification: %q", buffer[:n])
	}
}

func TestSystemdWatchdogSuppressesStaleProgress(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "notify.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", socketPath)
	t.Setenv("WATCHDOG_USEC", "20000")
	var progress atomic.Int64
	progress.Store(time.Now().Add(-time.Second).UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go systemdWatchdog(ctx, &progress)
	if err := listener.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	if _, _, err := listener.ReadFromUnix(buffer); err == nil {
		t.Fatal("stale progress unexpectedly produced a watchdog heartbeat")
	}
}

func TestLastKnownGoodProcessSurvivesInvalidConfigurationUpdate(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ] && grep -q invalid "$2"; then exit 1; fi; if [ "${1:-}" != --dry-run ]; then trap 'exit 0' TERM INT; while :; do sleep 0.01; done; fi`)
	m := testManager(t, root, command)
	m.reconcile(time.Now())
	state := m.states[config]
	if state == nil || state.process == nil {
		t.Fatal("initial known-good configuration did not start")
	}
	pid := state.process.cmd.Process.Pid
	if err := os.WriteFile(config, []byte("invalid (defcfg input (device-file \"/dev/null\"))\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.reconcile(time.Now())
	if state.process == nil || state.process.cmd.Process.Pid != pid {
		t.Fatal("invalid update displaced the last-known-good process")
	}
	if state.pendingSignature == "" {
		t.Fatal("invalid update was not recorded as pending")
	}
}

func TestAttachProcessCgroupWritesLimitsAndPID(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "keyboard.kbd")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory.max", "cpu.max", "cgroup.procs"} {
		if err := os.WriteFile(filepath.Join(child, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	m.cgroupRoot = root
	m.processMemoryMax = "64M"
	m.processCPUQuota = "50000 100000"
	if err := m.attachProcessCgroup(filepath.Join(m.configDir, "keyboard.kbd"), 1234); err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string]string{"memory.max": "64M\n", "cpu.max": "50000 100000\n", "cgroup.procs": "1234\n"} {
		data, err := os.ReadFile(filepath.Join(child, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != expected {
			t.Fatalf("%s: expected %q, got %q", name, expected, data)
		}
	}
}

func TestAttachProcessCgroupCleansPartialSetup(t *testing.T) {
	root := t.TempDir()
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	m.cgroupRoot = root
	m.processMemoryMax = "64M"
	path := filepath.Join(root, "keyboard.kbd")
	if err := m.attachProcessCgroup(filepath.Join(m.configDir, "keyboard.kbd"), 1234); err == nil {
		t.Fatal("partial cgroup setup unexpectedly succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("partial cgroup was not removed: %v", err)
	}
}

func TestCleanupCgroupRefusesNonemptyCgroup(t *testing.T) {
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "cgroup.procs"), []byte("1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupCgroup(path); err == nil {
		t.Fatal("nonempty cgroup was removed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nonempty cgroup disappeared: %v", err)
	}
}

func TestAttachProcessCgroupIntegration(t *testing.T) {
	root := os.Getenv("KMONAD_TEST_CGROUP_ROOT")
	if root == "" {
		t.Skip("set KMONAD_TEST_CGROUP_ROOT to a delegated cgroup v2 directory")
	}
	command := fakeKMonad(t)
	cmd := exec.Command(command, "keyboard.kbd")
	host.ConfigureChild(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		signalProcessID(cmd.Process.Pid, platform.SignalKill)
		_ = cmd.Wait()
	})
	m := testManager(t, t.TempDir(), command)
	m.cgroupRoot = root
	if err := m.attachProcessCgroup("keyboard.kbd", cmd.Process.Pid); err != nil {
		t.Fatalf("cannot attach process to delegated cgroup: %v", err)
	}
}

func TestReadDeviceFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyboard.kbd")
	content := "(defcfg\n  input(device-file \"/dev/input/event0\")\n)\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readDeviceFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if value != "/dev/input/event0" {
		t.Fatalf("expected device path, got %q", value)
	}
}

func TestDeviceFileParserIgnoresCommentsAndStringContents(t *testing.T) {
	content := []byte(`(defcfg
  ;; input (device-file "/dev/wrong")
  output (uinput-sink "input (device-file \\\"/dev/wrong\\\")")
  input(device-file "/dev/input/event0")
)`)
	device, err := deviceFileFromData(content)
	if err != nil {
		t.Fatal(err)
	}
	if device != "/dev/input/event0" {
		t.Fatalf("expected /dev/input/event0, got %q", device)
	}
}

func TestDeviceFileParserRejectsUnterminatedStrings(t *testing.T) {
	if _, err := deviceFileFromData([]byte(`(defcfg input (device-file "/dev/input/event0)`)); err == nil {
		t.Fatal("expected unterminated string to fail")
	}
}

func TestSecondsRejectsOverflow(t *testing.T) {
	if got := seconds("0"); got != 0 {
		t.Fatalf("expected zero duration for zero input, got %s", got)
	}
	if got := seconds("999999999999999999999999"); got != 0 {
		t.Fatalf("expected zero duration for overflow, got %s", got)
	}
	if got := seconds("3"); got != 3*time.Second {
		t.Fatalf("unexpected valid duration: %s", got)
	}
}

func TestReconcileStartsReadyPrimaryAndSkipsDuplicate(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(configDir, "one.kbd")
	duplicate := filepath.Join(configDir, "two.kbd")
	missing := filepath.Join(configDir, "missing.kbd")
	writeKBD(t, primary, "/dev/null")
	writeKBD(t, duplicate, "/dev/null")
	writeKBD(t, missing, filepath.Join(root, "not-connected"))

	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())

	if m.states[primary] == nil || m.states[primary].process == nil {
		t.Fatal("expected primary configuration to be running")
	}
	if _, ok := m.states[duplicate]; ok {
		t.Fatal("duplicate configuration should not be started")
	}
	if _, ok := m.states[missing]; ok {
		t.Fatal("disconnected configuration should not be tracked")
	}
	if m.duplicates[duplicate] == "" {
		t.Fatal("expected duplicate configuration to be recorded")
	}
}

func TestReconcileRefusesWorldWritableConfiguration(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "unsafe.kbd")
	writeKBD(t, config, "/dev/null")
	if err := os.Chmod(config, 0o666); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	if _, ok := m.states[config]; ok {
		t.Fatal("world-writable configuration should not be started")
	}
}

func TestReconcileRefusesWorldWritableConfigurationDirectory(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configDir, 0o777); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "unsafe.kbd")
	writeKBD(t, config, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	if _, ok := m.states[config]; ok {
		t.Fatal("world-writable configuration directory should not be started")
	}
}

func TestReconcileHonorsConfigurationLimit(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(configDir, "one.kbd")
	second := filepath.Join(configDir, "two.kbd")
	writeKBD(t, first, "/dev/null")
	writeKBD(t, second, "/dev/zero")
	m := testManager(t, configDir, fakeKMonad(t))
	m.maxConfigs = 1
	m.reconcile(time.Now())
	if m.states[first] == nil || m.states[first].process == nil {
		t.Fatal("expected the first configuration to run")
	}
	if _, ok := m.states[second]; ok {
		t.Fatal("configuration beyond the limit should not run")
	}
}

func TestReconcileDuplicateFailsOverWhenPrimaryIsRemoved(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(configDir, "one.kbd")
	duplicate := filepath.Join(configDir, "two.kbd")
	writeKBD(t, primary, "/dev/null")
	writeKBD(t, duplicate, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}
	m.reconcile(time.Now())
	if m.states[duplicate] == nil || m.states[duplicate].process == nil {
		t.Fatal("expected duplicate configuration to take over")
	}
	if _, ok := m.states[primary]; ok {
		t.Fatal("removed primary configuration should not remain tracked")
	}
}

func TestReconcileRestartsWhenDeviceTargetChanges(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	device := filepath.Join(root, "device")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", device); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "one.kbd")
	writeKBD(t, config, device)
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	oldPID := m.states[config].process.cmd.Process.Pid
	if err := os.Remove(device); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", device); err != nil {
		t.Fatal(err)
	}
	m.reconcile(time.Now())
	newPID := m.states[config].process.cmd.Process.Pid
	if newPID == oldPID {
		t.Fatalf("expected a new process after device replacement, still have PID %d", oldPID)
	}
}

func TestReconcileBacksOffARepeatedCrash(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "crash.kbd")
	writeKBD(t, config, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	process := m.states[config].process
	waitFor(t, func() bool { return !processStillRunning(process) })
	now := time.Now()
	m.reconcile(now)
	state := m.states[config]
	if state.failures != 1 {
		t.Fatalf("expected one recorded failure, got %d", state.failures)
	}
	if !state.retryAfter.After(now) {
		t.Fatal("expected a future retry deadline")
	}
	if state.process != nil {
		t.Fatal("crashed process should not be immediately restarted")
	}
}

func TestLockPreventsConcurrentManagers(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	first, _, err := acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLock(first)
	second, _, err := acquireLock()
	if !errors.Is(err, errLockHeld) {
		t.Fatalf("expected lock contention, got file=%v err=%v", second, err)
	}
}

func TestJSONLogIncludesStructuredFields(t *testing.T) {
	var output bytes.Buffer
	previousOutput := logOutput
	logOutput = &output
	defer func() { logOutput = previousOutput }()
	t.Setenv("KMONAD_LOG_FORMAT", "json")

	logConfigEvent("process_started", "/tmp/keyboard.kbd", "KMonad process started", map[string]any{"pid": 42})
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["event"] != "process_started" || record["config"] != "keyboard.kbd" || record["pid"] != float64(42) {
		t.Fatalf("unexpected structured log: %#v", record)
	}
}

func TestJSONModeWrapsKMonadOutput(t *testing.T) {
	var output bytes.Buffer
	previousOutput := logOutput
	logOutput = &output
	defer func() { logOutput = previousOutput }()
	t.Setenv("KMONAD_LOG_FORMAT", "json")

	stdout, stderr := childOutputWriters("/tmp/keyboard.kbd")
	if _, err := stdout.Write([]byte("standard output\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := stderr.Write([]byte("standard error\n")); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two JSON log records, got %q", output.String())
	}
	for index, expectedEvent := range []string{"kmonad_stdout", "kmonad_stderr"} {
		var record map[string]any
		if err := json.Unmarshal([]byte(lines[index]), &record); err != nil {
			t.Fatalf("child output is not valid JSON: %q: %v", lines[index], err)
		}
		if record["event"] != expectedEvent || record["config"] != "keyboard.kbd" {
			t.Fatalf("unexpected child output record: %#v", record)
		}
	}
}

func TestRefreshWatchesConfigurationAndDeviceDirectories(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	deviceDir := filepath.Join(root, "devices")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(deviceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	device := filepath.Join(deviceDir, "keyboard")
	if err := os.Symlink("/dev/null", device); err != nil {
		t.Fatal(err)
	}
	writeKBD(t, filepath.Join(configDir, "keyboard.kbd"), device)
	m := testManager(t, configDir, fakeKMonad(t))
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	m.refreshWatches(watcher)
	if !m.watchPaths[configDir] || !m.watchPaths[deviceDir] {
		t.Fatalf("expected config and device directories to be watched: %#v", m.watchPaths)
	}
}

func TestRefreshWatchesRetriesMissingDirectories(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	m := testManager(t, configDir, fakeKMonad(t))
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	m.refreshWatches(watcher)
	if m.watchPaths[configDir] {
		t.Fatalf("missing directory was recorded as watched: %#v", m.watchPaths)
	}
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	m.refreshWatches(watcher)
	if !m.watchPaths[configDir] {
		t.Fatalf("directory was not watched after it appeared: %#v", m.watchPaths)
	}
}

func TestRefreshWatchesReaddsRecreatedDirectory(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, configDir, fakeKMonad(t))
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	m.refreshWatches(watcher)
	if !m.watchPaths[configDir] {
		t.Fatal("initial configuration directory was not watched")
	}
	if err := os.Remove(configDir); err != nil {
		t.Fatal(err)
	}
	m.refreshWatches(watcher)
	if m.watchPaths[configDir] {
		t.Fatal("removed configuration directory remained marked as watched")
	}
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	m.refreshWatches(watcher)
	if !m.watchPaths[configDir] {
		t.Fatal("recreated configuration directory was not watched")
	}
}

func TestRecoverOwnedProcessFromStaleStatus(t *testing.T) {
	command := fakeKMonad(t)
	config := filepath.Join(t.TempDir(), "recover.kbd")
	cmd := exec.Command(command, config)
	host.ConfigureChild(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		signalProcessID(cmd.Process.Pid, platform.SignalKill)
		_ = cmd.Wait()
	})
	start := processStartTime(cmd.Process.Pid)
	if start == 0 {
		t.Fatal("could not read owned process start time")
	}
	statusPath := filepath.Join(t.TempDir(), "status.json")
	data, err := json.Marshal(statusFile{ConfigDir: filepath.Dir(config), Configurations: []statusConfig{{
		Name:         filepath.Base(config),
		ProcessID:    cmd.Process.Pid,
		ProcessStart: start,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	recoverOwnedProcesses(statusPath, command)
	if pidExists(cmd.Process.Pid) {
		t.Fatal("stale owned process was not terminated")
	}
	if _, err := os.Stat(statusPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale status file to be removed, got %v", err)
	}
	_ = cmd.Wait()
}

func TestRecoverOwnedProcessRejectsReusedPID(t *testing.T) {
	command := fakeKMonad(t)
	config := filepath.Join(t.TempDir(), "recover.kbd")
	cmd := exec.Command(command, config)
	host.ConfigureChild(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		signalProcessID(cmd.Process.Pid, platform.SignalKill)
		_ = cmd.Wait()
	}()
	statusPath := filepath.Join(t.TempDir(), "status.json")
	data, err := json.Marshal(statusFile{Configurations: []statusConfig{{
		Name:         filepath.Base(config),
		ProcessID:    cmd.Process.Pid,
		ProcessStart: processStartTime(cmd.Process.Pid) + 1,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	recoverOwnedProcesses(statusPath, command)
	if !pidExists(cmd.Process.Pid) {
		t.Fatal("PID start-time mismatch should not terminate the process")
	}
}

func FuzzReadDeviceFile(f *testing.F) {
	f.Add("(defcfg input (device-file \"/dev/null\"))")
	f.Add("; input (device-file \"/dev/null\")")
	f.Fuzz(func(t *testing.T, content string) {
		path := filepath.Join(t.TempDir(), "config.kbd")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _ = readConfig(path)
	})
}
