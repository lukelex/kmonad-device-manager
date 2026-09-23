package manager

import (
	"context"
	"regexp"
	"strconv"
	"time"
)

const kmonadVersionProbeTimeout = 5 * time.Second
const kmonadAvailabilityCheckInterval = 30 * time.Second

var kmonadVersionPattern = regexp.MustCompile(`\b([0-9]+)\.([0-9]+)(?:\.([0-9]+))?\b`)

func (m *manager) probeKMonadVersion(parent context.Context) {
	m.kmonad = probeKMonadInfo(parent, m.kmonadCommand)
	m.nextKMonadCheck = time.Now().Add(kmonadAvailabilityCheckInterval)
}

func probeKMonadInfo(parent context.Context, command string) KMonadInfo {
	if parent == nil {
		parent = context.Background()
	}
	context, cancel := context.WithTimeout(parent, kmonadVersionProbeTimeout)
	defer cancel()
	available := host.KMonadAvailable(command)
	if !available {
		return KMonadInfo{Compatibility: KMonadCompatibilityUnavailable, ReasonCode: ReasonDependencyUnavailable, Reason: "KMonad executable is unavailable"}
	}
	output, err := host.KMonadVersion(context, command)
	if err != nil {
		return KMonadInfo{Available: true, Compatibility: KMonadCompatibilityUnknown, ReasonCode: ReasonKMonadVersionUnknown, Reason: "KMonad version could not be determined"}
	}
	info := kmonadInfoFromVersion(output)
	info.Available = true
	return info
}

func (m *manager) refreshKMonadAvailability(now time.Time) {
	if !m.nextKMonadCheck.IsZero() && now.Before(m.nextKMonadCheck) {
		return
	}
	m.nextKMonadCheck = now.Add(kmonadAvailabilityCheckInterval)
	available := host.KMonadAvailable(m.kmonadCommand)
	if available == m.kmonad.Available && m.kmonad.ReasonCode != "" {
		return
	}
	m.kmonad.Available = available
	if !available {
		m.kmonad.Compatibility = KMonadCompatibilityUnavailable
		m.kmonad.ReasonCode = ReasonDependencyUnavailable
		m.kmonad.Reason = "KMonad executable is unavailable"
		return
	}
	if m.kmonad.Version == "" {
		m.kmonad.Compatibility = KMonadCompatibilityUnknown
		m.kmonad.ReasonCode = ReasonKMonadVersionUnknown
		m.kmonad.Reason = "KMonad version could not be determined"
	}
}

func normalizedKMonadInfo(info KMonadInfo) KMonadInfo {
	if info.ReasonCode != "" {
		return info
	}
	return KMonadInfo{Compatibility: KMonadCompatibilityUnavailable, ReasonCode: ReasonDependencyUnavailable, Reason: "KMonad executable is unavailable"}
}

func kmonadInfoFromVersion(output string) KMonadInfo {
	match := kmonadVersionPattern.FindStringSubmatch(output)
	if len(match) == 0 {
		return KMonadInfo{Compatibility: KMonadCompatibilityUnknown, ReasonCode: ReasonKMonadVersionUnknown, Reason: "KMonad version could not be determined"}
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch := 0
	if match[3] != "" {
		patch, _ = strconv.Atoi(match[3])
	}
	info := KMonadInfo{Version: strconv.Itoa(major) + "." + strconv.Itoa(minor) + "." + strconv.Itoa(patch)}
	if major > 0 || minor >= 4 {
		info.Compatibility = KMonadCompatibilityCompatible
		info.ReasonCode = ReasonKMonadCompatible
		info.Reason = "KMonad version supports the manager validation lifecycle"
		return info
	}
	info.Compatibility = KMonadCompatibilityIncompatible
	info.ReasonCode = ReasonKMonadVersionUnsupported
	info.Reason = "KMonad 0.4.0 or newer is required"
	return info
}
