"""Stand-ins for GitHub and Cloudflare in the single-host site build journey (EXC-671).

GitHub: the App's API calls, the user sign-in exchange and the repository over git's
smart HTTP (git http-backend). Cloudflare: the Workers for Platforms calls a site
deploy makes. GET /_seen answers the files the last deploy uploaded, by path.
Nothing here is verified: it stands in for the far side only.

    python3 sitebuild_fakes.py <port> <repo-dir>
"""
import base64
import json
import os
import re
import subprocess
import sys
import threading
from email import policy
from email.parser import BytesParser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

INSTALLATION, REPO_ID, FULL_NAME = 4242, 777, "acme/site"
REPO = {"id": REPO_ID, "full_name": FULL_NAME, "default_branch": "main", "private": True}
STATE = {"manifest": {}, "bodies": {}, "lock": threading.Lock()}


def head_sha(root):
    return subprocess.check_output(["git", "-C", os.path.join(root, FULL_NAME + ".git"), "rev-parse", "main"], text=True).strip()


class Fakes(BaseHTTPRequestHandler):
    root = ""

    def log_message(self, fmt, *args):
        sys.stderr.write("fakes: " + (fmt % args) + "\n")

    def body(self):
        if self.headers.get("Transfer-Encoding", "").lower() != "chunked":
            return self.rfile.read(int(self.headers.get("Content-Length") or 0))
        # The asset upload streams its multipart body, so it arrives chunked.
        chunks = []
        while True:
            size = int(self.rfile.readline().split(b";")[0].strip() or b"0", 16)
            if size == 0:
                self.rfile.readline()
                return b"".join(chunks)
            chunks.append(self.rfile.read(size))
            self.rfile.readline()

    def answer(self, status, payload=None):
        raw = b"" if payload is None else json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def cloudflare(self, status, result):
        self.answer(status, {"success": True, "errors": [], "messages": [], "result": result})

    def do_GET(self):
        self.route("GET")

    def do_POST(self):
        self.route("POST")

    def do_PUT(self):
        self.route("PUT")

    def do_DELETE(self):
        self.route("DELETE")

    def route(self, method):
        path = self.path.split("?", 1)[0]
        if path.startswith("/gh/" + FULL_NAME + ".git/"):
            return self.git(method)
        if path.startswith("/gh/"):
            return self.github(method, path[3:])
        if path.startswith("/cf/"):
            return self.cf(method, path[3:])
        if path == "/_seen":
            with STATE["lock"]:
                files = {name: STATE["bodies"].get(entry["hash"], "") for name, entry in STATE["manifest"].items()}
            return self.answer(200, {"files": files, "sha": head_sha(self.root)})
        if path == "/_sha":
            return self.answer(200, {"sha": head_sha(self.root)})
        self.answer(404, {"message": "Not Found"})

    def github(self, method, path):
        self.body()
        sha = head_sha(self.root)
        routes = [
            ("POST", r"/app/installations/\d+/access_tokens", 201, {"token": "ghs_smoke_clone", "expires_at": "2099-01-01T00:00:00Z"}),
            ("DELETE", r"/installation/token", 204, None),
            ("GET", r"/repositories/%d" % REPO_ID, 200, REPO),
            ("GET", r"/repos/%s/git/ref/heads/main" % FULL_NAME, 200, {"object": {"sha": sha}}),
            ("GET", r"/repos/%s/commits/[0-9a-f]+" % FULL_NAME, 200, {"commit": {"message": "smoke site"}}),
            ("POST", r"/login/oauth/access_token", 200, {"access_token": "gho_smoke_user"}),
            ("GET", r"/user/installations", 200, {"installations": [{"id": INSTALLATION, "account": {"login": "acme"}}]}),
            ("GET", r"/user/installations/%d/repositories" % INSTALLATION, 200, {"repositories": [REPO]}),
            ("DELETE", r"/applications/[^/]+/token", 204, None),
        ]
        for want, pattern, status, payload in routes:
            if method == want and re.fullmatch(pattern, path):
                return self.answer(status, payload)
        self.answer(404, {"message": "Not Found"})

    def cf(self, method, path):
        raw = self.body()
        if method == "POST" and path.endswith("/assets-upload-session"):
            manifest = json.loads(raw)["manifest"]
            with STATE["lock"]:
                STATE["manifest"] = manifest
            hashes = sorted({entry["hash"] for entry in manifest.values()})
            return self.cloudflare(200, {"jwt": "smoke-upload", "buckets": [hashes] if hashes else []})
        if method == "POST" and path.endswith("/workers/assets/upload"):
            message = BytesParser(policy=policy.default).parsebytes(
                b"Content-Type: " + self.headers["Content-Type"].encode() + b"\r\n\r\n" + raw)
            with STATE["lock"]:
                for part in message.iter_parts():
                    content = base64.b64decode(part.get_payload(decode=True) or b"")
                    STATE["bodies"][part.get_param("name", header="content-disposition")] = content.decode("utf-8", "replace")
            return self.cloudflare(201, {"jwt": "smoke-complete"})
        return self.cloudflare(200, {})

    def git(self, method):
        raw = self.body()
        path, _, query = self.path.partition("?")
        env = dict(os.environ, GIT_PROJECT_ROOT=self.root, GIT_HTTP_EXPORT_ALL="1", REQUEST_METHOD=method,
                   PATH_INFO=path[3:], QUERY_STRING=query, CONTENT_TYPE=self.headers.get("Content-Type", ""),
                   CONTENT_LENGTH=str(len(raw)), GIT_PROTOCOL=self.headers.get("Git-Protocol", ""), REMOTE_ADDR="127.0.0.1")
        backend = subprocess.run(["git", "http-backend"], input=raw, env=env, capture_output=True, check=False)
        if backend.returncode != 0 and not backend.stdout:
            sys.stderr.write("fakes: git http-backend: " + backend.stderr.decode("utf-8", "replace"))
            return self.answer(500, {"message": "git http-backend failed"})
        out = backend.stdout
        # CGI headers end at the first blank line; git http-backend ends its lines with a bare \n.
        match = re.search(rb"\r?\n\r?\n", out)
        head, payload = (out[:match.start()], out[match.end():]) if match else (b"", out)
        status = 200
        headers = []
        for line in head.decode().splitlines():
            name, _, value = line.partition(":")
            if name.lower() == "status":
                status = int(value.strip().split()[0])
            elif name:
                headers.append((name, value.strip()))
        self.send_response(status)
        for name, value in headers:
            self.send_header(name, value)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


# The fixture site: no dependencies, so the install needs no registry; probe.sh records what the build reaches.
FIXTURE = {
    "package.json": '{"name":"smoke-site","version":"1.0.0","private":true,"scripts":{"build":"node build.js"}}\n',
    "build.js": 'const fs = require("fs");\nfs.mkdirSync("dist", { recursive: true });\n'
                'fs.writeFileSync("dist/index.html", "<h1>" + process.env.GREETING + "</h1>\\n");\n',
    "probe.sh": """set -u
mkdir -p dist
reach() { if timeout 4 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null; then echo "$3=open"; else echo "$3=closed"; fi; }
code() { curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$1"; }
own=$(hostname -i | awk '{print $1}')
{
  reach 1.1.1.1 443 direct-internet
  reach 169.254.169.254 80 direct-metadata
  reach "${own%.*}.1" "$HOST_PORT" direct-host
  reach "$HOST_GATEWAY" "$HOST_PORT" direct-host-gateway
  echo "proxy-metadata=$(code http://169.254.169.254/latest/meta-data/)"
  echo "proxy-private=$(code http://10.0.0.1/)"
  echo "proxy-internet=$(code https://registry.npmjs.org/)"
} > dist/probes.txt
cat dist/probes.txt
""",
}


def make_repo(root):
    bare = os.path.join(root, FULL_NAME + ".git")
    if os.path.isdir(bare):
        return
    work = os.path.join(root, "work")
    os.makedirs(work, exist_ok=True)
    for name, body in FIXTURE.items():
        with open(os.path.join(work, name), "w", encoding="utf-8") as out:
            out.write(body)
    env = dict(os.environ, GIT_AUTHOR_NAME="smoke", GIT_AUTHOR_EMAIL="smoke@example.com",
               GIT_COMMITTER_NAME="smoke", GIT_COMMITTER_EMAIL="smoke@example.com")
    for args in (["init", "-q", "-b", "main"], ["add", "."], ["commit", "-q", "-m", "smoke site"]):
        subprocess.run(["git", "-C", work] + args, env=env, check=True)
    subprocess.run(["git", "clone", "-q", "--bare", work, bare], check=True)
    subprocess.run(["git", "-C", bare, "config", "uploadpack.allowAnySHA1InWant", "true"], check=True)


def main():
    port, root = int(sys.argv[1]), sys.argv[2]
    make_repo(root)
    Fakes.root = root
    ThreadingHTTPServer(("", port), Fakes).serve_forever()


if __name__ == "__main__":
    main()
