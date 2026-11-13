//go:build unix

package devcmd

import (
	"os/exec"
	"syscall"
)

// interruptGroup 让 cmd 在自己的进程组中运行，停止时向整个进程组发送 SIGINT：
// 工具启动的子进程（例如 bun 启动的 vite）会一起收到。
func interruptGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
}

// ownGroup 让应用在自己的进程组中运行：终端的 Ctrl+C 不会直接打断它，由 dev 平滑停止。
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate 向应用的进程组发送 SIGTERM，让它平滑停止。
func terminate(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }

// kill 强制结束应用的进程组。
func kill(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
