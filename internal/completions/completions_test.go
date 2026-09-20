package completions

import "testing"

func TestForReturnsEmbeddedDefinitions(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		content, err := For(shell)
		if err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		if content == "" {
			t.Fatalf("%s: empty completion definition", shell)
		}
	}
}

func TestForRejectsUnknownShell(t *testing.T) {
	if _, err := For("powershell"); err == nil {
		t.Fatal("expected unknown shell to be rejected")
	}
}
