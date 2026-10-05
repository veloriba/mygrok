//go:build windows

package client

// keysEnabled reports whether key input is available. The Windows client has
// no raw-mode stdin support, so the dashboard auto-rotates the selected
// tunnel instead of waiting for keys.
func keysEnabled() bool { return false }

// startKeyReader is a no-op stub on Windows: the keys channel never closes or
// delivers anything and stop does nothing.
func startKeyReader() (stop func(), keys <-chan rune) {
	out := make(chan rune, 16)
	return func() {}, out
}
