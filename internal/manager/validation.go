package manager

import (
	"context"
	"errors"
	"time"

	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

type validationPreviewParams struct {
	Model   *ManagedConfigurationModel `json:"model,omitempty"`
	Content *string                    `json:"content,omitempty"`
}

type validationPreparation struct {
	snapshotPath    string
	command         string
	timeout         time.Duration
	candidate       []byte
	sourceMap       behaviorSourceMap
	candidateDigest string
}

// prepareValidationPreview resolves a candidate using the reconciliation owner.
// It never invokes KMonad: dry-run happens outside the owner so a slow preview
// cannot delay configured keyboard reconciliation.
func (m *manager) prepareValidationPreview(ctx context.Context, params validationPreviewParams) commandResult {
	if ctx.Err() != nil {
		return commandDeadlineResult()
	}
	if (params.Model == nil) == (params.Content == nil) {
		return commandResult{err: &apiError{Code: "invalid_request", Message: "provide exactly one of model or content"}}
	}
	var content []byte
	var sourceMap behaviorSourceMap
	var candidateDigest string
	if params.Model != nil {
		var result ValidationResult
		candidateDigest = submittedBehaviorDigest(params.Model.Behavior)
		content, sourceMap, result = m.renderManagedConfigurationWithSourceMap(*params.Model)
		if result.Outcome != ValidationValid {
			result.CandidateDigest = candidateDigest
			return commandResult{result: result}
		}
	} else {
		content = []byte(*params.Content)
	}
	limit := m.maxConfigBytes
	if limit <= 0 {
		limit = defaultMaxConfigBytes
	}
	if int64(len(content)) > limit {
		result := validationRejected(ReasonConfigurationTooLarge, "candidate exceeds the configuration size limit", "Reduce the candidate size, then retry.", nil)
		result.CandidateDigest = candidateDigest
		return commandResult{result: result}
	}
	if result := m.checkCandidateInput(content); result.Outcome != ValidationValid {
		result.CandidateDigest = candidateDigest
		return commandResult{result: result}
	}
	if ctx.Err() != nil {
		return commandDeadlineResult()
	}
	snapshot, err := createValidationSnapshot(content)
	if err != nil {
		result := validationBlocked(ReasonDependencyUnavailable, "cannot create a validation snapshot", "Check the manager runtime directory, then retry.", nil)
		result.CandidateDigest = candidateDigest
		return commandResult{result: result}
	}
	if ctx.Err() != nil {
		_ = removeConfigSnapshot(snapshot)
		return commandDeadlineResult()
	}
	timeout := m.dryRunTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return commandResult{result: validationPreparation{
		snapshotPath: snapshot, command: m.kmonadCommand, timeout: timeout,
		candidate: content, sourceMap: sourceMap, candidateDigest: candidateDigest,
	}}
}

func (m *manager) checkCandidateInput(content []byte) ValidationResult {
	return m.checkCandidateInputForClaim(content, "")
}

func (m *manager) checkCandidateInputForClaim(content []byte, allowedClaim string) ValidationResult {
	return m.checkCandidateInputForClaims(content, allowedClaim)
}

func (m *manager) checkCandidateInputForClaims(content []byte, allowedClaims ...string) ValidationResult {
	device, err := deviceFileFromData(content)
	if err != nil {
		return validationRejected(ReasonValidationFailed, "candidate does not declare a valid input device", "Provide a valid KMonad input form or manager-owned model.", nil)
	}
	availability, reasonCode, reason := configuredDeviceAvailability(device)
	if availability != DeviceConnected {
		return validationBlocked(reasonCode, reason, "Reconnect or fix access to the selected keyboard, then retry.", nil)
	}
	identity, err := deviceID(device)
	if err != nil {
		return validationBlocked(ReasonDeviceDisconnected, "input device changed during validation", "Reconnect the keyboard, then retry.", nil)
	}
	m.refreshDevices()
	deviceID := opaqueDeviceID(identity)
	refreshed, found := m.discoveredNodeDevices[identity]
	if !found {
		// Some platform identities are already the public device identity.
		refreshed, found = m.devices[deviceID]
	}
	if found && refreshed.Role == DeviceRoleManagerOutput {
		return validationRejected(ReasonDeviceManagerOutput, "candidate selects a manager-owned virtual output", "Select a physical keyboard input.", &ResourceRef{Kind: ResourceDevice, ID: deviceID})
	}
	var claims []string
	if found {
		claims = refreshed.ConfiguredBy
	} else {
		// Preserve the claim check when discovery failed and refreshDevices could
		// not associate the path with a retained device record.
		claims = m.deviceClaims()[identity]
	}
	for _, claim := range claims {
		allowed := false
		for _, candidate := range allowedClaims {
			if claim == candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return validationBlocked(ReasonDeviceConflicting, "input device is already claimed by a configuration", "Select an unclaimed keyboard or update its existing configuration.", nil)
		}
	}
	return ValidationResult{Outcome: ValidationValid, ReasonCode: ReasonValidationSucceeded, Reason: "candidate input is available"}
}

func runPreparedValidation(ctx context.Context, preparation validationPreparation) ValidationResult {
	defer func() { _ = removeConfigSnapshot(preparation.snapshotPath) }()
	output, err := dryRunContextOutput(ctx, preparation.command, preparation.timeout, preparation.snapshotPath)
	if err == nil {
		return ValidationResult{Outcome: ValidationValid, ReasonCode: ReasonValidationSucceeded, Reason: "candidate passed KMonad validation", CandidateDigest: preparation.candidateDigest}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || isDryRunTimeout(err) {
		result := validationBlocked(ReasonValidationTimedOut, "KMonad validation timed out", "Retry validation or increase the configured dry-run timeout.", nil)
		result.CandidateDigest = preparation.candidateDigest
		return result
	}
	if errors.Is(err, platform.ErrCommandNotFound) {
		result := validationBlocked(ReasonDependencyUnavailable, "KMonad is unavailable", "Install KMonad or correct the configured command, then retry.", nil)
		result.CandidateDigest = preparation.candidateDigest
		return result
	}
	result := validationRejected(ReasonValidationFailed, "KMonad rejected the candidate", "Correct the KMonad configuration and retry.", nil)
	result.CandidateDigest = preparation.candidateDigest
	if parsed, ok := parseValidatorRange(output); ok {
		result.Diagnostics[0].Location = preparation.sourceMap.candidateDiagnosticLocation(preparation.candidate, parsed)
	}
	return result
}

func submittedBehaviorDigest(behavior string) string {
	return "sha256:" + configurationDigest([]byte(behavior))
}

func isDryRunTimeout(err error) bool {
	return err != nil && len(err.Error()) >= len("KMonad dry-run timed out") && err.Error()[:len("KMonad dry-run timed out")] == "KMonad dry-run timed out"
}

func validationRejected(code ReasonCode, reason, remediation string, resource *ResourceRef) ValidationResult {
	return ValidationResult{Outcome: ValidationRejected, ReasonCode: code, Reason: reason, Diagnostics: []Diagnostic{{
		ID: "validation.candidate", Severity: DiagnosticError, ReasonCode: code, Summary: reason, Remediation: remediation, Resource: resource,
	}}}
}

func validationBlocked(code ReasonCode, reason, remediation string, resource *ResourceRef) ValidationResult {
	return ValidationResult{Outcome: ValidationBlocked, ReasonCode: code, Reason: reason, Diagnostics: []Diagnostic{{
		ID: "validation.candidate", Severity: DiagnosticTemporary, ReasonCode: code, Summary: reason, Remediation: remediation, Resource: resource,
	}}}
}
