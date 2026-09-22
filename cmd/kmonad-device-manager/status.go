package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"
)

func showStatus(jsonOutput bool) int {
	base, err := runtimeDir()
	if err != nil {
		writeCLIError(os.Stderr, jsonOutput, "runtime_directory_unavailable", fmt.Sprintf("cannot locate runtime directory: %v", err))
		return 1
	}
	data, err := os.ReadFile(filepath.Join(base, "status.json"))
	if err != nil {
		if os.IsNotExist(err) {
			writeCLIError(os.Stderr, jsonOutput, "manager_not_running", "manager is not running")
			return 3
		}
		writeCLIError(os.Stderr, jsonOutput, "status_unreadable", fmt.Sprintf("cannot read status: %v", err))
		return 1
	}
	var status statusFile
	if err := json.Unmarshal(data, &status); err != nil {
		writeCLIError(os.Stderr, jsonOutput, "status_invalid", fmt.Sprintf("invalid status file: %v", err))
		return 1
	}
	if !processExists(status.PID) || status.ProcessStart == 0 || processStartTime(status.PID) != status.ProcessStart {
		writeCLIError(os.Stderr, jsonOutput, "manager_not_running", "manager is not running")
		return 3
	}
	if jsonOutput {
		data, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			writeCLIError(os.Stderr, true, "status_encoding_failed", fmt.Sprintf("cannot encode status: %v", err))
			return 1
		}
		fmt.Println(string(data))
		return 0
	}
	fmt.Printf("Manager PID: %d\n", status.PID)
	fmt.Printf("Configuration directory: %s\n", status.ConfigDir)
	fmt.Printf("Updated: %s\n", status.UpdatedAt.Format(time.RFC3339))
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tSTATE\tCONNECTED\tHEALTHY\tPID\tREASON")
	for _, config := range status.Configurations {
		pid := "-"
		if config.ProcessID != 0 {
			pid = strconv.Itoa(config.ProcessID)
		}
		reason := config.Reason
		if reason == "" {
			reason = "-"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			config.Name, config.State, yesNo(config.Connected), yesNo(config.Healthy), pid, reason)
	}
	_ = writer.Flush()
	return 0
}

func yesNo(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
