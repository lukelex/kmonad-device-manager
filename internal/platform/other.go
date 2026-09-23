//go:build !linux && !darwin && !windows

package platform

import "runtime"

// defaultSystem identifies unsupported targets without presenting a simulated
// platform backend.
type defaultSystem struct{ unsupportedSystem }

func (defaultSystem) Platform() string { return runtime.GOOS }
func (defaultSystem) Backend() string  { return "unsupported" }
