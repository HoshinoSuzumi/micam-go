//go:build !windows

package pppp

import (
	"net"
	"syscall"
)

// enableBroadcast allows sending to a broadcast address. Linux refuses the send
// otherwise; Windows does not require the option.
func enableBroadcast(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}

	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	}); err != nil {
		return err
	}
	return sockErr
}
