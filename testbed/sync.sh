#!/usr/bin/env bash
# Run on the development machine. Copies the committed branch to the server with a git
# bundle (no GitHub credentials needed, Linux line endings on the server), then overlays
# the working-tree copy of testbed/ so uncommitted testbed edits are included.
#
#   testbed/sync.sh [user@host] [remote dir]
set -euo pipefail
HOST=${1:-${EDGE_HOST:-yuvraj@10.0.118.48}}
RDIR=${2:-${EDGE_REPO:-edge-lab/repo}}
cd "$(git rev-parse --show-toplevel)"

BRANCH=$(git rev-parse --abbrev-ref HEAD)
BUNDLE=$(mktemp -t edge-XXXXXX.bundle)
trap 'rm -f "$BUNDLE"' EXIT
git bundle create -q "$BUNDLE" HEAD "$BRANCH"
scp -q -o BatchMode=yes "$BUNDLE" "$HOST:/tmp/edge-sync.bundle"
ssh -o BatchMode=yes "$HOST" "set -e
  if [ ! -d '$RDIR/.git' ]; then mkdir -p '$RDIR'; git -C '$RDIR' init -q; fi
  git -C '$RDIR' fetch -q /tmp/edge-sync.bundle '$BRANCH'
  git -C '$RDIR' checkout -q -f -B '$BRANCH' FETCH_HEAD
  rm -f /tmp/edge-sync.bundle"
git ls-files -co --exclude-standard testbed | tar -cf - -T - | ssh -o BatchMode=yes "$HOST" "tar -xf - -C '$RDIR'"
ssh -o BatchMode=yes "$HOST" "cd '$RDIR' && sed -i 's/\r\$//' testbed/*.sh && chmod +x testbed/*.sh && git log --oneline -1"
