//go:build linux

package platform

import (
	"errors"
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
