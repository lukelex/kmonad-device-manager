package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnvironmentValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte("# comment\r\n KMONAD_CONFIG_DIR = \"/tmp/kmonad\"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := environmentValue(path, "KMONAD_CONFIG_DIR")
	if err != nil {
		t.Fatal(err)
	}
	if value != "/tmp/kmonad" {
		t.Fatalf("expected /tmp/kmonad, got %q", value)
	}
}

func TestReadDeviceFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyboard.kbd")
	content := "(defcfg\n  input(device-file \"/dev/input/event0\")\n)\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readDeviceFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if value != "/dev/input/event0" {
		t.Fatalf("expected device path, got %q", value)
	}
}

func TestSecondsRejectsOverflow(t *testing.T) {
	if got := seconds("0"); got != 0 {
		t.Fatalf("expected zero duration for zero input, got %s", got)
	}
	if got := seconds("999999999999999999999999"); got != 0 {
		t.Fatalf("expected zero duration for overflow, got %s", got)
	}
	if got := seconds("3"); got != 3*time.Second {
		t.Fatalf("unexpected valid duration: %s", got)
	}
}
