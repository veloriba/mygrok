//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// notifyReload subscribes ch to SIGHUP, the nginx-style hot-reload trigger.
func notifyReload(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGHUP)
}

// sendReloadSignal asks the running process to re-read its config. The
// signal-0 probe distinguishes a dead pid (stale pidfile) from a live one.
func sendReloadSignal(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("process %d not found: %w", pid, err)
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return fmt.Errorf("process %d is not running (stale pidfile?): %w", pid, err)
	}
	return proc.Signal(syscall.SIGHUP)
}
