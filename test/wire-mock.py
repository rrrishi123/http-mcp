#!/usr/bin/env python3
# Multi-transport mock to probe http-mcp's atoms end-to-end.
# Serves: HTTP (GET/POST) + SSE on TCP :8791, and HTTP over a unix socket.
import json, os, socket, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from socketserver import ThreadingMixIn, UnixStreamServer

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _j(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code); self.send_header("Content-Type","application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        if self.path == "/hello":
            self._j(200, {"transport":"http","mode":"CALL","ok":True,"via":"tcp"})
        elif self.path == "/ping":  # unix socket route
            self._j(200, {"transport":"unix","mode":"CALL","ok":True,"via":"unix-socket"})
        elif self.path == "/events":  # SSE — afferent channel
            self.send_response(200); self.send_header("Content-Type","text/event-stream")
            self.send_header("Cache-Control","no-cache"); self.end_headers()
            for i in range(3):
                self.wfile.write(f"event: tick\ndata: {json.dumps({'seq':i,'transport':'sse'})}\n\n".encode())
                self.wfile.flush(); time.sleep(0.2)
            self.wfile.write(b"event: done\ndata: {\"closed\":true}\n\n"); self.wfile.flush()
        else:
            self._j(404, {"error":"no route","path":self.path})
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0)); raw = self.rfile.read(n).decode()
        try: parsed = json.loads(raw)
        except Exception: parsed = raw
        self._j(200, {"transport":"http","mode":"CALL","echo":parsed, "you_sent_bytes": n})

class TCP(ThreadingHTTPServer): daemon_threads = True
class UNIX(ThreadingMixIn, UnixStreamServer): daemon_threads = True

def run_unix(path):
    if os.path.exists(path): os.remove(path)
    UNIX(path, H).serve_forever()

if __name__ == "__main__":
    sock = "/tmp/wire-mock.sock"
    threading.Thread(target=run_unix, args=(sock,), daemon=True).start()
    print(f"mock up: tcp 127.0.0.1:8791  unix {sock}", flush=True)
    TCP(("127.0.0.1", 8791), H).serve_forever()
