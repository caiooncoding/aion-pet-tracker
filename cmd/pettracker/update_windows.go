package main

import (
	"os/exec"
	"syscall"
)

// runInstaller starts the downloaded installer in silent mode. It outlives this
// process: the installer closes the app, updates the files and starts it again.
func runInstaller(path string) error {
	cmd := exec.Command(path, "/S")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200} // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	return cmd.Start()
}
