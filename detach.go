package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// startDetached starts exe with args in a session of its own, its output
// going to logPath, and returns without waiting. The Herdr app runs the
// helper with a 30-second limit and waits for its output to close, so a
// long-lived child (the sign-in, which waits on the browser) must hold none
// of the helper's pipes.
func startDetached(exe string, args []string, logPath string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// stopLoginWorker ends the sign-in worker pid and waits briefly for it to
// go. It checks first that pid is still a login worker, so a PID the system
// has since reused for something else is left alone.
func stopLoginWorker(pid int) {
	if !isLoginWorker(pid) {
		return
	}
	if syscall.Kill(pid, syscall.SIGTERM) != nil {
		return
	}
	for i := 0; i < 20 && processAlive(pid); i++ {
		time.Sleep(50 * time.Millisecond)
	}
}

// isLoginWorker reports whether pid is running `api login-worker`.
func isLoginWorker(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	return err == nil && strings.Contains(string(out), "login-worker")
}

// processAlive reports whether pid is a running process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
