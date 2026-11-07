#!/usr/bin/env python3
"""Sample agent logs for the Tiimi demo video. Everything here is made up.

  seed.py history <demo-dir>   30 days of Claude Code + Codex logs for a sample "shop" project
  seed.py live <demo-dir> claude|codex   the last few seconds of an agent's replies, written
                               just before it runs tk handoff / tk done in the recording

Logs go to <demo-dir>/home/.claude/projects and <demo-dir>/home/.codex/sessions, never
to your real ones; demo/demo.tape points tk there with TK_CLAUDE_PROJECTS etc.
"""
import json, os, random, sys, uuid
from datetime import datetime, timedelta, timezone

random.seed(7)
UTC = timezone.utc

def claude_line(at, cwd, branch, session, model, inp, cread, cwrite, out):
    mid = "msg_" + uuid.uuid4().hex[:20]
    return json.dumps({
        "type": "assistant", "requestId": "req_" + uuid.uuid4().hex[:16],
        "timestamp": at.strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z",
        "gitBranch": branch, "cwd": cwd, "sessionId": session,
        "message": {"id": mid, "model": model, "usage": {
            "input_tokens": inp, "cache_read_input_tokens": cread,
            "cache_creation_input_tokens": cwrite, "output_tokens": out,
            "cache_creation": {"ephemeral_5m_input_tokens": cwrite}}},
    }) + "\n"

def codex_file(root, at, cwd, branch, model, replies):
    d = os.path.join(root, at.strftime("%Y"), at.strftime("%m"), at.strftime("%d"))
    os.makedirs(d, exist_ok=True)
    sid = str(uuid.uuid4())
    ts = lambda t: t.strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
    lines = [json.dumps({"timestamp": ts(at), "type": "session_meta", "payload": {
                "id": sid, "cwd": cwd, "git": {"branch": branch}, "cli_version": "0.170.0"}}),
             json.dumps({"timestamp": ts(at), "type": "turn_context", "payload": {"model": model, "cwd": cwd}})]
    for i, (t, inp, cached, out) in enumerate(replies):
        u = {"input_tokens": inp, "cached_input_tokens": cached, "cache_write_input_tokens": 0,
             "output_tokens": out, "reasoning_output_tokens": 0, "total_tokens": inp + out}
        lines.append(json.dumps({"timestamp": ts(t), "type": "token_usage_record",
                                 "payload": {"response_id": "resp_" + uuid.uuid4().hex[:16], "usage": u}}))
    path = os.path.join(d, f"rollout-{ts(at)[:19].replace(':', '-')}-{sid}.jsonl")
    with open(path, "w") as f:
        f.write("\n".join(lines) + "\n")
    os.utime(path, (max(t for t, *_ in replies).timestamp(),) * 2)

def claude_dir(demo, cwd):
    d = os.path.join(demo, "home", ".claude", "projects", "".join(c if c.isalnum() or c == "-" else "-" for c in cwd))
    os.makedirs(d, exist_ok=True)
    return d

def opus_reply():  # ~8 cents at claude-opus-5-5 list prices
    return dict(inp=random.randint(2500, 6500), cread=random.randint(110_000, 190_000),
                cwrite=random.randint(0, 4000), out=random.randint(900, 2200))

# (branch, agent sessions, dollars) for the sample project
HISTORY = [
    ("prateek/eng-142-fix-refund-rounding", [("claude", 5), ("codex", 2)], 96.10),
    ("prateek/eng-131-migrate-billing-webhooks", [("claude", 3)], 58.40),
    ("prateek/eng-147-flaky-checkout-test", [("claude", 2), ("codex", 1)], 21.70),
    ("prateek/eng-150-cursor-pagination", [("codex", 1)], 12.90),
    ("main", [("claude", 4)], 31.20),
]

def history(demo):
    cwd = os.path.join(demo, "home", "shop")
    os.makedirs(cwd, exist_ok=True)
    cdir = claude_dir(demo, cwd)
    croot = os.path.join(demo, "home", ".codex", "sessions")
    now = datetime.now(UTC)
    for branch, sessions, dollars in HISTORY:
        total = sum(n for _, n in sessions)
        for agent, n in sessions:
            for _ in range(n):
                budget = dollars * 100 / total  # cents for this session
                start = now - timedelta(days=random.uniform(2, 26), hours=random.uniform(0, 8))
                t, spent = start, 0.0
                if agent == "claude":
                    sid = str(uuid.uuid4())
                    with open(os.path.join(cdir, sid + ".jsonl"), "w") as f:
                        while spent < budget:
                            r = opus_reply()
                            c = (r["inp"] * 4 + r["cread"] * 0.2 + r["cwrite"] * 5 + r["out"] * 20) / 1e4
                            if spent + c > budget + 2:
                                break
                            spent += c
                            t += timedelta(seconds=random.randint(20, 90))
                            f.write(claude_line(t, cwd, branch, sid, "claude-opus-5-5", **r))
                else:
                    replies = []
                    while spent < budget:  # gpt-6.1-sol: $2 in, $0.10 cached, $10 out per M
                        cached = random.randint(80_000, 160_000)
                        inp = cached + random.randint(3000, 9000)
                        out = random.randint(800, 2500)
                        c = ((inp - cached) * 2 + cached * 0.1 + out * 10) / 1e4
                        if spent + c > budget + 2:
                            break
                        spent += c
                        t += timedelta(seconds=random.randint(20, 90))
                        replies.append((t, inp, cached, out))
                    codex_file(croot, start, cwd, branch, "gpt-6.1-sol", replies)

def live(demo, agent):
    """Replies an agent made in the last few seconds, as its log would show them
    right before it runs tk handoff / tk done during the recording."""
    cwd = os.path.join(demo, "home", "shop")
    now = datetime.now(UTC)
    if agent == "claude":
        sid = str(uuid.uuid4())
        with open(os.path.join(claude_dir(demo, cwd), sid + ".jsonl"), "w") as f:
            for i in range(40):
                f.write(claude_line(now - timedelta(seconds=4 - i * 0.09), cwd, "main", sid,
                                    "claude-opus-5-5", **opus_reply()))
    else:
        replies = [(now - timedelta(seconds=4 - i * 0.12), 140_000 + i * 900, 128_000, 1600) for i in range(30)]
        codex_file(os.path.join(demo, "home", ".codex", "sessions"), now - timedelta(seconds=5), cwd, "main", "gpt-6.1-sol", replies)

if __name__ == "__main__":
    if len(sys.argv) < 3 or sys.argv[1] not in ("history", "live"):
        sys.exit(__doc__)
    if sys.argv[1] == "history":
        history(os.path.abspath(sys.argv[2]))
    else:
        live(os.path.abspath(sys.argv[2]), sys.argv[3] if len(sys.argv) > 3 else "claude")
