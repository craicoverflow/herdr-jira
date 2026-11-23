package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
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

// processAlive reports whether pid is a running process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
