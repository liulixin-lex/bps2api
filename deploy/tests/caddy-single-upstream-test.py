#!/usr/bin/env python3
"""Replay three business 503s, then verify the healthy homepage stays reachable.

Runs real Caddy and a disposable HTTP upstream on loopback; no TLS issuance,
production service, external model, or persistent server state is involved.
Requires Python 3 and caddy. --observe records legacy behavior for rollback tests.
"""
import argparse
import http.client
import http.server
import json
import os
import pathlib
import socket
import subprocess
import tempfile
import threading
import time


class Upstream(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"busy" if self.path == "/busy" else b"healthy"
        self.send_response(503 if self.path == "/busy" else 200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


def proxy_handlers(value):
    if isinstance(value, dict):
        if value.get("handler") == "reverse_proxy":
            yield value
        for child in value.values():
            yield from proxy_handlers(child)
    elif isinstance(value, list):
        for child in value:
            yield from proxy_handlers(child)


def request(port, path):
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
    try:
        conn.request("GET", path)
        response = conn.getresponse()
        response.read()
        return response.status
    finally:
        conn.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("caddyfile", type=pathlib.Path)
    parser.add_argument("--observe", action="store_true")
    args = parser.parse_args()
    adapted = subprocess.run(
        ["caddy", "adapt", "--config", str(args.caddyfile), "--adapter", "caddyfile"],
        capture_output=True, text=True, check=True,
    )
    handlers = list(proxy_handlers(json.loads(adapted.stdout)))
    assert len(handlers) == 1, "fixture must contain exactly one proxy"
    proxy = handlers[0]
    assert len(proxy["upstreams"]) == 1, "fixture must use one backend"
    upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    try:
        proxy["upstreams"][0]["dial"] = "127.0.0.1:%d" % upstream.server_port
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        with tempfile.TemporaryDirectory(prefix="sub2api-proxy-test-") as tmp:
            config = {
                "admin": {"disabled": True},
                "storage": {"module": "file_system", "root": tmp + "/storage"},
                "apps": {"http": {"servers": {"test": {
                    "listen": ["127.0.0.1:%d" % port],
                    "automatic_https": {"disable": True},
                    "routes": [{"handle": [proxy]}],
                }}}},
            }
            path = pathlib.Path(tmp) / "config.json"
            path.write_text(json.dumps(config))
            env = dict(os.environ, XDG_CONFIG_HOME=tmp, XDG_DATA_HOME=tmp)
            with (pathlib.Path(tmp) / "caddy.log").open("w+") as log:
                process = subprocess.Popen(["caddy", "run", "--config", str(path)], stdout=log, stderr=log, env=env)
                try:
                    deadline = time.monotonic() + 10
                    while True:
                        if process.poll() is not None:
                            log.seek(0)
                            raise RuntimeError(log.read())
                        try:
                            if request(port, "/") == 200:
                                break
                        except OSError:
                            pass
                        if time.monotonic() >= deadline:
                            raise TimeoutError("Caddy did not become ready")
                        time.sleep(0.05)
                    busy = [request(port, "/busy") for _ in range(3)]
                    homepage = request(port, "/")
                    print(json.dumps({"busy_requests": busy, "homepage_after_busy": homepage}))
                    assert busy == [503, 503, 503], "replay must exercise real upstream 503 responses"
                    if not args.observe:
                        assert homepage == 200, "business 503s must not evict the only backend"
                finally:
                    process.terminate()
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
    finally:
        upstream.shutdown()
        upstream.server_close()
        thread.join(timeout=5)


if __name__ == "__main__":
    main()
