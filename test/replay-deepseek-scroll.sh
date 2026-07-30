#!/usr/bin/env bash
# replay-deepseek-scroll.sh — a REPLAYABLE two-step gesture on the already-running
# Firefox, driven through the shared BiDi broker (the CHANNEL atom, http-mcp's
# bidi_command sibling). Run it any time to re-drive AND self-verify the sequence.
#
#   Sequence: scroll the DeepSeek chat's virtual-list container one step, then again.
#   Each step asserts the container's scrollTop actually moved -> proof it happened.
#
# Why the broker and not geckodriver/:4444: Firefox (pid launched with --marionette
# --remote-debugging-port 9222) is shared by many agents over ONE BiDi socket held by
# a broker on :4445. We target the existing DeepSeek tab by its context id directly;
# we NEVER session.end / DeleteSession (that would kill every agent's session).
#
# Usage:  bash test/replay-deepseek-scroll.sh
# Env override:  BROKER=http://127.0.0.1:4445  STEP=600
set -euo pipefail

BROKER="${BROKER:-http://127.0.0.1:4445}"
STEP="${STEP:-600}"
DEEPSEEK_URL_MATCH="chat.deepseek.com"
SELECTOR=".ds-virtual-list.ds-scroll-area"

# --- one broker command; prints the raw JSON result -------------------------
cmd() { curl -sS -X POST "$BROKER/command" -H 'content-type: application/json' -d "$1"; }

# --- resolve the DeepSeek tab's context id from the live tree ---------------
echo "[*] broker: $BROKER  — locating the DeepSeek tab..."
TREE=$(cmd '{"method":"browsingContext.getTree","params":{}}')
CTX=$(printf '%s' "$TREE" | python3 -c '
import sys,json
d=json.load(sys.stdin)
def walk(ns):
    for n in ns:
        yield n
        yield from walk(n.get("children") or [])
for n in walk(d["result"]["contexts"]):
    if "'"$DEEPSEEK_URL_MATCH"'" in (n.get("url") or ""):
        print(n["context"]); break
')
if [ -z "${CTX:-}" ]; then echo "[!] DeepSeek tab not found on this channel" >&2; exit 1; fi
echo "[*] context = $CTX"

# --- run one scroll step in the real container; assert it moved -------------
# Direction is chosen so replay is always observable: if we're at the top, go
# down; otherwise go up. Returns before/after/dir as JSON.
scroll_step() {
  local n="$1"
  local expr='(()=>{const el=document.querySelector("'"$SELECTOR"'");if(!el)return JSON.stringify({error:"no-container"});const b=Math.round(el.scrollTop);const dir=b<=0?1:-1;el.scrollTop=b+dir*'"$STEP"';return JSON.stringify({before:b,after:Math.round(el.scrollTop),dir})})()'
  local body
  body=$(printf '{"method":"script.evaluate","params":{"awaitPromise":true,"expression":%s,"target":{"context":"%s"}}}' \
        "$(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$expr")" "$CTX")
  local res inner
  res=$(cmd "$body")
  inner=$(printf '%s' "$res" | python3 -c 'import sys,json;print(json.load(sys.stdin)["result"]["result"]["value"])')
  local before after
  before=$(printf '%s' "$inner" | python3 -c 'import sys,json;print(json.load(sys.stdin)["before"])')
  after=$(printf  '%s' "$inner" | python3 -c 'import sys,json;print(json.load(sys.stdin)["after"])')
  if [ "$before" = "$after" ]; then
    echo "[FAIL] step $n: scrollTop did not move ($before)"; exit 1
  fi
  echo "[PASS] step $n: scrollTop $before -> $after"
}

scroll_step 1
sleep 0.3
scroll_step 2
echo "[OK] replay complete: 2/2 steps drove a real scroll on the DeepSeek tab."
