//go:build unix

package cli

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 让子进程成为独立进程组的组长。
//
// 不这么做的话超时形同虚设：exec.CommandContext 只给直接子进程发信号，而我们的
// 直接子进程是 /bin/sh，真正干活的命令是它的孩子。杀掉 sh 之后孙子进程还活着、
// 还握着输出管道，cmd.Wait() 就一直等到它自己结束为止。
//
// 表现极具迷惑性：超时标记是对的，错误信息也写着"命令超时被终止"，只有墙钟时间
// 暴露了真相——设 2 秒超时的 sleep 5 老老实实跑满了 5 秒。
//
// setProcessGroup makes the child the leader of its own process group.
//
// Without it the timeout is decorative: exec.CommandContext signals only the direct child, which
// here is /bin/sh, while the command doing the actual work is its child. Killing sh leaves the
// grandchild alive and still holding the output pipe, so cmd.Wait() blocks until that grandchild
// finishes on its own.
//
// The symptom is thoroughly misleading: the timeout flag is set correctly and the error even
// reads "command timed out", with only the wall-clock time giving it away — a sleep 5 under a
// 2-second timeout runs the full 5 seconds.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 杀掉整个进程组。负的 PID 表示"这个进程组的所有成员"。
// killProcessGroup kills the entire process group. A negative PID means "every member of this
// process group".
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
