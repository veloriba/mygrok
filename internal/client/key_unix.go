//go:build !windows

package client

import (
	"bufio"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

// keysEnabled reports whether key input can be read: the client only takes
// keys when stdin is a real TTY (a piped or captured stdin would swallow
// input meant for nothing and leave the terminal in raw mode).
func keysEnabled() bool {
	return isatty.IsTerminal(os.Stdin.Fd())
}

// readerRetry is how long the key reader pauses after a transient read error
// before re-arming raw mode and continuing, so key input (and quit) is not
// lost if the TTY hiccups mid-session.
const readerRetry = 100 * time.Millisecond

// startKeyReader puts stdin into raw mode (when it is a TTY) and delivers
// pressed keys on a buffered channel, dropping keys once the buffer is full.
// Transient read errors (e.g. a PTY EIO) re-arm raw mode and keep reading
// instead of killing input. stop restores the terminal state; the reader
// goroutine then closes the keys channel. When stdin is not a TTY, stop is a
// no-op and keys never closes or delivers anything, so selection falls back
// to auto-rotation.
func startKeyReader() (stop func(), keys <-chan rune) {
	out := make(chan rune, 16)
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return func() {}, out
	}
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return func() {}, out
	}
	stopped := make(chan struct{})
	go func() {
		defer close(out)
		r := bufio.NewReader(os.Stdin)
		for {
			ch, _, err := r.ReadRune()
			if err != nil {
				// The TTY may have hiccupped (e.g. a transient PTY EIO).
				// Pause, re-arm raw mode, and keep reading so key input —
				// and the quit key — is not lost. A hard stop wins.
				select {
				case <-stopped:
					return
				case <-time.After(readerRetry):
				}
				// Re-assert raw mode (the original state is restored by
				// stop); if the terminal is gone, stop reading.
				if _, err2 := term.MakeRaw(fd); err2 != nil {
					return
				}
				r = bufio.NewReader(os.Stdin)
				continue
			}
			select {
			case out <- ch:
			default: // buffer full: drop the key
			}
			select {
			case <-stopped:
				return
			default:
			}
		}
	}()
	return func() {
		close(stopped)
		term.Restore(fd, state)
	}, out
}
