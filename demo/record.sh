#!/bin/sh
# Records demo/tiimi-demo.gif and .mp4 from demo/demo.tape, on sample data only.
# Needs vhs (brew install vhs) and python3. Your real ~/.tk and agent logs are never read.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
repo=$(dirname "$here")
D=$(mktemp -d "${TMPDIR:-/tmp}/tiimi-demo.XXXXXX")
mkdir -p "$D/home/shop" "$D/bin"
(cd "$repo" && go build -o "$D/bin/tk" .)
python3 "$here/seed.py" history "$D"
git -C "$D/home/shop" init -q
cat > "$D/env.sh" <<EOF
export HOME="$D/home" TK_HOME="$D/home/.tk" TK_SECRET_STORE=file TK_MODE=local
export TK_CLAUDE_PROJECTS="$D/home/.claude/projects" TK_CODEX_SESSIONS="$D/home/.codex/sessions"
export PATH="$D/bin:\$PATH"
seed() { python3 "$here/seed.py" live "$D" "\$1"; printf '\033[1A\033[2K'; }
export PS1='\[\033[1;32m\]~/shop\[\033[0m\] \$ '
cd "$D/home/shop"
EOF
cd "$repo"
TK_DEMO_ENV="$D/env.sh" vhs demo/demo.tape
echo "recorded demo/tiimi-demo.gif and demo/tiimi-demo.mp4 (sample data in $D)"
