package completions

import (
	"fmt"
	"io/fs"
)

//go:generate sh -c "cp ../../completions/kmonad-device-manager.bash bash && cp ../../completions/_kmonad-device-manager zsh && cp ../../completions/kmonad-device-manager.fish fish"

func For(shell string) (string, error) {
	name := map[string]string{
		"bash": "bash",
		"zsh":  "zsh",
		"fish": "fish",
	}[shell]
	if name == "" {
		return "", fmt.Errorf("unsupported completion shell: %s", shell)
	}
	data, err := fs.ReadFile(files, name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
