"""A GraphQL subscriber, as an app's browser runs one (EXC-612).

    realtime_subscriber.py <port> <project> <token> <field> <out> <seconds>

Opens wss://api.localhost:<port>/<project>/graphql (graphql-transport-ws),
subscribes to <field>, writes "ready" to <out> once the subscription is
accepted, then one JSON line per change until <seconds> pass. Standard
library only, so the smoke host needs nothing installed.
"""
import base64
import json
import os
import socket
import ssl
import struct
import sys
import time


def frame(text):
    payload = text.encode()
    mask = os.urandom(4)
    head = bytes([0x81])
    if len(payload) < 126:
        head += bytes([0x80 | len(payload)])
    else:
        head += bytes([0x80 | 126]) + struct.pack("!H", len(payload))
    return head + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(payload))


def read_exact(sock, size):
    data = b""
    while len(data) < size:
        chunk = sock.recv(size - len(data))
        if not chunk:
            raise ConnectionError("closed")
        data += chunk
    return data


def read_message(sock):
    first, second = read_exact(sock, 2)
    size = second & 0x7F
    if size == 126:
        size = struct.unpack("!H", read_exact(sock, 2))[0]
    elif size == 127:
        size = struct.unpack("!Q", read_exact(sock, 8))[0]
    payload = read_exact(sock, size)
    opcode = first & 0x0F
    if opcode == 0x8:
        raise ConnectionError("server closed: " + payload[2:].decode(errors="replace"))
    if opcode == 0x9:
        sock.sendall(bytes([0x8A, 0x80]) + os.urandom(4))
        return None
    return payload.decode()


def connect(port, project, token):
    raw = socket.create_connection(("127.0.0.1", port), timeout=10)
    context = ssl.create_default_context()
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE
    sock = context.wrap_socket(raw, server_hostname="api.localhost")
    key = base64.b64encode(os.urandom(16)).decode()
    sock.sendall((
        f"GET /{project}/graphql HTTP/1.1\r\nHost: api.localhost:{port}\r\n"
        "Upgrade: websocket\r\nConnection: Upgrade\r\n"
        f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n"
        "Sec-WebSocket-Protocol: graphql-transport-ws\r\n"
        f"Authorization: Bearer {token}\r\n\r\n").encode())
    response = b""
    while b"\r\n\r\n" not in response:
        chunk = sock.recv(1)
        if not chunk:
            raise ConnectionError("no upgrade answer")
        response += chunk
    if b" 101 " not in response.split(b"\r\n", 1)[0]:
        raise ConnectionError(response.decode(errors="replace").splitlines()[0])
    return sock


def main():
    port, project, token, field, out, seconds = sys.argv[1:7]
    deadline = time.time() + int(seconds)
    sock = connect(int(port), project, token)
    sock.sendall(frame(json.dumps({"type": "connection_init", "payload": {}})))
    query = "subscription { %s { operation table data } }" % field
    with open(out, "a", encoding="utf-8") as sink:
        while time.time() < deadline:
            sock.settimeout(max(0.1, deadline - time.time()))
            try:
                message = read_message(sock)
            except socket.timeout:
                break
            if message is None:
                continue
            parsed = json.loads(message)
            if parsed.get("type") == "connection_ack":
                sock.sendall(frame(json.dumps({"id": "1", "type": "subscribe", "payload": {"query": query}})))
                # The handler subscribes on receipt; give it a moment before changes are made.
                time.sleep(1)
                sink.write("ready\n")
            elif parsed.get("type") == "next":
                sink.write(json.dumps(parsed["payload"]) + "\n")
            elif parsed.get("type") in ("error", "connection_error", "complete"):
                sink.write("closed " + message + "\n")
                break
            sink.flush()


if __name__ == "__main__":
    main()
