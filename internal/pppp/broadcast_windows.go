//go:build windows

package pppp

import "net"

// enableBroadcast is a no-op on Windows, which allows broadcast sends on a UDP
// socket without SO_BROADCAST.
func enableBroadcast(*net.UDPConn) error { return nil }
