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

func showStatus() int {
	base, err := runtimeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "kmonad-device-manager: cannot locate runtime directory: %v\n", err)
		return 1
	}
	data, err := os.ReadFile(filepath.Join(base, "status.json"))
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "kmonad-device-manager: manager is not running")
			return 3
		}
		fmt.Fprintf(os.Stderr, "kmonad-device-manager: cannot read status: %v\n", err)
		return 1
	}
	var status statusFile
	if err := json.Unmarshal(data, &status); err != nil {
		fmt.Fprintf(os.Stderr, "kmonad-device-manager: invalid status file: %v\n", err)
		return 1
	}
	if !processExists(status.PID) {
		fmt.Fprintln(os.Stderr, "kmonad-device-manager: manager is not running")
		return 3
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
