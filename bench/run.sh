#!/bin/sh
# Handoff benchmark: does session B finish cheaper/faster when session A left a tk handoff?
#   bench/run.sh [runs-per-condition] [model]
# Each run copies bench/task, lets session A fix only the tax tests, then a fresh
# session B makes all tests pass. Condition "tk": A ends with tk handoff and B starts
# with tk start. Condition "none": A's summary is printed and lost, B gets only the code.
# Results: one JSON line per session in $OUT/results.jsonl. Stops if spend passes $STOP_USD.
set -eu
RUNS=${1:-3}
MODEL=${2:-claude-sonnet-5-5}
CAP=${CAP_USD:-0.75}
STOP=${STOP_USD:-9}
here=$(cd "$(dirname "$0")" && pwd)
repo=$(dirname "$here")
OUT=${OUT:-$(mktemp -d "${TMPDIR:-/tmp}/tiimi-bench.XXXXXX")}
mkdir -p "$OUT/bin"
(cd "$repo" && go build -o "$OUT/bin/tk" .)
export PATH="$OUT/bin:$PATH" TK_SECRET_STORE=file TK_MODE=local
echo "results: $OUT/results.jsonl"

TOOLS='Read,Edit,Write,Glob,Grep,Bash(python3 -m unittest*),Bash(python3 -c*),Bash(tk *),Bash(ls*),Bash(cat*),Bash(git diff*),Bash(git status*)'

A_TASK='The tests in this repo fail in two areas: tax and refunds. In this session fix ONLY the tax tests (tests/test_tax.py). Do not touch shop/refund.py or tests/test_refund.py: another session will do refunds later.'
A_TK="Run \`tk start LOC-1\` first. $A_TASK When the tax tests pass, record a handoff for the next session with \`tk handoff \"...\"\`: what you changed, what is left, and anything you tried that did not work."
A_NONE="$A_TASK When the tax tests pass, end with a short summary: what you changed, what is left, and anything you tried that did not work."
B_TK='Run `tk start LOC-1` first and read the brief. Then make all tests pass (python3 -m unittest). Finish with `tk done "..."`.'
B_NONE='Make all tests pass (python3 -m unittest).'

spent() { python3 -c "import json,sys; print(round(sum(json.loads(l).get('cost',0) for l in open(sys.argv[1])),4))" "$OUT/results.jsonl" 2>/dev/null || echo 0; }

session() { # cond run phase prompt
  dir="$OUT/$1-$2/shop"
  start=$(date +%s)
  (cd "$dir" && claude -p "$4" --model "$MODEL" --output-format json --max-budget-usd "$CAP" \
      --permission-mode acceptEdits --setting-sources project,local --allowedTools "$TOOLS" \
      < /dev/null > "$OUT/$1-$2-$3.json" 2> "$OUT/$1-$2-$3.err") || true
  pass=$(cd "$dir" && python3 -m unittest 2>&1 | tail -1)
  python3 - "$OUT/$1-$2-$3.json" "$1" "$2" "$3" "$pass" "$(( $(date +%s) - start ))" >> "$OUT/results.jsonl" <<'PY'
import json, sys
f, cond, run, phase, tests, secs = sys.argv[1:]
try:
    r = json.load(open(f))
except Exception:
    r = {}
u = r.get("usage") or {}
print(json.dumps({"cond": cond, "run": int(run), "phase": phase, "cost": r.get("total_cost_usd", 0),
                  "turns": r.get("num_turns"), "secs": int(secs), "tests": tests,
                  "error": r.get("subtype") if r.get("is_error") or r.get("subtype") not in (None, "success") else None,
                  "in": u.get("input_tokens"), "cache_read": u.get("cache_read_input_tokens"),
                  "cache_write": u.get("cache_creation_input_tokens"), "out": u.get("output_tokens")}))
PY
  tail -1 "$OUT/results.jsonl"
}

for run in $(seq 1 "$RUNS"); do
  for cond in tk none; do
    if [ "$(python3 -c "print($(spent) > $STOP)")" = True ]; then echo "stopping: spend passed \$$STOP"; exit 0; fi
    w="$OUT/$cond-$run"; rm -rf "$w"; mkdir -p "$w"
    cp -R "$here/task" "$w/shop"
    export TK_HOME="$w/tkhome"
    (cd "$w/shop" && git init -q && git add -A && git -c user.name=bench -c user.email=bench@example.com commit -qm task)
    if [ "$cond" = tk ]; then
      (cd "$w/shop" && tk init >/dev/null && tk new "Make the failing tests pass" -- "Tax and refund tests fail" >/dev/null)
      session "$cond" "$run" A "$A_TK"
      session "$cond" "$run" B "$B_TK"
    else
      session "$cond" "$run" A "$A_NONE"
      session "$cond" "$run" B "$B_NONE"
    fi
  done
done
echo "total spend: \$$(spent)"
