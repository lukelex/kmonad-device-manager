package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func logf(format string, args ...any) {
	logEvent("message", fmt.Sprintf(format, args...), nil)
}

func logEvent(event, message string, fields map[string]any) {
	if os.Getenv("KMONAD_LOG_FORMAT") == "json" {
		payload := map[string]any{
			"event":   event,
			"message": message,
			"time":    time.Now().UTC(),
		}
		for key, value := range fields {
			payload[key] = value
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(logOutput, "%s\n", data)
		return
	}
	fmt.Fprintf(logOutput, "kmonad-device-manager: %s\n", message)
}

func logConfigEvent(event, config, message string, fields map[string]any) {
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["config"] = filepath.Base(config)
	logEvent(event, message, fields)
}
