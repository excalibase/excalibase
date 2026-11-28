package main

import (
	"context"
	"net"
	"syscall"
)

// socketBuffer is the send and receive buffer, in bytes, of each connection the proxy accepts.
// Left to the kernel they grow to megabytes a connection, which is charged to the proxy's
// memory limit and which a flood of large requests fills before the proxy reads any of it.
const socketBuffer = 256 << 10

// listen opens address with every accepted connection's buffers fixed at socketBuffer.
func listen(address string) (net.Listener, error) {
	config := net.ListenConfig{Control: fixBuffers}
	return config.Listen(context.Background(), "tcp", address)
}

func fixBuffers(_, _ string, conn syscall.RawConn) error {
	var failure error
	if err := conn.Control(func(fd uintptr) {
		for _, option := range []int{syscall.SO_RCVBUF, syscall.SO_SNDBUF} {
			if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, option, socketBuffer); err != nil && failure == nil {
				failure = err
			}
		}
	}); err != nil {
		return err
	}
	return failure
}
