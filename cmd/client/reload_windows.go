//go:build windows

package main

import (
	"errors"
	"os"
)

// notifyReload has no effect on Windows: there is no SIGHUP. The watcher
// never fires, so the process keeps its startup config.
func notifyReload(ch chan<- os.Signal) {}

// sendReloadSignal always fails on bare Windows; the supported hot-reload
// path for Windows hosts is Docker.
func sendReloadSignal(pid int) error {
	return errors.New("hot reload is not supported on bare Windows; run the client in Docker and use `docker kill --signal=HUP <container>`")
}
