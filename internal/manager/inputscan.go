package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

const (
	tokenNamespaceKMonadV1 = "kmonad-v1"
	defaultProbeTimeout    = 15 * time.Second
	minProbeTimeout        = time.Second
	maxProbeTimeout        = 30 * time.Second
)

var inputKeyCapabilities = func(path string) ([]platform.KeyCode, error) {
	return host.KeyCapabilities(path)
}

// kmonadV1Tokens maps the evdev key codes a device may emit to the versioned
// KMonad source-key vocabulary ("kmonad-v1"). It is a pure vocabulary
// translation; the manager never infers a layout, product, or geometry from
// it. Token names are the identifiers KMonad itself parses in defsrc
// (Keycode.hs keyNames), preferring the canonical short alias used in
// published configurations. Every extra code a device reports contributes to
// an input scan's unmapped_count instead.
var kmonadV1Tokens = map[platform.KeyCode]string{
	platform.KeyEsc:            "esc",
	platform.Key1:              "1",
	platform.Key2:              "2",
	platform.Key3:              "3",
	platform.Key4:              "4",
	platform.Key5:              "5",
	platform.Key6:              "6",
	platform.Key7:              "7",
	platform.Key8:              "8",
	platform.Key9:              "9",
	platform.Key0:              "0",
	platform.KeyMinus:          "-",
	platform.KeyEqual:          "=",
	platform.KeyBackspace:      "bspc",
	platform.KeyTab:            "tab",
	platform.KeyQ:              "q",
	platform.KeyW:              "w",
	platform.KeyE:              "e",
	platform.KeyR:              "r",
	platform.KeyT:              "t",
	platform.KeyY:              "y",
	platform.KeyU:              "u",
	platform.KeyI:              "i",
	platform.KeyO:              "o",
	platform.KeyP:              "p",
	platform.KeyLeftBrace:      "[",
	platform.KeyRightBrace:     "]",
	platform.KeyEnter:          "ret",
	platform.KeyLeftCtrl:       "lctl",
	platform.KeyA:              "a",
	platform.KeyS:              "s",
	platform.KeyD:              "d",
	platform.KeyF:              "f",
	platform.KeyG:              "g",
	platform.KeyH:              "h",
	platform.KeyJ:              "j",
	platform.KeyK:              "k",
	platform.KeyL:              "l",
	platform.KeySemicolon:      ";",
	platform.KeyApostrophe:     "'",
	platform.KeyGrave:          "grv",
	platform.KeyLeftShift:      "lsft",
	platform.KeyBackslash:      "\\",
	platform.KeyZ:              "z",
	platform.KeyX:              "x",
	platform.KeyC:              "c",
	platform.KeyV:              "v",
	platform.KeyB:              "b",
	platform.KeyN:              "n",
	platform.KeyM:              "m",
	platform.KeyComma:          ",",
	platform.KeyDot:            ".",
	platform.KeySlash:          "/",
	platform.KeyRightShift:     "rsft",
	platform.KeyKpAsterisk:     "kpasterisk",
	platform.KeyLeftAlt:        "lalt",
	platform.KeySpace:          "spc",
	platform.KeyCapsLock:       "caps",
	platform.KeyF1:             "f1",
	platform.KeyF2:             "f2",
	platform.KeyF3:             "f3",
	platform.KeyF4:             "f4",
	platform.KeyF5:             "f5",
	platform.KeyF6:             "f6",
	platform.KeyF7:             "f7",
	platform.KeyF8:             "f8",
	platform.KeyF9:             "f9",
	platform.KeyF10:            "f10",
	platform.KeyNumLock:        "numlock",
	platform.KeyScrollLock:     "scrolllock",
	platform.KeyKp7:            "kp7",
	platform.KeyKp8:            "kp8",
	platform.KeyKp9:            "kp9",
	platform.KeyKpMinus:        "kpminus",
	platform.KeyKp4:            "kp4",
	platform.KeyKp5:            "kp5",
	platform.KeyKp6:            "kp6",
	platform.KeyKpPlus:         "kpplus",
	platform.KeyKp1:            "kp1",
	platform.KeyKp2:            "kp2",
	platform.KeyKp3:            "kp3",
	platform.KeyKp0:            "kp0",
	platform.KeyKpDot:          "kpdot",
	platform.Key102ND:          "102nd",
	platform.KeyF11:            "f11",
	platform.KeyF12:            "f12",
	platform.KeyKpEnter:        "kpen",
	platform.KeyRightCtrl:      "rctl",
	platform.KeyKpSlash:        "kpslash",
	platform.KeySysRq:          "sysrq",
	platform.KeyRightAlt:       "ralt",
	platform.KeyHome:           "home",
	platform.KeyUp:             "up",
	platform.KeyPageUp:         "pgup",
	platform.KeyLeft:           "left",
	platform.KeyRight:          "right",
	platform.KeyEnd:            "end",
	platform.KeyDown:           "down",
	platform.KeyPageDown:       "pgdn",
	platform.KeyInsert:         "ins",
	platform.KeyDelete:         "del",
	platform.KeyMute:           "mute",
	platform.KeyVolumeDown:     "volumedown",
	platform.KeyVolumeUp:       "volumeup",
	platform.KeyPause:          "pause",
	platform.KeyLeftMeta:       "lmet",
	platform.KeyRightMeta:      "rmet",
	platform.KeyCompose:        "compose",
	platform.KeyMenu:           "menu",
	platform.KeyProg1:          "prog1",
	platform.KeyProg2:          "prog2",
	platform.KeyWww:            "www",
	platform.KeyMail:           "mail",
	platform.KeyNextSong:       "nextsong",
	platform.KeyPlayPause:      "playpause",
	platform.KeyPreviousSong:   "previoussong",
	platform.KeyStopCd:         "stopcd",
	platform.KeyRewind:         "rewind",
	platform.KeyHomePage:       "homepage",
	platform.KeyRefresh:        "refresh",
	platform.KeyF13:            "f13",
	platform.KeyF14:            "f14",
	platform.KeyF15:            "f15",
	platform.KeyF16:            "f16",
	platform.KeyF17:            "f17",
	platform.KeyF18:            "f18",
	platform.KeyF19:            "f19",
	platform.KeyF20:            "f20",
	platform.KeyF21:            "f21",
	platform.KeyF22:            "f22",
	platform.KeyF23:            "f23",
	platform.KeyF24:            "f24",
	platform.KeyProg3:          "prog3",
	platform.KeyProg4:          "prog4",
	platform.KeyFastForward:    "fastforward",
	platform.KeySearch:         "search",
	platform.KeyBrightnessDown: "brightnessdown",
	platform.KeyBrightnessUp:   "brightnessup",
	platform.KeyMicMute:        "micmute",
}

// kmonadV1TokenCode resolves a token back to its evdev code. The reverse scan
// is linear over a fixed, small vocabulary and runs only when a probe starts.
func kmonadV1TokenCode(token string) (platform.KeyCode, bool) {
	for code, candidate := range kmonadV1Tokens {
		if candidate == token {
			return code, true
		}
	}
	return 0, false
}

// kmonadV1KeysFor returns the sorted, de-duplicated token set for the codes a
// device reports. Codes without a vocabulary entry are ignored here; they are
// counted as unmapped by the caller.
func kmonadV1KeysFor(codes []platform.KeyCode) []string {
	seen := make(map[string]bool, len(codes))
	keys := make([]string, 0, len(codes))
	for _, code := range codes {
		if token, known := kmonadV1Tokens[code]; known && !seen[token] {
			seen[token] = true
			keys = append(keys, token)
		}
	}
	sort.Strings(keys)
	return keys
}

// inputScanDigest is a deterministic fingerprint of a key set within one token
// namespace, so a digest can never be confused across namespaces.
func inputScanDigest(keys []string) string {
	sum := sha256.Sum256([]byte(tokenNamespaceKMonadV1 + "\x00" + strings.Join(keys, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type inputScanParams struct {
	DeviceID string `json:"device_id"`
}

type probeStartParams struct {
	DeviceID  string `json:"device_id"`
	Token     string `json:"token"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type probeSession struct {
	operationID string
	deviceID    string
	platformID  string
	nodePath    string
	token       string
	code        platform.KeyCode
	cancel      context.CancelFunc
}

// inputDeviceTarget resolves a public device ID to its current discovery entry
// and node identity, following the same availability gate and role checks as
// identification so scans and probes never touch a manager-owned output.
func (m *manager) inputDeviceTarget(publicDeviceID string) (*discoveredKeyboard, string, *apiError) {
	if publicDeviceID == "" {
		return nil, "", &apiError{Code: "invalid_request", Message: "device_id is required"}
	}
	m.refreshDevices()
	if device, known := m.devices[publicDeviceID]; known && device.Role == DeviceRoleManagerOutput {
		return nil, "", &apiError{Code: "device_not_configurable", Message: "device is a manager-owned virtual output"}
	}
	discovered, err := discoverKeyboardDevices()
	if err != nil {
		return nil, "", &apiError{Code: "temporary_unavailable", Message: "keyboard discovery is unavailable"}
	}
	var target *discoveredKeyboard
	for index := range discovered {
		if discovered[index].device.ID == publicDeviceID {
			target = &discovered[index]
			break
		}
	}
	if target == nil {
		return nil, "", &apiError{Code: "not_found", Message: "device does not exist"}
	}
	if target.device.Availability != DeviceConnected {
		return nil, "", &apiError{Code: "temporary_unavailable", Message: "device is not connected and accessible"}
	}
	platformID, err := deviceID(target.nodePath)
	if err != nil {
		return nil, "", &apiError{Code: "temporary_unavailable", Message: "device is no longer available"}
	}
	return target, platformID, nil
}

func (m *manager) inputScan(ctx context.Context, params inputScanParams) commandResult {
	if ctx.Err() != nil {
		return commandDeadlineResult()
	}
	target, platformID, apiErr := m.inputDeviceTarget(params.DeviceID)
	if apiErr != nil {
		return commandResult{err: apiErr}
	}
	codes, err := inputKeyCapabilities(target.nodePath)
	if err != nil {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "device input capabilities are unavailable"}}
	}
	keys := kmonadV1KeysFor(codes)
	unmapped := 0
	seen := make(map[platform.KeyCode]bool, len(codes))
	for _, code := range codes {
		if seen[code] {
			continue
		}
		seen[code] = true
		if _, known := kmonadV1Tokens[code]; !known {
			unmapped++
		}
	}
	result := InputScan{
		DeviceID:       params.DeviceID,
		TokenNamespace: tokenNamespaceKMonadV1,
		Keys:           keys,
		UnmappedCount:  unmapped,
		Generation:     m.trackInputScanGeneration(params.DeviceID, platformID),
		Digest:         inputScanDigest(keys),
		ObservedAt:     time.Now(),
	}
	return commandResult{result: map[string]InputScan{"inputscan": result}}
}

// trackInputScanGeneration returns a per-device counter that starts at 1 and
// is bumped whenever the device is seen again under a different resolved node
// identity (for example after a re-plug or an enumeration change). The counter
// is in-memory only and resets when the manager restarts, which clients can
// observe because observed_at is always fresh.
func (m *manager) trackInputScanGeneration(deviceID, platformID string) int {
	if m.inputScanBasis == nil {
		m.inputScanBasis = make(map[string]string)
		m.inputScanGeneration = make(map[string]int)
	}
	generation := m.inputScanGeneration[deviceID]
	if generation == 0 {
		generation = 1
	} else if basis, known := m.inputScanBasis[deviceID]; known && basis != platformID {
		generation++
	}
	m.inputScanBasis[deviceID] = platformID
	m.inputScanGeneration[deviceID] = generation
	return generation
}

func (m *manager) startProbe(ctx context.Context, params probeStartParams) commandResult {
	if ctx.Err() != nil {
		return commandDeadlineResult()
	}
	if params.DeviceID == "" {
		return commandResult{err: &apiError{Code: "invalid_request", Message: "device_id is required"}}
	}
	if params.Token == "" {
		return commandResult{err: &apiError{Code: "invalid_request", Message: "token is required"}}
	}
	code, known := kmonadV1TokenCode(params.Token)
	if !known {
		return commandResult{err: &apiError{Code: "invalid_request", Message: "token is not a kmonad-v1 key"}}
	}
	timeout := defaultProbeTimeout
	if params.TimeoutMS != 0 {
		timeout = time.Duration(params.TimeoutMS) * time.Millisecond
	}
	if timeout < minProbeTimeout || timeout > maxProbeTimeout {
		return commandResult{err: &apiError{Code: "invalid_request", Message: "timeout_ms must be between 1000 and 30000"}}
	}
	if m.probe != nil {
		return commandResult{err: &apiError{Code: "conflict", Message: "a probe session is already running"}}
	}
	target, platformID, apiErr := m.inputDeviceTarget(params.DeviceID)
	if apiErr != nil {
		return commandResult{err: apiErr}
	}
	operationID, err := newOperationID()
	if err != nil {
		return commandResult{err: &apiError{Code: "internal", Message: "cannot create probe operation"}}
	}
	observer, err := keypressObserver(target.nodePath)
	if err != nil {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "device cannot be observed"}}
	}
	parent := m.runContext
	if parent == nil {
		parent = context.Background()
	}
	sessionContext, cancel := context.WithTimeout(parent, timeout)
	now := time.Now()
	operation := Operation{
		ID: operationID, Kind: OperationProbe, State: OperationWaiting,
		Resource:  ResourceRef{Kind: ResourceDevice, ID: target.device.ID},
		StartedAt: now, UpdatedAt: now, ReasonCode: ReasonOperationRunning,
		Reason: "waiting for the " + params.Token + " key",
	}
	session := &probeSession{operationID: operationID, deviceID: target.device.ID, platformID: platformID, nodePath: target.nodePath, token: params.Token, code: code, cancel: cancel}
	if m.operations == nil {
		m.operations = make(map[string]Operation)
	}
	m.operations[operationID] = operation
	m.publishOperationChange(operation)
	m.probe = session
	m.pauseDeviceConfigurations(platformID)
	m.writeStatus()
	go m.waitForProbe(sessionContext, session, observer)
	return commandResult{result: map[string]Operation{"operation": operation}}
}

func (m *manager) waitForProbe(ctx context.Context, session *probeSession, observer platform.KeypressObserver) {
	err := observer.WaitForKeyCode(ctx, session.code)
	parent := m.runContext
	if parent == nil {
		parent = context.Background()
	}
	_ = m.submitCommand(parent, func(_ context.Context, owner *manager) commandResult {
		owner.finishProbe(session, err)
		return commandResult{}
	})
}

func (m *manager) finishProbe(session *probeSession, result error) {
	if session == nil || m.probe != session {
		return
	}
	operation := m.operations[session.operationID]
	operation.UpdatedAt = time.Now()
	switch {
	case result == nil:
		operation.State = OperationSucceeded
		operation.ReasonCode = ReasonOperationSucceeded
		operation.Reason = session.token + " keypress received"
	case errors.Is(result, context.DeadlineExceeded):
		operation.State = OperationFailed
		operation.ReasonCode = ReasonOperationTimedOut
		operation.Reason = "no " + session.token + " keypress was received before the timeout"
	case errors.Is(result, context.Canceled):
		operation.State = OperationCancelled
		operation.ReasonCode = ReasonOperationCancelled
		operation.Reason = "probe was cancelled"
	default:
		operation.State = OperationFailed
		operation.ReasonCode, operation.Reason = probeFailureReason(session)
	}
	m.operations[session.operationID] = operation
	m.publishOperationChange(operation)
	session.cancel()
	m.probe = nil
	m.pruneOperations()
	m.reconcile(time.Now())
}

func probeFailureReason(session *probeSession) (ReasonCode, string) {
	switch identificationDeviceAvailability(session.nodePath) {
	case platform.DeviceDisconnected:
		return ReasonDeviceDisconnected, "device disconnected during probe"
	case platform.DeviceInaccessible:
		return ReasonDeviceInaccessible, "device became inaccessible during probe"
	case platform.DeviceUnsupported:
		return ReasonDeviceUnsupported, "device became unsupported during probe"
	default:
		return ReasonInternal, "key observation failed"
	}
}

func (m *manager) cancelProbeOperation(operationID string) commandResult {
	_, exists := m.operations[operationID]
	if !exists {
		return commandResult{err: &apiError{Code: "not_found", Message: "operation does not exist"}}
	}
	if m.probe == nil || m.probe.operationID != operationID {
		return commandResult{err: &apiError{Code: "operation_not_cancellable", Message: "operation is no longer running"}}
	}
	m.probe.cancel()
	m.finishProbe(m.probe, context.Canceled)
	return commandResult{result: map[string]Operation{"operation": m.operations[operationID]}}
}

// probingDevice reports whether an interactive probe currently holds the given
// node identity; reconciliation must keep its configurations paused while so.
func (m *manager) probingDevice(platformID string) bool {
	return m.probe != nil && m.probe.platformID == platformID
}

func (m *manager) cancelProbe() {
	if m.probe == nil {
		return
	}
	m.probe.cancel()
	m.probe = nil
}
