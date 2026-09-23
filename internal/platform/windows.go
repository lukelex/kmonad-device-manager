//go:build windows

package platform

// defaultSystem identifies Windows explicitly while its native backend remains
// unavailable. Do not set Supported true until a complete Windows backend exists.
type defaultSystem struct{ unsupportedSystem }

func (defaultSystem) Platform() string { return "windows" }
func (defaultSystem) Backend() string  { return "windows-unavailable" }
