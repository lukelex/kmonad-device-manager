//go:build darwin

package platform

// defaultSystem identifies macOS explicitly while its native backend remains
// unavailable. Do not set Supported true until a complete macOS backend exists.
type defaultSystem struct{ unsupportedSystem }

func (defaultSystem) Platform() string { return "darwin" }
func (defaultSystem) Backend() string  { return "darwin-unavailable" }
