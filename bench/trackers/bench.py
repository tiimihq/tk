#!/usr/bin/env python3
"""Tracker-integration benchmark: tokens, cost and time an agent spends on the same
tracker workflow through different integrations.

  bench.py <integration> [reps] [model]
  integrations: baseline, tk-local, beads, tk-github, gh-cli, github-mcp, tk-linear, linear-mcp
  bench.py cleanup        close/archive the shared noise issues on GitHub and Linear

Workflow (identical for all): find the open issue about EU refunds, read it with its full
comment history, record a progress note, hand it back (open / not in progress). No file edits.

Each rep runs in a fresh folder; local trackers get a fresh store seeded from seed.json.
Results append to $OUT/results.jsonl (default: bench/trackers/results.jsonl).
Claude Code runs with --setting-sources project,local, so only each integration's own
project setup applies, never your global settings.
"""
import json, os, shutil, subprocess, sys, tempfile, time

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
SEED = json.load(open(os.path.join(HERE, "seed.json")))
OUT = os.environ.get("OUT", HERE)
NOTE = "Benchmark: re-checked the plan; the next session implements the no-round-trip fix in refunds/service.py."

TOOL_NAME = {
    "tk-local": "the tk CLI", "tk-github": "the tk CLI", "tk-linear": "the tk CLI",
    "beads": "the bd (Beads) CLI", "gh-cli": "the gh CLI (repo {repo})",
    "github-mcp": "the GitHub MCP tools (repo {repo})", "linear-mcp": "the Linear MCP tools",
}
PROMPT = ("Using {tool}, find the open issue about EU refunds being a cent off, read it together "
          "with its full comment history, then record this progress note on it, word for word: "
          "'" + NOTE + "' Then hand the issue back for the next session: it must end up open and "
          "not in progress, with that note recorded. Do not edit any files. Reply DONE when finished.")


def sh(cmd, cwd=None, env=None, check=True):
    r = subprocess.run(cmd, cwd=cwd, env=env, shell=isinstance(cmd, str), capture_output=True, text=True)
    if check and r.returncode != 0:
        raise RuntimeError(f"{cmd}: {r.stderr.strip()[:400]}")
    return r.stdout


def build_tk(bindir):
    os.makedirs(bindir, exist_ok=True)
    sh(["go", "build", "-o", os.path.join(bindir, "tk"), "."], cwd=REPO)


def setup_tk_local(proj, env):
    sh("git init -q", cwd=proj, env=env)
    sh(["tk", "init"], cwd=proj, env=env)
    for t, d in SEED["others"][:2]:
        sh(["tk", "new", t, "--", d], cwd=proj, env=env)
    out = sh(["tk", "new", SEED["target"]["title"], "--", SEED["target"]["description"]], cwd=proj, env=env)
    key = out.split()[0]
    for c in SEED["target"]["comments"]:
        sh(["tk", "comment", key, c], cwd=proj, env=env)
    for t, d in SEED["others"][2:]:
        sh(["tk", "new", t, "--", d], cwd=proj, env=env)
    return key


def check_tk_local(proj, env, key):
    raw = json.loads(sh(["tk", "peek", key, "--json"], cwd=proj, env=env))
    ctx = sh(["tk", "resume", key, "--full"], cwd=proj, env=env)
    return raw.get("status") in ("todo", "backlog") and "Benchmark:" in ctx


def setup_beads(proj, env):
    sh("git init -q", cwd=proj, env=env)
    sh(["bd", "init", "--non-interactive"], cwd=proj, env=env)
    sh(["bd", "setup", "claude"], cwd=proj, env=env)  # project scope (no --global)
    ids = []
    for t, d in SEED["others"][:2]:
        sh(["bd", "create", t, "-d", d], cwd=proj, env=env)
    target = json.loads(sh(["bd", "create", SEED["target"]["title"], "-d", SEED["target"]["description"], "--json"], cwd=proj, env=env))
    key = target["id"] if isinstance(target, dict) else target[0]["id"]
    for c in SEED["target"]["comments"]:
        sh(["bd", "comment", key, c], cwd=proj, env=env)
    for t, d in SEED["others"][2:]:
        sh(["bd", "create", t, "-d", d], cwd=proj, env=env)
    return key


def check_beads(proj, env, key):
    raw = sh(["bd", "show", key, "--json"], cwd=proj, env=env)
    data = json.loads(raw)
    issue = data[0] if isinstance(data, list) else data
    text = raw + sh(["bd", "comments", key, "--json"], cwd=proj, env=env, check=False)
    return issue.get("status") == "open" and "Benchmark:" in text


GH_REPO = os.environ.get("BENCH_GH_REPO", "Prateek-Vortex/tk-sandbox")


def gh_noise(env):
    """The other open issues, created once and reused (label bench-noise)."""
    have = json.loads(sh(["gh", "issue", "list", "-R", GH_REPO, "--label", "bench-noise", "--state", "open",
                          "--json", "number"], env=env))
    if len(have) >= len(SEED["others"]):
        return
    sh(["gh", "label", "create", "bench-noise", "-R", GH_REPO, "--force"], env=env)
    for t, d in SEED["others"]:
        sh(["gh", "issue", "create", "-R", GH_REPO, "-t", t, "-b", d, "-l", "bench-noise", "-a", "@me"], env=env)


def setup_github(proj, env):
    sh("git init -q", cwd=proj, env=env)
    gh_noise(env)
    url = sh(["gh", "issue", "create", "-R", GH_REPO, "-t", SEED["target"]["title"],
              "-b", SEED["target"]["description"], "-a", "@me"], env=env).strip()
    num = url.rsplit("/", 1)[1]
    for c in SEED["target"]["comments"]:
        sh(["gh", "issue", "comment", num, "-R", GH_REPO, "-b", c], env=env)
    return num


def check_github(proj, env, num):
    raw = json.loads(sh(["gh", "issue", "view", num, "-R", GH_REPO, "--json", "state,labels,comments"], env=env))
    labels = [l["name"].lower() for l in raw["labels"]]
    ok = raw["state"] == "OPEN" and "in progress" not in labels and any("Benchmark:" in c["body"] for c in raw["comments"])
    sh(["gh", "issue", "close", num, "-R", GH_REPO, "-c", "benchmark run finished"], env=env, check=False)
    return ok


def setup_tk_github(proj, env):
    os.makedirs(env["TK_HOME"], exist_ok=True)
    with open(os.path.join(env["TK_HOME"], "config.json"), "w") as f:
        json.dump({"mode": "github", "github": {"repo": GH_REPO}}, f)
    num = setup_github(proj, env)
    sh(["tk", "init"], cwd=proj, env=env)
    return num


LINEAR_TEAM = os.environ.get("BENCH_LINEAR_TEAM", "FOC")


def linear_key():
    k = os.environ.get("LINEAR_API_KEY", "")
    if not k:
        with open(os.path.expanduser("~/.tk/linear_key")) as f:
            k = f.read().strip()
    return k


def linear_gql(query, variables=None):
    import urllib.request
    req = urllib.request.Request("https://api.linear.app/graphql", method="POST",
                                 data=json.dumps({"query": query, "variables": variables or {}}).encode(),
                                 headers={"Authorization": linear_key(), "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        out = json.load(r)
    if out.get("errors"):
        raise RuntimeError(str(out["errors"])[:300])
    return out["data"]


def linear_create(title, desc, me, team):
    d = linear_gql("mutation($i: IssueCreateInput!){ issueCreate(input:$i){ issue { id identifier } } }",
                   {"i": {"title": title, "description": desc, "teamId": team, "assigneeId": me}})
    return d["issueCreate"]["issue"]


def linear_ids():
    d = linear_gql("query($k: String!){ viewer { id } teams(filter:{key:{eq:$k}}){ nodes { id } } }", {"k": LINEAR_TEAM})
    return d["viewer"]["id"], d["teams"]["nodes"][0]["id"]


def linear_noise():
    me, team = linear_ids()
    have = linear_gql("query($t: String!){ issues(filter:{title:{startsWith:\"[bench] \"}, team:{key:{eq:$t}}, "
                      "state:{type:{nin:[\"canceled\",\"completed\"]}}}){ nodes { id } } }", {"t": LINEAR_TEAM})
    if len(have["issues"]["nodes"]) < len(SEED["others"]):
        for t, d in SEED["others"]:
            linear_create("[bench] " + t, d, me, team)
    return me, team


def setup_linear(proj, env):
    sh("git init -q", cwd=proj, env=env)
    me, team = linear_noise()
    issue = linear_create("[bench] " + SEED["target"]["title"], SEED["target"]["description"], me, team)
    for c in SEED["target"]["comments"]:
        linear_gql("mutation($i: CommentCreateInput!){ commentCreate(input:$i){ success } }",
                   {"i": {"issueId": issue["id"], "body": c}})
    return issue["id"]


def check_linear(proj, env, issue_id):
    d = linear_gql("query($id: String!){ issue(id:$id){ state { type } comments { nodes { body } } } }", {"id": issue_id})
    st = d["issue"]["state"]["type"]
    ok = st in ("unstarted", "backlog", "triage") and any("Benchmark:" in c["body"] for c in d["issue"]["comments"]["nodes"])
    linear_gql("mutation($id: String!){ issueArchive(id:$id){ success } }", {"id": issue_id})
    return ok


def setup_tk_linear(proj, env):
    os.makedirs(env["TK_HOME"], exist_ok=True)
    with open(os.path.join(env["TK_HOME"], "config.json"), "w") as f:
        json.dump({"mode": "linear", "linear": {"team": LINEAR_TEAM}}, f)
    iid = setup_linear(proj, env)
    sh(["tk", "init"], cwd=proj, env=env)
    return iid


SETUP = {"tk-local": (setup_tk_local, check_tk_local), "beads": (setup_beads, check_beads),
         "tk-linear": (setup_tk_linear, check_linear), "linear-mcp": (setup_linear, check_linear),
         "tk-github": (setup_tk_github, check_github), "gh-cli": (setup_github, check_github),
         "github-mcp": (setup_github, check_github)}

# Remote MCP servers; tokens come from the environment at run time and are never written down.
MCP = {"github-mcp": {"mcpServers": {"github": {"type": "http", "url": "https://api.githubcopilot.com/mcp/",
                                                "headers": {"Authorization": "Bearer ${BENCH_GH_TOKEN}"}}}},
       "linear-mcp": {"mcpServers": {"linear": {"type": "http", "url": "https://mcp.linear.app/mcp",
                                                "headers": {"Authorization": "Bearer ${BENCH_LINEAR_TOKEN}"}}}}}


def run(integration, rep, model, bindir):
    work = tempfile.mkdtemp(prefix=f"trk-{integration}-")
    proj = os.path.join(work, "shop")
    os.makedirs(proj)
    env = dict(os.environ, PATH=bindir + os.pathsep + os.environ["PATH"],
               TK_HOME=os.path.join(work, "tkhome"), TK_MODE="local", TK_SECRET_STORE="file")
    if integration in ("tk-github", "tk-linear"):
        env.pop("TK_MODE")            # mode comes from the run's config.json
        env.pop("TK_SECRET_STORE")    # use your real key store (Keychain / gh login)
    extra = []
    if integration in MCP:
        if integration == "github-mcp":
            env["BENCH_GH_TOKEN"] = sh(["gh", "auth", "token"]).strip()
        if integration == "linear-mcp":
            env["BENCH_LINEAR_TOKEN"] = linear_key()
        cfg = os.path.join(work, "mcp.json")
        with open(cfg, "w") as f:
            json.dump(MCP[integration], f)
        extra = ["--mcp-config", cfg, "--strict-mcp-config"]
    if integration == "tk-linear":
        env["LINEAR_API_KEY"] = linear_key()
    key, check = None, None
    if integration == "baseline":
        sh("git init -q", cwd=proj, env=env)
        prompt = "Reply DONE."
    else:
        setup, check = SETUP[integration]
        key = setup(proj, env)
        prompt = PROMPT.format(tool=TOOL_NAME[integration].format(repo=GH_REPO))
    t0 = time.time()
    r = subprocess.run(["claude", "-p", prompt, *extra, "--model", model, "--output-format", "json",
                        "--max-budget-usd", "0.75", "--permission-mode", "acceptEdits",
                        "--setting-sources", "project,local",
                        "--allowedTools", "Read,Glob,Grep,Bash(tk *),Bash(bd *),Bash(gh *),Bash(git status*),mcp__github,mcp__linear"],
                       cwd=proj, env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True)
    wall = time.time() - t0
    try:
        res = json.loads(r.stdout)
    except Exception:
        res = {"is_error": True, "result": (r.stdout or r.stderr)[:300]}
    u = res.get("usage") or {}
    ok = check(proj, env, key) if check else res.get("result", "").strip().startswith("DONE")
    row = {"integration": integration, "rep": rep, "model": model, "ok": ok,
           "cost": round(res.get("total_cost_usd", 0), 5), "wall_s": round(wall, 1),
           "api_s": round(res.get("duration_api_ms", 0) / 1000, 1), "turns": res.get("num_turns"),
           "in": u.get("input_tokens", 0), "cache_write": u.get("cache_creation_input_tokens", 0),
           "cache_read": u.get("cache_read_input_tokens", 0), "out": u.get("output_tokens", 0),
           "error": res.get("subtype") if res.get("is_error") else None, "work": work}
    row["tokens_in_total"] = row["in"] + row["cache_write"] + row["cache_read"]
    with open(os.path.join(OUT, "results.jsonl"), "a") as f:
        f.write(json.dumps(row) + "\n")
    print(json.dumps({k: row[k] for k in ("integration", "rep", "ok", "cost", "wall_s", "turns", "tokens_in_total", "out")}))
    return row


def cleanup():
    """Close/archive the shared noise issues the benchmark created."""
    for n in json.loads(sh(["gh", "issue", "list", "-R", GH_REPO, "--label", "bench-noise", "--state", "open", "--json", "number"])):
        sh(["gh", "issue", "close", str(n["number"]), "-R", GH_REPO, "-c", "benchmark finished"], check=False)
    try:
        nodes = linear_gql("query($t: String!){ issues(filter:{title:{startsWith:\"[bench] \"}, team:{key:{eq:$t}}}){ nodes { id } } }",
                           {"t": LINEAR_TEAM})["issues"]["nodes"]
        for n in nodes:
            linear_gql("mutation($id: String!){ issueArchive(id:$id){ success } }", {"id": n["id"]})
    except FileNotFoundError:
        pass


if __name__ == "__main__":
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    if sys.argv[1] == "cleanup":
        cleanup()
        sys.exit(0)
    integ, reps = sys.argv[1], int(sys.argv[2]) if len(sys.argv) > 2 else 3
    model = sys.argv[3] if len(sys.argv) > 3 else "claude-sonnet-5-5"
    bindir = tempfile.mkdtemp(prefix="trk-bin-")
    build_tk(bindir)
    for i in range(1, reps + 1):
        run(integ, i, model, bindir)
