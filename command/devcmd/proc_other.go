//go:build !unix

package devcmd

import "os/exec"

// interruptGroup 在不支持进程组的平台上保持 exec.CommandContext 的默认行为（结束进程）。
func interruptGroup(*exec.Cmd) {}
