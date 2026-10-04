#!/usr/bin/env bash
# Cross-compile nanoflux and its external plugins for armv6
# (Raspberry Pi Zero W: linux/arm/v6, ARM1176JZF-S).
#
# Usage:
#   scripts/build-armv6.sh            # binaries only
#   scripts/build-armv6.sh --image    # binaries + container image (reuses Dockerfile)
#
# Environment:
#   OUT   output directory          (default: dist/armv6)
#   IMAGE image tag for --image     (default: nanoflux:armv6)
#   RUNTIME docker|podman for --image (default: autodetect, prefers docker)
#
# Binaries land in $OUT:
#   $OUT/nanoflux
#   $OUT/plugins/nanoflux-plugin-<name>
#
# Nothing in the working tree (plugins/, docker-compose.yml, Makefile) is
# modified; the host/amd64 plugin binaries the local deployment uses are left
# alone. The image is built from a throwaway context under $OUT/image.
#
# Building the image runs the Dockerfile's `apk add` for the target platform,
# so the host needs qemu-user/binfmt_misc registered for arm (podman:
# `podman run --rm --privileged multiarch/qemu-user-static --reset -p yes`;
# docker: docker/setup-qemu-action or binfmt_misc).
set -euo pipefail

cd "$(dirname "$0")/.."

WANT_IMAGE=''
while [ $# -gt 0 ]; do
	case "$1" in
	--image) WANT_IMAGE=1 ;;
	-h | --help)
		sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "unknown argument: $1" >&2
		exit 2
		;;
	esac
	shift
done

# Building the image executes the Dockerfile's `apk add` for the target
# platform. That only works on an arm host or with qemu-arm binfmt registered.
if [ -n "$WANT_IMAGE" ]; then
	HOST_ARCH="$(go env GOHOSTARCH 2>/dev/null || uname -m)"
	case "$HOST_ARCH" in
	arm | armv6* | armv7*) ;;
	*)
		if [ ! -e /proc/sys/fs/binfmt_misc/qemu-arm ] && [ "${ALLOW_NO_QEMU:-0}" != 1 ]; then
			cat >&2 <<-'EOF'
				--image needs qemu-arm binfmt to run the armv6 Dockerfile.
				Register it, then re-run:
				  podman run --rm --privileged multiarch/qemu-user-static --reset -p yes
				On NixOS add to configuration.nix and rebuild:
				  boot.binfmt.emulatedSystems = [ "armv6l-linux" ];
				Set ALLOW_NO_QEMU=1 to skip this check (build will likely fail).
			EOF
			exit 1
		fi
		;;
	esac
fi

export GOOS=linux GOARCH=arm GOARM=6 CGO_ENABLED=0

OUT="${OUT:-dist/armv6}"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')}"

mkdir -p "$OUT/plugins"

echo "nanoflux ${VERSION} -> linux/arm/v6"
go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "$OUT/nanoflux" ./cmd/server

for mod in plugins/*/go.mod; do
	dir="$(dirname "$mod")"
	name="$(basename "$dir")"
	case "$name" in nanoflux-plugin-*) out="$name" ;; *) out="nanoflux-plugin-$name" ;; esac
	echo "plugin ${out} -> linux/arm/v6"
	go -C "$dir" build -trimpath -ldflags "-s -w" -o "$(pwd)/$OUT/plugins/$out" .
done

ls -lh "$OUT" "$OUT/plugins"

if [ -z "$WANT_IMAGE" ]; then
	echo "done (pass --image to also build the container image)"
	exit 0
fi

# Reuse the root Dockerfile. It does `COPY $TARGETPLATFORM/nanoflux`, so stage a
# context holding the Dockerfile and the binary under the platform's path.
IMAGE="${IMAGE:-nanoflux:armv6}"
RUNTIME="${RUNTIME:-$(command -v docker >/dev/null 2>&1 && echo docker || (command -v podman >/dev/null 2>&1 && echo podman || echo docker))}"
CTX="$OUT/image"

rm -rf "$CTX"
mkdir -p "$CTX/linux/arm/v6"
cp Dockerfile "$CTX/Dockerfile"
cp "$OUT/nanoflux" "$CTX/linux/arm/v6/nanoflux"

echo "building image ${IMAGE} (linux/arm/v6) with ${RUNTIME}"
if [ "$RUNTIME" = docker ] && docker buildx version >/dev/null 2>&1; then
	docker buildx build --platform linux/arm/v6 -t "$IMAGE" --load "$CTX"
else
	"$RUNTIME" build --platform linux/arm/v6 -t "$IMAGE" "$CTX"
fi

echo "done: image ${IMAGE}"
