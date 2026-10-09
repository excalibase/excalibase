package engineproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
)

// WithUpgradeDial makes the proxy relay an authorised attached exec start byte
// for byte over a fresh engine connection, as the socket itself would. Podman
// 4.9 answers it with 200 and a raw stream rather than 101, which an HTTP
// reverse proxy cannot relay.
func (h *Handler) WithUpgradeDial(dial func(ctx context.Context) (net.Conn, error)) *Handler {
	h.dial = dial
	return h
}

// isExecUpgrade is an attached exec start, the only upgrade provisioning makes.
func isExecUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") == "" || r.Method != http.MethodPost {
		return false
	}
	match := execPath.FindStringSubmatch(stripVersion(r.URL.Path))
	return match != nil && match[2] == "start"
}

// hijacked is an engine answer after which the connection carries the exec's
// stream, not HTTP. Only then may raw bytes flow; anything else would let a
// request pipelined behind it reach the engine unchecked.
func hijacked(resp *http.Response) bool {
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return true
	}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return resp.StatusCode == http.StatusOK &&
		(media == "application/vnd.docker.raw-stream" || media == "application/vnd.docker.multiplexed-stream")
}

func (h *Handler) splice(w http.ResponseWriter, r *http.Request) {
	engine, err := h.dial(r.Context())
	if err != nil {
		h.refuse(w, r, err)
		return
	}
	defer engine.Close()
	out := r.Clone(r.Context())
	out.URL.Scheme, out.URL.Host, out.Host = "", "", h.target.Host
	out.RequestURI = ""
	if err := out.Write(engine); err != nil {
		h.refuse(w, r, err)
		return
	}
	fromEngine := bufio.NewReader(engine)
	resp, err := http.ReadResponse(fromEngine, out)
	if err != nil {
		h.refuse(w, r, err)
		return
	}
	if !hijacked(resp) {
		relayAndClose(w, resp)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		h.refuse(w, r, errors.New("connection cannot be upgraded"))
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if err := writeHead(client, resp); err != nil {
		return
	}
	pipe(client, buffered, engine, fromEngine)
}

// relayAndClose answers with the engine's response and ends the connection.
func relayAndClose(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("Connection", "close")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeHead(client net.Conn, resp *http.Response) error {
	head := bufio.NewWriter(client)
	if _, err := fmt.Fprintf(head, "HTTP/1.1 %s\r\n", resp.Status); err != nil {
		return err
	}
	if err := resp.Header.Write(head); err != nil {
		return err
	}
	if _, err := head.WriteString("\r\n"); err != nil {
		return err
	}
	return head.Flush()
}

// pipe copies the exec's stream both ways until either side closes.
func pipe(client net.Conn, fromClient io.Reader, engine net.Conn, fromEngine io.Reader) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(engine, fromClient)
		closeWrite(engine)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, fromEngine)
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func closeWrite(conn net.Conn) {
	if half, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
	}
}
