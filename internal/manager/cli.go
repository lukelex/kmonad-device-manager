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
		JSONOutput:  "Returns command, config_dir, healthy, failures, waiting, and checks. Every check is a Diagnostic with stable ID, severity, reason_code, summary, remediation, and an optional affected resource.",
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
		Name:        "manager",
		Invocation:  "kmonad-device-manager manager get [--json]",
		Summary:     "Read public manager metadata, limits, and capabilities.",
		Description: "Ask the running manager for public API, manager, and KMonad version/compatibility information, per-instance server ID, platform backend, health, state revision, resumable event cursor, configured public limits, and capability availability. This read does not discover devices, change configuration state, or affect reconciliation.",
		Options:     []cliOptionHelp{jsonOptionHelp},
		JSONOutput:  "Returns api_versions, manager_version, server_id, platform, backend, kmonad, state_revision, event_cursor, limits, capabilities, and health. Errors are JSON objects on standard error.",
		Examples: []string{
			"kmonad-device-manager manager get",
			"kmonad-device-manager manager get --json",
		},
	},
	{
		Name:        "snapshot",
		Invocation:  "kmonad-device-manager snapshot [--json]",
		Summary:     "Read one authoritative public manager-state snapshot.",
		Description: "Ask the running manager for one coherent state view. The snapshot includes connected and known-disconnected devices, managed and read-only external configurations, retained operations, public diagnostics, manager health, and a monotonic state revision. It does not require or trigger a GUI client for reconciliation.",
		Options:     []cliOptionHelp{jsonOptionHelp},
		JSONOutput:  "Returns state_revision, devices, configurations, operations, diagnostics, and health. Errors are JSON objects on standard error.",
		Examples: []string{
			"kmonad-device-manager snapshot",
			"kmonad-device-manager snapshot --json",
		},
	},
	{
		Name:        "events",
		Invocation:  "kmonad-device-manager events subscribe [--after EVENT_ID] [--server SERVER_ID] [--json]",
		Summary:     "Stream ordered public manager state-transition events.",
		Description: "Subscribe to the running manager's ordered event stream. --after replays retained events strictly newer than EVENT_ID before live events. Pair it with the server_id from snapshot event_cursor using --server so a manager restart cannot silently reuse a cursor. If the server changed, history expired, the cursor is invalid, or the client falls behind, the manager emits manager.resync_required and ends the stream; fetch snapshot and subscribe again. The stream never controls or blocks reconciliation.",
		Arguments: []cliArgumentHelp{{
			Name: "EVENT_ID", Description: "Optional non-negative opaque event sequence from a prior stream; used with --after.",
		}},
		Options: []cliOptionHelp{
			jsonOptionHelp,
			{Syntax: "--after EVENT_ID", Description: "Replay retained events with an ID greater than EVENT_ID before following live events."},
			{Syntax: "--server SERVER_ID", Description: "Require the server_id from snapshot event_cursor when resuming with --after."},
		},
		JSONOutput: "Writes one Event JSON object per line until the manager closes the stream or sends manager_resync_required. Errors are JSON objects on standard error.",
		Examples: []string{
			"kmonad-device-manager events subscribe --json",
			"kmonad-device-manager events subscribe --after 42 --server srv_0123 --json",
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
		Name:        "validate",
		Invocation:  "kmonad-device-manager validate { model MODEL_FILE | file KBD_FILE } [--json]",
		Summary:     "Preview a manager-owned model or KMonad candidate without applying it.",
		Description: "Send one candidate to the running manager for bounded, side-effect-free validation. model reads a JSON managed configuration model with device_id and behavior; file reads KMonad candidate text. The manager resolves device ownership, validates a private runtime snapshot with KMonad, and never writes the watched configuration directory or changes a running mapping.",
		Arguments: []cliArgumentHelp{
			{Name: "MODEL_FILE", Description: "JSON file containing device_id and behavior; used by model."},
			{Name: "KBD_FILE", Description: "KMonad candidate text file; used by file."},
		},
		Options:    []cliOptionHelp{jsonOptionHelp},
		JSONOutput: "Returns a validation object with outcome, reason_code, reason, and structured diagnostics. Errors are JSON objects on standard error.",
		Examples: []string{
			"kmonad-device-manager validate model candidate.json --json",
			"kmonad-device-manager validate file candidate.kbd",
		},
	},
	{
		Name:        "apply",
		Invocation:  "kmonad-device-manager apply MODEL_FILE [--name NAME] [--id CONFIGURATION_ID --revision REVISION] [--json]",
		Summary:     "Transactionally persist and activate a managed configuration model.",
		Description: "Read a managed configuration model and ask the running manager to validate it again, persist an immutable manager-owned revision, and activate only that configuration. A new configuration requires --name. Updating a configuration requires both its opaque --id and its current --revision to prevent overwriting a concurrent change. The operation succeeds only after its KMonad process is started, attached to its cgroup, and passes the manager ownership and health check.",
		Arguments: []cliArgumentHelp{
			{Name: "MODEL_FILE", Description: "JSON file containing a managed configuration model with device_id and behavior."},
			{Name: "NAME", Description: "Display name for a new managed configuration; required when --id is absent."},
			{Name: "CONFIGURATION_ID", Description: "Opaque managed configuration ID to update; used with --id."},
			{Name: "REVISION", Description: "Current positive revision required when updating; used with --revision."},
		},
		Options: []cliOptionHelp{
			jsonOptionHelp,
			{Syntax: "--name NAME", Description: "Set a new configuration's display name, or rename an existing configuration."},
			{Syntax: "--id CONFIGURATION_ID", Description: "Update this existing managed configuration."},
			{Syntax: "--revision REVISION", Description: "Require this current revision for an update; the manager rejects stale revisions."},
		},
		JSONOutput: "Returns an operation object. Its resource ID is the new configuration ID for a create, and configuration_revision is the revision to use for its next update. The operation contains state, reason_code, reason, and the final validation result.",
		Examples: []string{
			"kmonad-device-manager apply laptop.json --name 'Laptop keyboard' --json",
			"kmonad-device-manager apply laptop.json --id cfg_0123 --revision 1 --json",
		},
	},
	{
		Name:        "config",
		Invocation:  "kmonad-device-manager config { list | create MODEL_FILE --name NAME | update CONFIGURATION_ID REVISION MODEL_FILE [--name NAME] | enable CONFIGURATION_ID REVISION | disable CONFIGURATION_ID REVISION | delete CONFIGURATION_ID REVISION | adopt EXTERNAL_CONFIGURATION_ID [--name NAME] } [--json]",
		Summary:     "List or manage configurations while preserving external files as read-only.",
		Description: "list inventories manager-owned and external configurations without exposing platform paths. External .kbd files remain read-only unless adopt can losslessly represent their single device-file input configuration. Adoption never rewrites the source file, requires its bytes to remain unchanged until activation, and hands supervision to a newly persisted managed configuration only after validation. create and update use the transactional apply pipeline, including fresh validation and rollback after failed activation. enable retains the configuration for automatic reconnect recovery; disable stops only its KMonad process while retaining its immutable revision; delete stops it and removes its manager-owned revisions. Updates and lifecycle changes require the current revision, returned as configuration_revision by the prior operation. A manager-owned revision changed outside the manager is shown as failed and cannot be silently overwritten.",
		Arguments: []cliArgumentHelp{
			{Name: "MODEL_FILE", Description: "JSON file containing a managed configuration model with device_id and behavior; used by create and update."},
			{Name: "NAME", Description: "Display name required by create and optional on update."},
			{Name: "CONFIGURATION_ID", Description: "Opaque managed configuration ID returned by create; required by update, enable, disable, and delete."},
			{Name: "EXTERNAL_CONFIGURATION_ID", Description: "Opaque external configuration ID from list; required by adopt."},
			{Name: "REVISION", Description: "Current positive configuration revision required by update, enable, disable, and delete."},
		},
		Options: []cliOptionHelp{
			jsonOptionHelp,
			{Syntax: "--name NAME", Description: "Required by create and optional on update or adopt; sets the managed display name."},
		},
		JSONOutput: "list returns a configurations array with ownership, desired and active revisions, runtime state including retry_at when scheduled, and the most recent retained configuration operation. Mutations return an operation with state, reason_code, reason, resource, and configuration_revision. Errors are JSON objects on standard error.",
		Examples: []string{
			"kmonad-device-manager config list --json",
			"kmonad-device-manager config adopt cfg_0123 --name 'Imported keyboard' --json",
			"kmonad-device-manager config create laptop.json --name 'Laptop keyboard' --json",
			"kmonad-device-manager config update cfg_0123 1 laptop.json --json",
			"kmonad-device-manager config disable cfg_0123 2 --json",
			"kmonad-device-manager config delete cfg_0123 3 --json",
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
			return cliInvocation{}, fmt.Errorf("--status=json was removed; use --status --json")
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
			"kmonad-device-manager manager get [--json]",
			"kmonad-device-manager snapshot [--json]",
			"kmonad-device-manager events subscribe [--after EVENT_ID] [--server SERVER_ID] [--json]",
			"kmonad-device-manager identify start DEVICE_ID [--timeout SECONDS] [--json]",
			"kmonad-device-manager identify status OPERATION_ID [--json]",
			"kmonad-device-manager identify cancel OPERATION_ID [--json]",
			"kmonad-device-manager validate { model MODEL_FILE | file KBD_FILE } [--json]",
			"kmonad-device-manager apply MODEL_FILE [--name NAME] [--id CONFIGURATION_ID --revision REVISION] [--json]",
			"kmonad-device-manager config { list | create MODEL_FILE --name NAME | update CONFIGURATION_ID REVISION MODEL_FILE [--name NAME] | enable CONFIGURATION_ID REVISION | disable CONFIGURATION_ID REVISION | delete CONFIGURATION_ID REVISION } [--json]",
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
