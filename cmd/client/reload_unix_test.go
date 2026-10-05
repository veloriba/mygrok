//go:build !windows

package main

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSendReloadSignalSelf(t *testing.T) {
	ch := make(chan os.Signal, 1)
	notifyReload(ch)
	if err := sendReloadSignal(os.Getpid()); err != nil {
		t.Fatalf("sendReloadSignal: %v", err)
	}
	select {
	case sig := <-ch:
		if sig != syscall.SIGHUP {
			t.Errorf("signal = %v, want SIGHUP", sig)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGHUP not received")
	}
}

func TestSendReloadSignalDeadPid(t *testing.T) {
	// a pid no process will hold in a test run
	if err := sendReloadSignal(0x7FFFFFFF); err == nil {
		t.Fatal("expected error for dead pid")
	} else if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %v, want it to mention a dead process", err)
	}
}
