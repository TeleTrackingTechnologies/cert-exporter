
#!/usr/bin/env bash

set -euo pipefail

IMAGE="docker.teledev.io/cert-exporter"
VERSION=$(semversioner current-version)

docker push "${IMAGE}:${VERSION}"

docker push "${IMAGE}:latest"