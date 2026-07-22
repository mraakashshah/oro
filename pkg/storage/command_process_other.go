//go:build !unix

package storage

import "os/exec"

func configureLeasedCommandProcessGroup(_ *exec.Cmd) {}
