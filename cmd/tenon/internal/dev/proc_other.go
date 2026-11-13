//go:build !unix

package dev

import "os/exec"

// interruptGroup 在不支持进程组的平台上保持 exec.CommandContext 的默认行为（结束进程）。
func interruptGroup(*exec.Cmd) {}

func ownGroup(*exec.Cmd) {}

// terminate 在不支持信号的平台上直接结束应用。
func terminate(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

func kill(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
