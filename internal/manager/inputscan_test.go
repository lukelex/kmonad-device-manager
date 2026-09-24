package manager

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

func TestKMonadV1VocabularyIsWellFormed(t *testing.T) {
	tokens := make(map[string]platform.KeyCode, len(kmonadV1Tokens))
	for code, token := range kmonadV1Tokens {
		if token == "" {
			t.Fatalf("code %d has an empty token", code)
		}
		if previous, duplicate := tokens[token]; duplicate {
			t.Fatalf("token %q maps to both %d and %d", token, previous, code)
		}
		tokens[token] = code
		if reverse, known := kmonadV1TokenCodes[token]; !known || reverse != code {
			t.Fatalf("reverse token %q maps to %d (known=%v), want %d", token, reverse, known, code)
		}
	}
	required := map[string]platform.KeyCode{
		"esc": platform.KeyEsc, "bspc": platform.KeyBackspace, "ret": platform.KeyEnter,
		"spc": platform.KeySpace, "caps": platform.KeyCapsLock, "grv": platform.KeyGrave,
		"lsft": platform.KeyLeftShift, "rsft": platform.KeyRightShift,
		"lctl": platform.KeyLeftCtrl, "rctl": platform.KeyRightCtrl,
		"lmet": platform.KeyLeftMeta, "rmet": platform.KeyRightMeta,
		"lalt": platform.KeyLeftAlt, "ralt": platform.KeyRightAlt,
		"kpenter": platform.KeyKpEnter, "kpslash": platform.KeyKpSlash,
		"102nd": platform.Key102ND, "mute": platform.KeyMute,
		"volumedown": platform.KeyVolumeDown, "volumeup": platform.KeyVolumeUp,
		"pgup": platform.KeyPageUp, "pgdn": platform.KeyPageDown,
		"ins": platform.KeyInsert, "del": platform.KeyDelete,
		"pause": platform.KeyPause, "compose": platform.KeyCompose, "menu": platform.KeyMenu,
		"f24": platform.KeyF24, "micmute": platform.KeyMicMute,
	}
	for token, code := range required {
		if mapped, known := kmonadV1Tokens[code]; !known || mapped != token {
			t.Fatalf("code %d should map to %q, got %q (known=%v)", code, token, mapped, known)
		}
		if reverse, known := kmonadV1TokenCode(token); !known || reverse != code {
			t.Fatalf("token %q should resolve to %d, got %d (known=%v)", token, code, reverse, known)
		}
	}
	if _, known := kmonadV1TokenCode("not-a-key"); known {
		t.Fatal("unknown token resolved to a key code")
	}
}

func TestKMonadV1KeysForSortsAndDeduplicates(t *testing.T) {
	keys := kmonadV1KeysFor([]platform.KeyCode{
		platform.KeyB, platform.KeyA, platform.KeyB, platform.Key102ND,
		platform.KeyCode(0x100), platform.KeyA,
	})
	if strings.Join(keys, ",") != "102nd,a,b" {
		t.Fatalf("unexpected key set: %#v", keys)
	}
	if len(kmonadV1KeysFor(nil)) != 0 {
		t.Fatal("empty capabilities produced keys")
	}
}

func TestInputScanDigestIsNamespaceScopedAndDeterministic(t *testing.T) {
	first := inputScanDigest([]string{"a", "b"})
	if first != inputScanDigest([]string{"a", "b"}) {
		t.Fatal("digest is not deterministic")
	}
	if first == inputScanDigest([]string{"b", "a"}) {
		t.Fatal("digest ignored token order")
	}
	if first == inputScanDigest([]string{"a"}) {
		t.Fatal("digest ignored token content")
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("digest is not namespaced: %q", first)
	}
}

func TestInputScanGenerationAdvancesWhenIdentityChanges(t *testing.T) {
	m := &manager{}
	if generation := m.trackInputScanGeneration("dev", "node-a"); generation != 1 {
		t.Fatalf("first generation = %d", generation)
	}
	if generation := m.trackInputScanGeneration("dev", "node-a"); generation != 1 {
		t.Fatalf("stable generation = %d", generation)
	}
	if generation := m.trackInputScanGeneration("dev", "node-b"); generation != 2 {
		t.Fatalf("changed generation = %d", generation)
	}
	if generation := m.trackInputScanGeneration("dev", "node-b"); generation != 2 {
		t.Fatalf("restabilized generation = %d", generation)
	}
	if generation := m.trackInputScanGeneration("dev", "node-a"); generation != 3 {
		t.Fatalf("reverted generation = %d", generation)
	}
	if generation := m.trackInputScanGeneration("other", "node-a"); generation != 1 {
		t.Fatalf("independent generation = %d", generation)
	}
}

// inputScanTestManager installs a connected keyboard discovery seam and returns
// a manager ready for scan tests. The caller must restore any other seams.
func inputScanTestManager(t *testing.T, keyboard platform.KeyboardDevice) (*manager, string) {
	t.Helper()
	previous := listKeyboards
	t.Cleanup(func() { listKeyboards = previous })
	listKeyboards = func() ([]platform.KeyboardDevice, error) {
		return []platform.KeyboardDevice{keyboard}, nil
	}
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	return m, opaqueDeviceID(keyboard.Identity)
}

func TestInputScanReadsDeviceKeyCapabilities(t *testing.T) {
	previous := inputKeyCapabilities
	t.Cleanup(func() { inputKeyCapabilities = previous })
	inputKeyCapabilities = func(string) ([]platform.KeyCode, error) {
		return []platform.KeyCode{platform.KeyB, platform.KeyA, platform.Key102ND, platform.KeyCode(0x100)}, nil
	}
	keyboard := platform.KeyboardDevice{
		Identity: "topology:scan", IdentityStability: "topology", NodePath: "/dev/null",
		Availability: platform.DeviceConnected, DisplayName: "Keyboard",
	}
	m, deviceID := inputScanTestManager(t, keyboard)
	result := m.inputScan(context.Background(), inputScanParams{DeviceID: deviceID})
	scan, ok := result.result.(map[string]InputScan)["inputscan"]
	if result.err != nil || !ok {
		t.Fatalf("input scan failed: %#v", result)
	}
	if scan.DeviceID != deviceID || scan.TokenNamespace != tokenNamespaceKMonadV1 {
		t.Fatalf("unexpected scan identity: %#v", scan)
	}
	if strings.Join(scan.Keys, ",") != "102nd,a,b" || scan.UnmappedCount != 1 || scan.Generation != 1 {
		t.Fatalf("unexpected scan contents: %#v", scan)
	}
	if scan.Digest != inputScanDigest(scan.Keys) || scan.ObservedAt.IsZero() {
		t.Fatalf("unexpected scan evidence: %#v", scan)
	}
}

func TestInputScanRejectsUnknownDisconnectedAndOutputDevices(t *testing.T) {
	keyboard := platform.KeyboardDevice{
		Identity: "topology:scan", IdentityStability: "topology", NodePath: "/dev/null",
		Availability: platform.DeviceConnected, DisplayName: "Keyboard",
	}
	m, deviceID := inputScanTestManager(t, keyboard)

	if result := m.inputScan(context.Background(), inputScanParams{}); result.err == nil || result.err.Code != "invalid_request" {
		t.Fatalf("empty device was accepted: %#v", result)
	}
	if result := m.inputScan(context.Background(), inputScanParams{DeviceID: "dev_missing"}); result.err == nil || result.err.Code != "not_found" {
		t.Fatalf("unknown device was accepted: %#v", result)
	}

	previous := listKeyboards
	listKeyboards = func() ([]platform.KeyboardDevice, error) {
		disconnected := keyboard
		disconnected.Availability = platform.DeviceDisconnected
		return []platform.KeyboardDevice{disconnected}, nil
	}
	if result := m.inputScan(context.Background(), inputScanParams{DeviceID: deviceID}); result.err == nil || result.err.Code != "temporary_unavailable" {
		t.Fatalf("disconnected device was accepted: %#v", result)
	}
	listKeyboards = previous

	physicalID := deviceID
	output := platform.KeyboardDevice{
		Identity: "topology:output", IdentityStability: "topology", NodePath: "/dev/zero",
		Availability: platform.DeviceConnected, DisplayName: managedOutputName(physicalID), Virtual: true,
	}
	listKeyboards = func() ([]platform.KeyboardDevice, error) {
		return []platform.KeyboardDevice{keyboard, output}, nil
	}
	m.managedConfigs = map[string]managedConfiguration{
		"cfg_00000000000000000000000000000000": {
			Version: managedConfigurationStoreVersion, Ownership: ConfigurationManaged,
			ID: "cfg_00000000000000000000000000000000", Name: "Output", Revision: 1,
			Model: ManagedConfigurationModel{DeviceID: physicalID},
		},
	}
	if result := m.inputScan(context.Background(), inputScanParams{DeviceID: opaqueDeviceID(output.Identity)}); result.err == nil || result.err.Code != "device_not_configurable" {
		t.Fatalf("manager output was scanned: %#v", result)
	}
}

func TestInputScanReportsUnavailableCapabilities(t *testing.T) {
	previous := inputKeyCapabilities
	t.Cleanup(func() { inputKeyCapabilities = previous })
	inputKeyCapabilities = func(string) ([]platform.KeyCode, error) {
		return nil, errors.New("ioctl failed")
	}
	keyboard := platform.KeyboardDevice{
		Identity: "topology:scan", IdentityStability: "topology", NodePath: "/dev/null",
		Availability: platform.DeviceConnected, DisplayName: "Keyboard",
	}
	m, deviceID := inputScanTestManager(t, keyboard)
	if result := m.inputScan(context.Background(), inputScanParams{DeviceID: deviceID}); result.err == nil || result.err.Code != "temporary_unavailable" {
		t.Fatalf("capability failure was not reported: %#v", result)
	}
}

func TestStartProbeValidatesTokenAndTimeout(t *testing.T) {
	keyboard := platform.KeyboardDevice{
		Identity: "topology:scan", IdentityStability: "topology", NodePath: "/dev/null",
		Availability: platform.DeviceConnected, DisplayName: "Keyboard",
	}
	m, deviceID := inputScanTestManager(t, keyboard)

	cases := []struct {
		name   string
		params probeStartParams
		code   string
	}{
		{"empty device", probeStartParams{Token: "a"}, "invalid_request"},
		{"empty token", probeStartParams{DeviceID: deviceID}, "invalid_request"},
		{"unknown token", probeStartParams{DeviceID: deviceID, Token: "not-a-key"}, "invalid_request"},
		{"short timeout", probeStartParams{DeviceID: deviceID, Token: "a", TimeoutMS: 500}, "invalid_request"},
		{"long timeout", probeStartParams{DeviceID: deviceID, Token: "a", TimeoutMS: 30001}, "invalid_request"},
		{"unknown device", probeStartParams{DeviceID: "dev_missing", Token: "a"}, "not_found"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := m.startProbe(context.Background(), testCase.params)
			if result.err == nil || result.err.Code != testCase.code {
				t.Fatalf("probe validation = %#v", result)
			}
		})
	}
}

func TestProbeFailureReasonMapsDeviceAvailability(t *testing.T) {
	previous := identificationDeviceAvailability
	t.Cleanup(func() { identificationDeviceAvailability = previous })
	session := &probeSession{nodePath: "/dev/null"}
	cases := map[platform.DeviceAvailability]ReasonCode{
		platform.DeviceDisconnected: ReasonDeviceDisconnected,
		platform.DeviceInaccessible: ReasonDeviceInaccessible,
		platform.DeviceUnsupported:  ReasonDeviceUnsupported,
		platform.DeviceConnected:    ReasonInternal,
	}
	for availability, expected := range cases {
		identificationDeviceAvailability = func(string) platform.DeviceAvailability { return availability }
		if reason, message := probeFailureReason(session); reason != expected || message == "" {
			t.Fatalf("availability %q produced %q/%q", availability, reason, message)
		}
	}
}

func TestCancelProbeOperationRejectsUnknownAndInactiveOperations(t *testing.T) {
	m := &manager{operations: map[string]Operation{
		"op_done": {ID: "op_done", Kind: OperationProbe, State: OperationSucceeded},
	}}
	if result := m.cancelProbeOperation("op_missing"); result.err == nil || result.err.Code != "not_found" {
		t.Fatalf("unknown probe cancellation = %#v", result)
	}
	if result := m.cancelProbeOperation("op_done"); result.err == nil || result.err.Code != "operation_not_cancellable" {
		t.Fatalf("inactive probe cancellation = %#v", result)
	}
}

// TestInputScanJSONLinesFixture audits the device.inputscan.get wire contract
// against the manager's own vocabulary and digest rules. The fixture covers
// exact, superset, subset, and partial key sets, vendor keys counted as
// unmapped, stale generation/digest after hotplug, a manager that does not
// advertise device_input_scan, and the resulting unsupported_capability error.
func TestInputScanJSONLinesFixture(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "tests", "fixtures", "inputscan-v1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	requests := make(map[string]apiRequest)
	responses := make(map[string]apiResponse)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var frame struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatalf("invalid fixture line %q: %v", scanner.Text(), err)
		}
		switch frame.Type {
		case "request":
			var request apiRequest
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				t.Fatal(err)
			}
			requests[request.ID] = request
		case "response":
			var response apiResponse
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			responses[response.ID] = response
		default:
			t.Fatalf("fixture has unknown record type %q", frame.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(requests) == 0 || len(responses) != len(requests) {
		t.Fatalf("fixture has %d requests and %d responses", len(requests), len(responses))
	}

	scans := make(map[string]InputScan)
	for id := range requests {
		response, found := responses[id]
		if !found {
			t.Fatalf("fixture request %q has no response", id)
		}
		if response.Error != nil {
			continue
		}
		data, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			InputScan *InputScan `json:"inputscan"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.InputScan != nil {
			scans[id] = *result.InputScan
		}
	}

	for id, scan := range scans {
		if scan.TokenNamespace != tokenNamespaceKMonadV1 || scan.DeviceID == "" || scan.ObservedAt.IsZero() {
			t.Fatalf("scan %q has an invalid envelope: %#v", id, scan)
		}
		if !sort.StringsAreSorted(scan.Keys) {
			t.Fatalf("scan %q keys are not sorted: %#v", id, scan.Keys)
		}
		seen := make(map[string]bool, len(scan.Keys))
		for _, key := range scan.Keys {
			if _, known := kmonadV1TokenCode(key); !known {
				t.Fatalf("scan %q exposes unknown token %q", id, key)
			}
			if seen[key] {
				t.Fatalf("scan %q repeats token %q", id, key)
			}
			seen[key] = true
		}
		if scan.Digest != inputScanDigest(scan.Keys) {
			t.Fatalf("scan %q digest does not match its keys: %#v", id, scan)
		}
	}

	exact, superset, subset, partial := scans["scan-exact"], scans["scan-superset"], scans["scan-subset"], scans["scan-partial"]
	if len(exact.Keys) == 0 {
		t.Fatal("fixture has no exact scan")
	}
	if !containsAll(superset.Keys, exact.Keys) || len(superset.Keys) <= len(exact.Keys) {
		t.Fatalf("superset fixture is not a strict superset: %#v", superset.Keys)
	}
	if !containsAll(exact.Keys, subset.Keys) || len(subset.Keys) >= len(exact.Keys) {
		t.Fatalf("subset fixture is not a strict subset: %#v", subset.Keys)
	}
	if !intersects(partial.Keys, exact.Keys) || containsAll(partial.Keys, exact.Keys) || containsAll(exact.Keys, partial.Keys) {
		t.Fatalf("partial fixture does not partially overlap: %#v", partial.Keys)
	}

	if vendor := scans["scan-vendor"]; vendor.UnmappedCount == 0 {
		t.Fatal("vendor fixture does not count unmapped keys")
	}

	staleOne, staleTwo := scans["scan-stale-1"], scans["scan-stale-2"]
	if staleOne.DeviceID != staleTwo.DeviceID || staleTwo.Generation != staleOne.Generation+1 || staleOne.Digest == staleTwo.Digest {
		t.Fatalf("stale fixture does not advance generation and digest: %#v %#v", staleOne, staleTwo)
	}

	absent := responses["capability-absent"]
	data, err := json.Marshal(absent.Result)
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		Capabilities []Capability `json:"capabilities"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	for _, capability := range info.Capabilities {
		if capability.Name == CapabilityDeviceInputScan {
			t.Fatal("capability-absent fixture advertises device_input_scan")
		}
	}
	if methodAbsent := responses["method-absent"]; methodAbsent.Error == nil || methodAbsent.Error.Code != "unsupported_capability" {
		t.Fatalf("method-absent fixture = %#v", methodAbsent)
	}
}

func containsAll(haystack, needles []string) bool {
	set := make(map[string]bool, len(haystack))
	for _, value := range haystack {
		set[value] = true
	}
	for _, value := range needles {
		if !set[value] {
			return false
		}
	}
	return true
}

func intersects(left, right []string) bool {
	set := make(map[string]bool, len(left))
	for _, value := range left {
		set[value] = true
	}
	for _, value := range right {
		if set[value] {
			return true
		}
	}
	return false
}
