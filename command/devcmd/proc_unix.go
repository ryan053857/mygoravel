//go:build unix

package devcmd

import (
	"os/exec"
	"syscall"
)

// interruptGroup 让 cmd 在自己的进程组中运行，停止时向整个进程组发送 SIGINT：
// templ 启动的应用、go run 编译出的程序都会一起收到。
func interruptGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
}
