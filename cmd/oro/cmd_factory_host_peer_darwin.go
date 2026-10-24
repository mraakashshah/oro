//go:build darwin

package main

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// factoryHostPeer reads the connected peer's identity from the kernel. JSON
// handshake claims and socket-file ownership are not substitutes for this check.
func factoryHostPeer(connection *net.UnixConn) (pid, uid int, err error) {
	if connection == nil {
		return 0, 0, errors.New("factory peer requires a connected Unix socket")
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, 0, fmt.Errorf("access factory peer socket: %w", err)
	}
	var credentialError error
	err = raw.Control(func(fd uintptr) {
		credentials, readErr := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if readErr != nil {
			credentialError = fmt.Errorf("read factory peer credentials: %w", readErr)
			return
		}
		// Darwin's sys/ucred.h defines XUCRED_VERSION as zero.
		if credentials.Version != 0 {
			credentialError = errors.New("unsupported factory peer credential version")
			return
		}
		uid = int(credentials.Uid)
		pid, readErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if readErr != nil {
			credentialError = fmt.Errorf("read factory peer PID: %w", readErr)
		}
	})
	if err != nil || credentialError != nil {
		return 0, 0, fmt.Errorf("authenticate factory peer: %w", errors.Join(err, credentialError))
	}
	if pid <= 1 {
		return 0, 0, errors.New("factory peer PID must exceed one")
	}
	return pid, uid, nil
}
