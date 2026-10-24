//go:build !darwin

package main

import (
	"errors"
	"net"
)

func factoryHostPeer(_ *net.UnixConn) (pid, uid int, err error) {
	return 0, 0, errors.New("managed factory peer authentication is unsupported on this platform")
}
