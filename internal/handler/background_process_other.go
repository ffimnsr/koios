//go:build windows

package handler

import "os/exec"

func configureBackgroundProcessCommand(cmd *exec.Cmd) {}

func requestBackgroundProcessStop(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func killBackgroundProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
