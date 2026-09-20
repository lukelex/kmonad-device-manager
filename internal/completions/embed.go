package completions

import "embed"

// The completion definitions are embedded so the installed manager remains a
// single executable and does not need to locate the source tree at runtime.
//
//go:embed bash zsh fish
var files embed.FS
