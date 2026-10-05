package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// writePidFile records the current PID so `mygrok reload` can find the
// running process. A failure to write is not fatal (e.g. a read-only cwd in
// a scratch container): the process keeps serving and the operator can
// still trigger a reload with `docker kill --signal=HUP <container>`.
func writePidFile(path string) error {
	if path == "" {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
}

func readPidFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("malformed pidfile %s: %q", path, strings.TrimSpace(string(data)))
	}
	return pid, nil
}

func removePidFile(path string) {
	if path == "" {
		return
	}
	os.Remove(path)
}
