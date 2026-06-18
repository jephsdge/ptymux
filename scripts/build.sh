#!/usr/bin/env sh
set -eu

ROOT_DIR="$(CDPATH= cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="${OUT_DIR:-$ROOT_DIR/dist}"
GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"
CGO_ENABLED="${CGO_ENABLED:-0}"
BIN_NAME="${BIN_NAME:-ptymux}"
TARGET="${TARGET:-dist}"

build_one() {
	build_goos="$1"
	build_goarch="$2"
	build_out="$3"

	mkdir -p "$(dirname "$build_out")"
	echo "building $build_out for $build_goos/$build_goarch with CGO_ENABLED=$CGO_ENABLED"
	CGO_ENABLED="$CGO_ENABLED" GOOS="$build_goos" GOARCH="$build_goarch" \
		go build -trimpath -ldflags="-s -w" -o "$build_out" "$ROOT_DIR/cmd/ptymux"
	echo "$build_out"
}

case "$TARGET" in
	dist)
		build_one "$GOOS" "$GOARCH" "$OUT_DIR/$BIN_NAME"
		;;
	skill)
		assets_dir="$ROOT_DIR/skills/use-ptymux/assets"
		build_one "$GOOS" "$GOARCH" "$assets_dir/ptymux-$GOOS-$GOARCH"
		;;
	skill-all)
		assets_dir="$ROOT_DIR/skills/use-ptymux/assets"
		build_one linux amd64 "$assets_dir/ptymux-linux-amd64"
		build_one linux arm64 "$assets_dir/ptymux-linux-arm64"
		build_one darwin amd64 "$assets_dir/ptymux-darwin-amd64"
		build_one darwin arm64 "$assets_dir/ptymux-darwin-arm64"
		;;
	*)
		echo "unknown TARGET: $TARGET" >&2
		echo "valid TARGET values: dist, skill, skill-all" >&2
		exit 1
		;;
esac
