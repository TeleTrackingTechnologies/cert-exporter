#!/usr/bin/env bash

set -euo pipefail
IMAGE="docker.teledev.io/cert-exporter"
VERSION=$(semversioner current-version)

docker build -t "${IMAGE}:${VERSION}" .

docker build -t "${IMAGE}:latest" .
