//go:build windows

package distorters

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {
	// Setpgid is Unix-only; no-op on Windows.
}
