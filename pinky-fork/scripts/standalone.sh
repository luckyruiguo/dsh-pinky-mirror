#!/bin/sh
# Builds the standalone Android prototype (android/standalone,
# docs/notes/standalone-phone.md): Pinky's Go core as libpinkycore.so for
# arm64 with the Android NDK, then the APK with the companion's Gradle.
#
#	scripts/standalone.sh          # build/…/app.pinky.standalone debug APK
#	scripts/standalone.sh core     # only the Go library
#
# Needs the Android SDK with an NDK (Android Studio's SDK Manager) and a JDK.
set -eu
cd "$(dirname "$0")/.."

ANDROID_HOME=${ANDROID_HOME:-$HOME/Library/Android/sdk}
JAVA_HOME=${JAVA_HOME:-/Applications/Android Studio.app/Contents/jbr/Contents/Home}
export ANDROID_HOME JAVA_HOME
NDK=${ANDROID_NDK_HOME:-$(ls -d "$ANDROID_HOME"/ndk/* 2>/dev/null | sort -V | tail -1)}
[ -d "$NDK" ] || { echo "standalone: no Android NDK under $ANDROID_HOME/ndk" >&2; exit 1; }
host=$(uname -s | tr '[:upper:]' '[:lower:]')-x86_64 # the NDK ships x86_64 hosts only; Apple silicon runs them
CC="$NDK/toolchains/llvm/prebuilt/$host/bin/aarch64-linux-android29-clang"

out=android/standalone/build/go/jniLibs/arm64-v8a
mkdir -p "$out"
version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
# Android 15 and later warn about libraries not aligned to 16 KB pages.
CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$CC" \
	go build -trimpath -buildmode=c-shared \
	-ldflags "-s -w -X main.version=$version -extldflags=-Wl,-z,max-page-size=16384" \
	-o "$out/libpinkycore.so" ./internal/standalone/libcore
rm -f "$out/libpinkycore.h"
ls -l "$out/libpinkycore.so"

[ "${1:-}" = core ] && exit 0
android/companion/gradlew --quiet -p android/standalone assembleDebug
ls -l android/standalone/build/outputs/apk/debug/*.apk
