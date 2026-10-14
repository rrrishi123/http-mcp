#!/usr/bin/env python3
"""painguard — the PUSH witness form factor (validates #611).

The passive PULL witness is dead: agents under deadline bypass it (my #68/#93,
proven by %7's 80-shot production consulting the wire 0 times). But the demand
is real — they hand-roll pain memos at the moment of failure. This closes the
gap from the other side: instead of waiting to be queried, the witness WATCHES
the captured request/response stream (the wire's replay sidecar, #116 fidelity)
and, on a pain signature, PUSHES an unprompted diagnosis to 8's work queue.

The load-bearing bet from #611: the witness holds the BODY, so it can classify
what a bare status/count cannot — is this 500 a driver-version drift (infra) or
a stale session (retry) or a real test bug? That classification, surfaced
unprompted, is what a mind-under-deadline could not get from staring at a 500.

Usage:
  painguard.py <replay.ndjson>            # dry-run: print classified pushes
  painguard.py <replay.ndjson> --push     # actually POST diagnoses to 8 /work
"""
import json
import os
import sys
import urllib.request

COLLECTOR = os.environ.get("COLLECTOR", "http://127.0.0.1:7070")
ACTOR = "painguard"


def classify(method, url, status, resp_body):
    """Map a witnessed failure to (kind, diagnosis, fix). Body-first — the
    whole point is reading the error, not just the status code."""
    b = (resp_body or "").lower()
    if "only supports chrome version" in b or "this version of chromedriver" in b:
        return ("INFRA", "driver/browser version drift — chromedriver vs installed browser",
                "align the driver to the browser version (local_drivers ensure <version>); NOT a test bug")
    if "no such element" in b or "unable to locate element" in b:
        return ("TEST", "locator did not resolve — selector stale or page changed",
                "re-check the selector against the live DOM; a site redesign is the usual cause")
    if "session-not-found" in b or "unable to find the session" in b:
        return ("STALE", "session id not on the hub — expired or wrong id",
                "create a fresh session; don't reuse a session id across runs")
    if status == 429:
        return ("WEDGE", "rate-limited — the slot is saturated",
                "BACK OFF; re-firing queues a backlog that wedges the slot (higgsfield #probe lesson)")
    if status == 501 or "unsupported method" in b:
        return ("API", "wrong HTTP method for this endpoint",
                "check the endpoint's method; a GET-only route was POSTed (or vice-versa)")
    return ("UNKNOWN", f"unclassified {status}",
            "no signature matched — witness holds the body for a human/mind to read")


def scan(records):
    """Yield a pain event per failure, plus a WEDGE escalation when the same
    path fails N+ times in a row (the repeated-failure signature #611 named)."""
    run = {}  # path -> consecutive-failure count
    for r in records:
        status = r.get("status")
        path = str(r.get("url", "")).split("?")[0]
        if not isinstance(status, int) or status < 400:
            run[path] = 0
            continue
        run[path] = run.get(path, 0) + 1
        kind, diag, fix = classify(r.get("method"), r.get("url"), status, r.get("resp_body"))
        yield {
            "kind": kind, "diag": diag, "fix": fix, "path": path,
            "status": status, "method": r.get("method"),
            "evidence": (r.get("resp_body") or "")[:180],
            "consecutive": run[path],
        }


def push(ev):
    text = (f"PUSH-WITNESS (painguard, moment-of-pain): {ev['kind']} on "
            f"{ev['method']} {ev['path']} → {ev['status']}"
            f"{' (x%d consecutive = WEDGE)' % ev['consecutive'] if ev['consecutive'] >= 3 else ''}. "
            f"DIAGNOSIS: {ev['diag']}. FIX: {ev['fix']}. "
            f"EVIDENCE (witnessed body): {ev['evidence']}")
    body = json.dumps({"text": text, "by": ACTOR}).encode()
    req = urllib.request.Request(COLLECTOR + "/work", data=body,
                                 headers={"Content-Type": "application/json", "X-8-Actor": ACTOR})
    with urllib.request.urlopen(req, timeout=10) as r:
        return r.read().decode()


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(2)
    path = sys.argv[1]
    do_push = "--push" in sys.argv[2:]
    records = [json.loads(l) for l in open(path) if l.strip()]
    events = list(scan(records))
    if not events:
        print("no pain in the stream — nothing to surface.")
        return
    for ev in events:
        print(f"[{ev['kind']}] {ev['method']} {ev['path']} → {ev['status']}: {ev['diag']}")
        print(f"    FIX: {ev['fix']}")
        if do_push:
            print(f"    pushed → {push(ev)}")


if __name__ == "__main__":
    main()
