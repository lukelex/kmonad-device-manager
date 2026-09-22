package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type cliInvocation struct {
	args       []string
	jsonOutput bool
}

type cliOptionHelp struct {
	Syntax      string `json:"syntax"`
	Description string `json:"description"`
}

type cliArgumentHelp struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type cliCommandHelp struct {
	Name        string            `json:"name"`
	Invocation  string            `json:"invocation"`
	Summary     string            `json:"summary"`
	Description string            `json:"description"`
	Arguments   []cliArgumentHelp `json:"arguments,omitempty"`
	Options     []cliOptionHelp   `json:"options"`
	JSONOutput  string            `json:"json_output"`
	Examples    []string          `json:"examples"`
}

type cliHelpDocument struct {
	Program     string           `json:"program"`
	Summary     string           `json:"summary"`
	Usage       []string         `json:"usage"`
	Commands    []cliCommandHelp `json:"commands"`
	ExitStatus  []cliOptionHelp  `json:"exit_status"`
	FurtherHelp string           `json:"further_help"`
}

var jsonOptionHelp = cliOptionHelp{
	Syntax:      "--json",
	Description: "Emit machine-readable JSON. Service mode emits one JSON object per log line; all one-shot commands emit one JSON document.",
}

var commandHelp = []cliCommandHelp{
	{
		Name:        "service",
		Invocation:  "kmonad-device-manager [--json]",
		Summary:     "Run the foreground manager service.",
		Description: "Continuously discover .kbd files, wait for configured input devices, validate configurations, and supervise one KMonad process per available keyboard. This is the default invocation used by the systemd user service and runs until it receives SIGINT or SIGTERM.",
		Options:     []cliOptionHelp{jsonOptionHelp},
		JSONOutput:  "With --json, operational logs are JSON Lines written to standard error. Each line contains time, event, message, and event-specific fields. KMonad stdout/stderr is wrapped as kmonad_stdout/kmonad_stderr events with an output field. This is equivalent to KMONAD_LOG_FORMAT=json for that invocation.",
		Examples: []string{
			"kmonad-device-manager",
			"kmonad-device-manager --json",
		},
	},
	{
		Name:        "doctor",
		Invocation:  "kmonad-device-manager --doctor [--json]",
		Summary:     "Check whether the system is ready to run configured keyboards.",
		Description: "Check KMonad availability, manager settings, input/uinput permissions, required groups and kernel facilities, configuration security, configured device availability, KMonad dry-run parsing, and systemd user-service state. Disconnected configured keyboards are waiting conditions; required setup failures produce a nonzero exit status.",
		Options:     []cliOptionHelp{jsonOptionHelp},
		JSONOutput:  "Returns command, config_dir, healthy, failures, waiting, and checks. Every check has a stable status of ok, waiting, or error plus a human-readable message.",
		Examples: []string{
			"kmonad-device-manager --doctor",
			"kmonad-device-manager --doctor --json",
		},
	},
	{
		Name:        "status",
		Invocation:  "kmonad-device-manager --status [--json]",
		Summary:     "Show the authoritative runtime snapshot published by the manager.",
		Description: "Verify that the status file belongs to the currently running manager, then report every known configuration with connection, health, process, retry, and failure details. Exit status 3 means the manager is not running.",
		Options: []cliOptionHelp{
			jsonOptionHelp,
			{Syntax: "--status=json", Description: "Compatibility spelling for --status --json."},
		},
		JSONOutput: "Returns the complete status document, including manager identity, update time, configuration directory, and configuration records. Errors are emitted as JSON error objects on standard error.",
		Examples: []string{
			"kmonad-device-manager --status",
			"kmonad-device-manager --status --json",
		},
	},
	{
		Name:        "ps",
		Invocation:  "kmonad-device-manager ps [--json]",
		Summary:     "Show manager status using a process-list-style alias.",
		Description: "Alias for --status. It reads and verifies the same atomic runtime snapshot and has identical fields, output formats, and exit statuses.",
		Options:     []cliOptionHelp{jsonOptionHelp},
		JSONOutput:  "Identical to --status --json.",
		Examples: []string{
			"kmonad-device-manager ps",
			"kmonad-device-manager ps --json",
		},
	},
	{
		Name:        "devices",
		Invocation:  "kmonad-device-manager devices [--json]",
		Summary:     "List known physical keyboard interfaces.",
		Description: "Enumerate known keyboard-capable Linux input interfaces. Device IDs are opaque manager identifiers; platform input paths are never displayed. This command does not require the manager service to be running. Availability is connected, disconnected, inaccessible, unsupported, or conflicting.",
		Options:     []cliOptionHelp{jsonOptionHelp},
		JSONOutput:  "Returns a devices array. Each device contains an opaque ID, display name, vendor, product, serial when available, availability, identity stability, configuration claim data, and a stable reason code.",
		Examples: []string{
			"kmonad-device-manager devices",
			"kmonad-device-manager devices --json",
		},
	},
	{
		Name:        "identify",
		Invocation:  "kmonad-device-manager identify { start DEVICE_ID [--timeout SECONDS] | status OPERATION_ID | cancel OPERATION_ID } [--json]",
		Summary:     "Run, inspect, or cancel a keyboard keypress identification session.",
		Description: "Ask the running manager to wait for a keypress from one connected opaque device ID. The session pauses only the KMonad configuration using that device, then restores it after success, timeout, cancellation, or hotplug. Only one identification session may run at a time; unrelated keyboards continue running.",
		Arguments: []cliArgumentHelp{
			{Name: "DEVICE_ID", Description: "Opaque ID returned by devices; required by start."},
			{Name: "OPERATION_ID", Description: "Opaque ID returned by start; required by status and cancel."},
		},
		Options: []cliOptionHelp{
			jsonOptionHelp,
			{Syntax: "--timeout SECONDS", Description: "Start only. Wait from 1 through 30 seconds; the default is 15 seconds."},
		},
		JSONOutput: "Returns an operation object with opaque ID, state, target device resource, timestamps, reason_code, and reason. Errors are JSON objects on standard error.",
		Examples: []string{
			"kmonad-device-manager identify start dev_0123 --timeout 10",
			"kmonad-device-manager identify status op_0123 --json",
			"kmonad-device-manager identify cancel op_0123",
		},
	},
	{
		Name:        "completion",
		Invocation:  "kmonad-device-manager --completion SHELL [--json]",
		Summary:     "Print an embedded shell-completion definition.",
		Description: "Return the completion definition embedded in this exact executable, allowing completions to match the installed version without locating repository files.",
		Arguments: []cliArgumentHelp{{
			Name:        "SHELL",
			Description: "Required completion target: bash, zsh, or fish.",
		}},
		Options:    []cliOptionHelp{jsonOptionHelp},
		JSONOutput: "Returns shell and completion fields. The completion field contains the full definition as a JSON string.",
		Examples: []string{
			"source <(kmonad-device-manager --completion bash)",
			"kmonad-device-manager --completion fish --json",
		},
	},
	{
		Name:        "version",
		Invocation:  "kmonad-device-manager --version [--json]",
		Summary:     "Show the installed manager version.",
		Description: "Print the version injected at build time. Development builds that did not receive a release version report dev.",
		Options:     []cliOptionHelp{jsonOptionHelp},
		JSONOutput:  "Returns program and version fields.",
		Examples: []string{
			"kmonad-device-manager --version",
			"kmonad-device-manager --version --json",
		},
	},
	{
		Name:        "help",
		Invocation:  "kmonad-device-manager {-h|--help} [--json]",
		Summary:     "Show the complete command index and option reference.",
		Description: "Describe every public manager invocation, argument, option, output contract, and example. The command reference is also maintained in the man page and GitHub Wiki.",
		Options: []cliOptionHelp{
			{Syntax: "-h, --help", Description: "Select the help command."},
			jsonOptionHelp,
		},
		JSONOutput: "Returns the command reference as structured metadata with usage, commands, arguments, options, output descriptions, examples, and exit statuses.",
		Examples: []string{
			"kmonad-device-manager --help",
			"kmonad-device-manager --help --json",
		},
	},
}

func parseCLIInvocation(arguments []string) (cliInvocation, error) {
	invocation := cliInvocation{args: make([]string, 0, len(arguments))}
	for _, argument := range arguments {
		switch argument {
		case "--json":
			if invocation.jsonOutput {
				return cliInvocation{}, fmt.Errorf("--json may only be specified once")
			}
			invocation.jsonOutput = true
		case "--status=json":
			if invocation.jsonOutput {
				return cliInvocation{}, fmt.Errorf("JSON output may only be specified once")
			}
			invocation.jsonOutput = true
			invocation.args = append(invocation.args, "--status")
		default:
			invocation.args = append(invocation.args, argument)
		}
	}
	return invocation, nil
}

func helpDocument() cliHelpDocument {
	return cliHelpDocument{
		Program: "kmonad-device-manager",
		Summary: "Supervise one KMonad process per connected configured keyboard.",
		Usage: []string{
			"kmonad-device-manager [--json]",
			"kmonad-device-manager --doctor [--json]",
			"kmonad-device-manager --status [--json]",
			"kmonad-device-manager ps [--json]",
			"kmonad-device-manager devices [--json]",
			"kmonad-device-manager identify start DEVICE_ID [--timeout SECONDS] [--json]",
			"kmonad-device-manager identify status OPERATION_ID [--json]",
			"kmonad-device-manager identify cancel OPERATION_ID [--json]",
			"kmonad-device-manager --completion SHELL [--json]",
			"kmonad-device-manager --version [--json]",
			"kmonad-device-manager {-h|--help} [--json]",
		},
		Commands: commandHelp,
		ExitStatus: []cliOptionHelp{
			{Syntax: "0", Description: "The command succeeded; for doctor, no required check failed."},
			{Syntax: "1", Description: "A runtime, dependency, setup, lock, or status-reading operation failed."},
			{Syntax: "2", Description: "Command-line arguments or manager settings are invalid."},
			{Syntax: "3", Description: "A status or ps query found that the manager is not running."},
			{Syntax: "127", Description: "Service mode could not find the configured KMonad executable."},
		},
		FurtherHelp: "See man kmonad-device-manager and https://github.com/lukelex/kmonad-device-manager/wiki/Command-Reference.",
	}
}

func writeHelp(writer io.Writer, jsonOutput bool) error {
	document := helpDocument()
	if jsonOutput {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(document)
	}

	fmt.Fprintf(writer, "%s - %s\n\n", document.Program, document.Summary)
	fmt.Fprintln(writer, "USAGE")
	for _, usage := range document.Usage {
		fmt.Fprintf(writer, "  %s\n", usage)
	}
	fmt.Fprintln(writer, "\nCOMMAND INDEX")
	for _, command := range document.Commands {
		fmt.Fprintf(writer, "  %-12s %s\n", command.Name, command.Summary)
	}
	fmt.Fprintln(writer, "\nCOMMAND DETAILS")
	for _, command := range document.Commands {
		fmt.Fprintf(writer, "\n%s\n  Usage: %s\n\n  %s\n", strings.ToUpper(command.Name), command.Invocation, command.Description)
		if len(command.Arguments) > 0 {
			fmt.Fprintln(writer, "\n  Arguments:")
			for _, argument := range command.Arguments {
				fmt.Fprintf(writer, "    %-18s %s\n", argument.Name, argument.Description)
			}
		}
		fmt.Fprintln(writer, "\n  Options:")
		for _, option := range command.Options {
			fmt.Fprintf(writer, "    %-18s %s\n", option.Syntax, option.Description)
		}
		fmt.Fprintf(writer, "\n  JSON output:\n    %s\n", command.JSONOutput)
		fmt.Fprintln(writer, "\n  Examples:")
		for _, example := range command.Examples {
			fmt.Fprintf(writer, "    %s\n", example)
		}
	}
	fmt.Fprintln(writer, "\nEXIT STATUS")
	for _, status := range document.ExitStatus {
		fmt.Fprintf(writer, "  %-4s %s\n", status.Syntax, status.Description)
	}
	fmt.Fprintf(writer, "\n%s\n", document.FurtherHelp)
	return nil
}

var version = "dev"

func writeVersion(writer io.Writer, jsonOutput bool) error {
	return writeVersionFor(writer, jsonOutput, version)
}

func writeVersionFor(writer io.Writer, jsonOutput bool, buildVersion string) error {
	if jsonOutput {
		return json.NewEncoder(writer).Encode(map[string]string{
			"program": "kmonad-device-manager",
			"version": buildVersion,
		})
	}
	_, err := fmt.Fprintf(writer, "kmonad-device-manager %s\n", buildVersion)
	return err
}

func writeCompletion(writer io.Writer, shell, definition string, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(writer).Encode(map[string]string{
			"shell":      shell,
			"completion": definition,
		})
	}
	_, err := fmt.Fprint(writer, definition)
	return err
}

func writeCLIError(writer io.Writer, jsonOutput bool, code, message string) {
	if jsonOutput {
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"error": map[string]string{
				"code":    code,
				"message": message,
			},
		})
		return
	}
	fmt.Fprintf(writer, "kmonad-device-manager: %s\n", message)
}
