#!/usr/bin/env sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
OUT_DIR="${OUT_DIR:-$ROOT_DIR/dist}"
GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"
CGO_ENABLED="${CGO_ENABLED:-0}"
BIN_NAME="${BIN_NAME:-ptymux}"
TARGET="${TARGET:-dist}"

if [ "$TARGET" = "skill" ]; then
	OUT_DIR="$ROOT_DIR/skills/use-ptymux/assets"
	BIN_NAME="ptymux"
fi

mkdir -p "$OUT_DIR"

echo "building $BIN_NAME for $GOOS/$GOARCH with CGO_ENABLED=$CGO_ENABLED"

CGO_ENABLED="$CGO_ENABLED" GOOS="$GOOS" GOARCH="$GOARCH" \
	go build -trimpath -ldflags="-s -w" -o "$OUT_DIR/$BIN_NAME" "$ROOT_DIR/cmd/ptymux"

echo "$OUT_DIR/$BIN_NAME"
