package manager

import (
	"context"
	"os"

	"github.com/lukelex/kmonad-device-manager/internal/completions"
)

// Run executes a complete command invocation and returns its documented exit
// status. It does not call os.Exit, so callers remain responsible for process
// bootstrap and termination.
func Run(ctx context.Context, arguments []string, buildVersion string) int {
	invocation, err := parseCLIInvocation(arguments)
	if err != nil {
		writeCLIError(os.Stderr, containsJSONOption(arguments), "invalid_arguments", err.Error())
		return 2
	}
	if invocation.idempotencyKey != "" && !(len(invocation.args) > 0 && (invocation.args[0] == "apply" || (len(invocation.args) > 1 && invocation.args[0] == "config" && (invocation.args[1] == "create" || invocation.args[1] == "update" || invocation.args[1] == "enable" || invocation.args[1] == "disable" || invocation.args[1] == "delete" || invocation.args[1] == "adopt")))) {
		writeCLIError(os.Stderr, invocation.jsonOutput, "invalid_arguments", "--idempotency-key is only available for configuration mutations")
		return 2
	}

	switch {
	case len(invocation.args) == 1 && invocation.args[0] == "--doctor":
		return doctor(loadSettings(), invocation.jsonOutput)
	case !host.Supported() && !nonPlatformInvocation(invocation.args):
		writeCLIError(os.Stderr, invocation.jsonOutput, "unsupported_platform", "KMonad Device Manager requires Linux with the evdev backend")
		return 2
	case len(invocation.args) == 1 && invocation.args[0] == "--version":
		if err := writeVersionFor(os.Stdout, invocation.jsonOutput, buildVersion); err != nil {
			writeCLIError(os.Stderr, invocation.jsonOutput, "output_failed", err.Error())
			return 1
		}
		return 0
	case len(invocation.args) == 1 && (invocation.args[0] == "--status" || invocation.args[0] == "ps"):
		return showStatus(invocation.jsonOutput)
	case len(invocation.args) == 1 && invocation.args[0] == "devices":
		return showDevices(invocation.jsonOutput)
	case len(invocation.args) >= 1 && invocation.args[0] == "snapshot":
		return snapshotCLI(invocation.args[1:], invocation.jsonOutput)
	case len(invocation.args) >= 1 && invocation.args[0] == "manager":
		return managerCLI(invocation.args[1:], invocation.jsonOutput)
	case len(invocation.args) >= 1 && invocation.args[0] == "events":
		return eventsCLI(invocation.args[1:], invocation.jsonOutput)
	case len(invocation.args) >= 1 && invocation.args[0] == "identify":
		return identifyCLI(invocation.args[1:], invocation.jsonOutput)
	case len(invocation.args) >= 1 && invocation.args[0] == "inputscan":
		return inputScanCLI(invocation.args[1:], invocation.jsonOutput)
	case len(invocation.args) >= 1 && invocation.args[0] == "validate":
		return validateCLI(invocation.args[1:], invocation.jsonOutput)
	case len(invocation.args) >= 1 && invocation.args[0] == "apply":
		return applyCLI(invocation.args[1:], invocation.jsonOutput, invocation.idempotencyKey)
	case len(invocation.args) >= 1 && invocation.args[0] == "config":
		return configCLI(invocation.args[1:], invocation.jsonOutput, invocation.idempotencyKey)
	case len(invocation.args) == 1 && (invocation.args[0] == "-h" || invocation.args[0] == "--help"):
		if err := writeHelp(os.Stdout, invocation.jsonOutput); err != nil {
			writeCLIError(os.Stderr, invocation.jsonOutput, "output_failed", err.Error())
			return 1
		}
		return 0
	case len(invocation.args) == 2 && invocation.args[0] == "--completion":
		output, err := completions.For(invocation.args[1])
		if err != nil {
			writeCLIError(os.Stderr, invocation.jsonOutput, "unsupported_shell", err.Error())
			return 2
		}
		if err := writeCompletion(os.Stdout, invocation.args[1], output, invocation.jsonOutput); err != nil {
			writeCLIError(os.Stderr, invocation.jsonOutput, "output_failed", err.Error())
			return 1
		}
		return 0
	case len(invocation.args) == 0:
		return runService(ctx, invocation.jsonOutput, buildVersion)
	default:
		message := "invalid command line"
		if len(invocation.args) > 0 {
			message = "unknown option or invalid arguments: " + invocation.args[0]
		}
		writeCLIError(os.Stderr, invocation.jsonOutput, "invalid_arguments", message)
		return 2
	}
}

func nonPlatformInvocation(arguments []string) bool {
	return (len(arguments) == 1 && (arguments[0] == "--version" || arguments[0] == "-h" || arguments[0] == "--help")) ||
		(len(arguments) == 2 && arguments[0] == "--completion")
}

func containsJSONOption(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "--json" {
			return true
		}
	}
	return false
}
