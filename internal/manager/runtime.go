package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

func (m *manager) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	watcher, err := newWatcher()
	if err != nil {
		logf("filesystem watcher unavailable; using polling only: %v", err)
	}
	if watcher != nil {
		defer watcher.Close()
		m.refreshWatches(watcher)
	}
	for {
		if watcher == nil {
			watcher, err = newWatcher()
			if err == nil {
				m.refreshWatches(watcher)
			} else {
				logf("filesystem watcher unavailable; using polling only: %v", err)
			}
		}
		m.reconcile(time.Now())
		m.markProgress()
		if watcher != nil {
			m.refreshWatches(watcher)
		}
		select {
		case <-ctx.Done():
			if watcher != nil {
				_ = watcher.Close()
			}
			return
		case <-ticker.C:
		case command := <-m.commands:
			m.executeCommand(command)
		case _, ok := <-watcherEvents(watcher):
			if !ok {
				if watcher != nil {
					_ = watcher.Close()
				}
				watcher = nil
			}
		case watcherErr, ok := <-watcherErrors(watcher):
			if !ok {
				if watcher != nil {
					_ = watcher.Close()
				}
				watcher = nil
			} else if watcherErr != nil {
				logf("filesystem watcher error: %v", watcherErr)
				_ = watcher.Close()
				watcher = nil
			}
		}
	}
}

func (m *manager) markProgress() {
	m.lastProgress.Store(time.Now().UnixNano())
}

func startMetricsServer(m *manager, address string) (*http.Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		m.metricsFailures.Add(1)
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", metricsHandler(m))
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	m.metricsServerUp.Store(true)
	go func() {
		err := server.Serve(listener)
		m.metricsServerUp.Store(false)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.metricsFailures.Add(1)
			logf("metrics server stopped unexpectedly: %v", err)
		}
	}()
	return server, nil
}

func metricsHandler(m *manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "kmonad_manager_reconciles_total %d\n", m.reconciles.Load())
		fmt.Fprintf(w, "kmonad_manager_process_starts_total %d\n", m.starts.Load())
		fmt.Fprintf(w, "kmonad_manager_failures_total %d\n", m.failures.Load())
		fmt.Fprintf(w, "kmonad_manager_process_stops_total %d\n", m.stops.Load())
		up := 0
		if m.metricsServerUp.Load() {
			up = 1
		}
		fmt.Fprintf(w, "kmonad_manager_metrics_server_up %d\n", up)
		fmt.Fprintf(w, "kmonad_manager_metrics_server_failures_total %d\n", m.metricsFailures.Load())
		fmt.Fprintf(w, "kmonad_manager_status_write_failures_total %d\n", m.statusFailures.Load())
	}
}

func systemdNotify(message string) {
	host.NotifyService(message)
}

func systemdWatchdog(ctx context.Context, lastProgress *atomic.Int64) {
	watchdogPeriod := host.WatchdogInterval()
	if watchdogPeriod <= 0 {
		return
	}
	interval := watchdogPeriod / 2
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			progressAt := time.Unix(0, lastProgress.Load())
			if time.Since(progressAt) <= watchdogPeriod {
				systemdNotify("WATCHDOG=1")
			}
		}
	}
}

func watcherEvents(watcher *fsnotify.Watcher) <-chan fsnotify.Event {
	if watcher == nil {
		return nil
	}
	return watcher.Events
}

func watcherErrors(watcher *fsnotify.Watcher) <-chan error {
	if watcher == nil {
		return nil
	}
	return watcher.Errors
}

func (m *manager) refreshWatches(watcher *fsnotify.Watcher) {
	if m.watchPaths == nil {
		m.watchPaths = make(map[string]bool)
	}
	desired := map[string]bool{m.configDir: true}
	if entries, err := os.ReadDir(m.configDir); err == nil {
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".kbd") {
				continue
			}
			if device, err := readDeviceFileWithLimit(filepath.Join(m.configDir, entry.Name()), m.maxConfigBytes); err == nil && device != "" {
				desired[filepath.Dir(device)] = true
			}
		}
	}
	for path := range m.watchPaths {
		if !desired[path] {
			_ = watcher.Remove(path)
			delete(m.watchPaths, path)
			continue
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_ = watcher.Remove(path)
			delete(m.watchPaths, path)
		}
	}
	for path := range desired {
		if m.watchPaths[path] {
			continue
		}
		if err := watcher.Add(path); err != nil {
			if !os.IsNotExist(err) {
				logf("cannot watch %s: %v", path, err)
			}
			continue
		}
		m.watchPaths[path] = true
	}
}
