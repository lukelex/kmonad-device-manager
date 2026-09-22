//go:build linux

package platform

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigureCgroupCleansEachFailedFileOperation(t *testing.T) {
	previousStat := cgroupStat
	previousWrite := cgroupWriteFile
	defer func() {
		cgroupStat = previousStat
		cgroupWriteFile = previousWrite
	}()
	cgroupStat = func(string) (os.FileInfo, error) { return nil, nil }

	for _, failedFile := range []string{"memory.max", "cpu.max", "cgroup.procs"} {
		t.Run(failedFile, func(t *testing.T) {
			root := t.TempDir()
			cgroupWriteFile = func(path string, data []byte, perm os.FileMode) error {
				if filepath.Base(path) == failedFile {
					return errors.New("injected cgroup write failure")
				}
				return nil
			}
			if _, err := (defaultSystem{}).ConfigureCgroup(root, "keyboard.kbd", 1234, "64M", "50000 100000"); err == nil {
				t.Fatal("injected cgroup failure unexpectedly succeeded")
			}
			if _, err := os.Stat(filepath.Join(root, "keyboard.kbd")); !os.IsNotExist(err) {
				t.Fatalf("failed cgroup setup leaked its directory: %v", err)
			}
		})
	}
}

func TestListenAPISocketSecuresSocketAndRemovesItOnClose(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "api.sock")
	listener, err := (defaultSystem{}).ListenAPISocket(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected API socket mode: %v", info.Mode())
	}
	accepted := make(chan APIConnection, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	select {
	case peer := <-accepted:
		_ = peer.Close()
	case err := <-acceptErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("same-user API connection was not accepted")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("API socket was not removed: %v", err)
	}
}

func TestListenAPISocketRefusesUnsafeExistingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.sock")
	if err := os.WriteFile(path, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (defaultSystem{}).ListenAPISocket(path); err == nil {
		t.Fatal("non-socket API path was replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "do not replace" {
		t.Fatalf("unsafe API path was changed: %q, %v", data, err)
	}
}

func TestAPISocketPathUsesPrivateServiceDirectory(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	if _, err := (defaultSystem{}).APISocketPath(); err == nil {
		t.Fatal("group-accessible runtime directory was accepted")
	}
	if err := os.Chmod(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := (defaultSystem{}).APISocketPath()
	if err != nil {
		t.Fatal(err)
	}
	wanted := filepath.Join(runtime, "kmonad-device-manager", "api.sock")
	if path != wanted {
		t.Fatalf("unexpected API socket path: %q", path)
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("API directory was not secured: %v, %v", info, err)
	}
}

func TestProcessMatchesEnvShebang(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kmonad-test")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nwhile true; do sleep 1; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "snapshot.kbd")
	command := exec.Command(path, config)
	(defaultSystem{}).ConfigureChild(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = (defaultSystem{}).SignalProcess(command.Process.Pid, SignalKill)
		_ = command.Wait()
	})
	time.Sleep(10 * time.Millisecond)
	if !(defaultSystem{}).ProcessMatchesCommand(command.Process.Pid, path, config) {
		arguments, _ := processArguments(command.Process.Pid)
		executable, _ := os.Readlink(procPath(command.Process.Pid, "exe"))
		t.Fatalf("env-shebang process was not matched: arguments=%q executable=%q", arguments, executable)
	}
}
