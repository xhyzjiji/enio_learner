//go:build !unix

package cli

import "os/exec"

// 非 Unix 平台上退回逐进程终止。进程组语义不同，这里不做等价模拟——
// 本项目的目标平台是 macOS 与 Linux，这个分支只为保证可编译。
//
// On non-Unix platforms fall back to per-process termination. Process-group semantics differ and
// no equivalent is emulated here: this project targets macOS and Linux, and this branch exists
// only to keep the build working.
func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
