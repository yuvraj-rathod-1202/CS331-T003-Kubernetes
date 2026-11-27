#!/usr/bin/env bash
# Build the operator, health agent, link router and iotsim, and load the images onto
# every node. Go runs inside a container, so the host needs only Docker.
#
#   testbed/build.sh            build and load everything
#   testbed/build.sh --no-load  build only
set -euo pipefail
. "$(dirname "$0")/env.sh"

GO_IMAGE=${GO_IMAGE:-golang:1.26}
mkdir -p "$STATE_DIR/gocache"

log "Building operator binaries in $GO_IMAGE"
docker run --rm -u "$(id -u):$(id -g)" \
  -v "$REPO_DIR/operator:/src" -v "$STATE_DIR/gocache:/cache" \
  -e GOPATH=/cache/gopath -e GOCACHE=/cache/build -e GOFLAGS=-buildvcs=false \
  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 -w /src "$GO_IMAGE" sh -c '
    go build -o bin/healthagent ./cmd/healthagent &&
    go build -o bin/linkrouter ./cmd/linkrouter &&
    go build -o bin/manager ./cmd/main.go'

log "Building images"
docker build -q -f "$REPO_DIR/operator/Dockerfile.healthagent" -t "$IMG_AGENT" "$REPO_DIR/operator/bin" >/dev/null
docker build -q -f "$REPO_DIR/operator/Dockerfile.linkrouter" -t "$IMG_ROUTER" "$REPO_DIR/operator/bin" >/dev/null
docker build -q -t "$IMG_IOTSIM" "$REPO_DIR/testbed/iotsim" >/dev/null

[ "${1:-}" = "--no-load" ] && exit 0

if [ "$DISTRO" = k3s ]; then
  # K3s nodes run containerd: import into its k8s.io namespace on every node.
  log "Importing images into the K3s nodes"
  "$K3D" image import -c "$PROFILE" -m direct "$IMG_AGENT" "$IMG_ROUTER" "$IMG_IOTSIM" >/dev/null
else
  # `minikube image load` does not replace an existing tag, so load with docker directly.
  for n in $NODES; do
    log "Loading images into $n"
    docker save "$IMG_AGENT" "$IMG_ROUTER" "$IMG_IOTSIM" | docker exec -i "$n" docker load -q >/dev/null
  done
fi
log "Build done: $(docker image inspect "$IMG_ROUTER" --format '{{.Created}}')"
