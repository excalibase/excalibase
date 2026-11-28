package main

import (
	"net"
	"syscall"
	"testing"
)

func bufferSize(t *testing.T, conn net.Conn, option int) int {
	t.Helper()
	raw, err := conn.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var size int
	var optErr error
	if err := raw.Control(func(fd uintptr) {
		size, optErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, option)
	}); err != nil {
		t.Fatal(err)
	}
	if optErr != nil {
		t.Fatal(optErr)
	}
	return size
}

// The kernel's socket memory is charged to the container and grows with the connection,
// so the proxy fixes the buffers of every connection it accepts.
func TestListenFixesTheBuffersOfAcceptedConnections(t *testing.T) {
	listener, err := listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	conn := <-accepted
	t.Cleanup(func() { conn.Close() })
	// The kernel reports twice what was asked for.
	for name, option := range map[string]int{"receive": syscall.SO_RCVBUF, "send": syscall.SO_SNDBUF} {
		if got := bufferSize(t, conn, option); got > 2*socketBuffer {
			t.Errorf("%s buffer is %d bytes, want at most %d", name, got, 2*socketBuffer)
		}
	}
}
