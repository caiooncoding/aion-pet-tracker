#!/usr/bin/env bash
# Builds dist/PetTracker.exe and dist/PetTracker-Setup-<version>.exe.
# Needs: Go 1.24+, makensis (NSIS 3), rsrc (go install github.com/akavel/rsrc@v0.10.2).
# Usage: scripts/build.sh [version]   (default: the VERSION file)
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-$(cat VERSION)}"
VERSION="${VERSION#v}"
echo "==> Pet Tracker $VERSION"

RSRC="$(command -v rsrc || echo "$(go env GOPATH)/bin/rsrc")"
mkdir -p dist

echo "==> tests"
go vet ./...
go test ./...

echo "==> windows resources (icon + manifest)"
"$RSRC" -arch amd64 -ico installer/icon.ico -manifest installer/pettracker.manifest \
  -o cmd/pettracker/rsrc_windows_amd64.syso

echo "==> PetTracker.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -H=windowsgui -X main.version=$VERSION" \
  -o dist/PetTracker.exe ./cmd/pettracker

echo "==> installer"
makensis -V2 -DVERSION="$VERSION" -DSRCDIR=../dist installer/pettracker.nsi

ls -la dist
