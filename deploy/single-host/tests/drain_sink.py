# A test HTTPS log intake for log-drains-journey.sh (EXC-658): keeps each POST
# body and its Authorization header in /data/<first path segment>.log.
import http.server
import re
import ssl


class Sink(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        name = re.sub(r"[^a-z0-9]", "", self.path.strip("/").split("/")[0].split("?")[0]) or "root"
        body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
        with open(f"/data/{name}.log", "ab") as out:
            out.write(b"auth=" + (self.headers.get("Authorization") or "").encode() + b"\n" + body + b"\n")
        self.send_response(202)
        self.end_headers()

    def log_message(self, *args):
        pass


server = http.server.ThreadingHTTPServer(("0.0.0.0", 443), Sink)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain("/sink/sink.pem", "/sink/sink.key")
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
