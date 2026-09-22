package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var logMu sync.Mutex

func logf(format string, args ...any) {
	logEvent("message", fmt.Sprintf(format, args...), nil)
}

func logEvent(event, message string, fields map[string]any) {
	logMu.Lock()
	defer logMu.Unlock()
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

type childJSONWriter struct {
	event  string
	config string
}

func (w childJSONWriter) Write(data []byte) (int, error) {
	output := strings.TrimRight(string(data), "\r\n")
	if output != "" {
		logConfigEvent(w.event, w.config, "KMonad emitted output", map[string]any{"output": output})
	}
	return len(data), nil
}

func childOutputWriters(config string) (io.Writer, io.Writer) {
	if os.Getenv("KMONAD_LOG_FORMAT") != "json" {
		return os.Stdout, os.Stderr
	}
	return childJSONWriter{event: "kmonad_stdout", config: config}, childJSONWriter{event: "kmonad_stderr", config: config}
}

func logConfigEvent(event, config, message string, fields map[string]any) {
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["config"] = filepath.Base(config)
	logEvent(event, message, fields)
}
