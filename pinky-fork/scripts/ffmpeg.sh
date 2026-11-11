#!/bin/sh
# Builds the FFmpeg libraries Pinky ships (make ffmpeg, scripts/dist.sh): a
# minimal LGPL build of FFmpeg 9 with shared libraries, holding only what
# Pinky uses.
#
#   scripts/ffmpeg.sh <prefix> [arm64|amd64|windows-amd64|linux-amd64]
#
# Without a second argument it builds for this machine. On macOS, arm64 or
# amd64 picks the Mac's processor, so a Mac with Apple silicon builds the
# Intel libraries too; x86_64 needs nasm (brew install nasm) for FFmpeg's
# assembly, or FFMPEG_NO_ASM=1 for a slower build without it.
# windows-amd64 (Pinky.exe's DLLs, cross-compiled with mingw-w64) and
# linux-amd64 are built in a Linux container (Docker), the same on a Mac and
# on GitHub's runners: the Linux libraries with Ubuntu 20.04's glibc, 2.31,
# so that they load on the Linux systems of the last five years or so.
#
# - libavcodec: the H.264 and HEVC decoders and parsers, with VideoToolbox
#   on macOS and D3D11VA on Windows (internal/video decodes the phones'
#   streams with them; on Linux it decodes in software), and on Windows
#   the H.264 encoders the iPhone driver needs to re-encode the phones'
#   pictures (internal/driver/ios): Cisco's OpenH264, built from its
#   source and linked in, and Media Foundation's h264_mf after it;
# - libavutil and libswscale (hardware frames, pixel conversion);
# - libavformat with the MP4/MOV muxer and demuxer, the raw H.264/HEVC
#   demuxers, the file protocol and the Annex B bitstream filters, for
#   recordings that are remuxed without re-encoding.
#
# Nothing GPL or nonfree is enabled, and every library FFmpeg would find on
# the machine by itself (zlib, iconv, SDL, X11, VA-API...) is turned off, so
# the libraries depend on the system's own libraries alone, which the end of
# this script checks for Windows and Linux. The source is checked against
# its SHA-256 and kept in <prefix>-src for the next build.
set -eu

VERSION=9.0.2
SHA256=8c3850283eb25fa026482078a04051e0be17347b09ef81a0849bec15a96e002e
URL=https://ffmpeg.org/releases/ffmpeg-$VERSION.tar.xz

# FFmpeg 9's x86 assembly needs a newer nasm than Ubuntu 20.04's 2.14, so
# the Linux build makes its own (the checksum is Homebrew's too).
NASM_VERSION=3.02
NASM_SHA256=87336eba53b4acfe917424ab5d500d2b0054d9f5148d35c2273ccf2cfb712f0d
NASM_URL=https://www.nasm.us/pub/nasm/releasebuilds/$NASM_VERSION/nasm-$NASM_VERSION.tar.xz

# The Windows build carries Cisco's OpenH264 as the H.264 encoder the iPhone
# driver uses first: it gives each picture back as soon as it is encoded,
# where Media Foundation's holds a dozen back. Its source is BSD, which an
# LGPL build can carry (x264's could not); Cisco's patent licence goes with
# Cisco's own binaries, not with a build made here. It is built, as a static
# library, in the Windows container, so that the DLLs keep importing nothing
# but Windows' own libraries.
OPENH264_VERSION=2.7.0
OPENH264_SHA256=5178775c8b440073e70920e97d195efff721c0e56d0a78cc82c919e95d54924d
OPENH264_URL=https://github.com/cisco/openh264/archive/refs/tags/v$OPENH264_VERSION.tar.gz

usage="usage: scripts/ffmpeg.sh <prefix> [arm64|amd64|windows-amd64|linux-amd64]"
prefix=${1:?$usage}
target=${2:-}
case $prefix in /*) ;; *) prefix=$(pwd)/$prefix ;; esac

src=$prefix-src
# One copy of the source, next to the builds, serves every build there.
tarball=$(dirname "$prefix")/ffmpeg-$VERSION.tar.xz

sha256() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		sha256sum "$1" | cut -d' ' -f1
	fi
}

# fetch downloads a source tarball to a file, unless the file is there
# with the right SHA-256. Sources are downloaded here, before any
# container: a container may not share this machine's way to the network
# (a proxy, say), so it mounts the folder that holds them. ffmpeg.org drops
# a connection now and then, which curl's own retries do not cover.
fetch() {
	url=$1 want=$2 file=$3
	if [ -f "$file" ] && [ "$(sha256 "$file")" = "$want" ]; then
		return
	fi
	echo "Downloading $url"
	tries=0
	until curl -fsSL --retry 3 -o "$file.part" "$url"; do
		tries=$((tries + 1))
		if [ $tries -ge 5 ]; then
			echo "ffmpeg.sh: could not download $url" >&2
			rm -f "$file.part"
			exit 1
		fi
		sleep $((tries * 5))
	done
	got=$(sha256 "$file.part")
	if [ "$got" != "$want" ]; then
		echo "ffmpeg.sh: ${file##*/} has SHA-256 $got, want $want" >&2
		rm -f "$file.part"
		exit 1
	fi
	mv "$file.part" "$file"
}

mkdir -p "$src"
fetch "$URL" "$SHA256" "$tarball"

# Windows and Linux: run this script again in a container, unless it is
# running in one. The images and packages are here, so that the hash of
# this file, which names the builds that scripts/dist.sh and the release
# workflow keep, changes with them.
case $target in
windows-amd64 | linux-amd64)
	if [ -z "${FFMPEG_IN_CONTAINER:-}" ]; then
		nasm=
		openh264=
		case $target in
		windows-amd64)
			image=ubuntu:24.04
			packages="gcc libc6-dev nasm pkg-config gcc-mingw-w64-x86-64 g++-mingw-w64-x86-64 binutils-mingw-w64-x86-64 mingw-w64-x86-64-dev"
			openh264=openh264-$OPENH264_VERSION.tar.gz
			fetch "$OPENH264_URL" "$OPENH264_SHA256" "$(dirname "$prefix")/$openh264"
			;;
		linux-amd64)
			image=ubuntu:20.04
			packages="gcc libc6-dev patchelf"
			nasm=nasm-$NASM_VERSION.tar.xz
			fetch "$NASM_URL" "$NASM_SHA256" "$(dirname "$prefix")/$nasm"
			;;
		esac
		if ! docker info >/dev/null 2>&1; then
			echo "ffmpeg.sh: FFmpeg for $target is built in a Docker container, and Docker is not running here" >&2
			exit 1
		fi
		mkdir -p "$(dirname "$prefix")"
		script=$(cd "$(dirname "$0")" && pwd)/ffmpeg.sh
		exec docker run --rm --platform linux/amd64 \
			-e FFMPEG_IN_CONTAINER=1 -e PACKAGES="$packages" -e TARGET="$target" -e NASM="$nasm" \
			-e OPENH264="$openh264" \
			-e OUT="/out/$(basename "$prefix")" -e OWNER="$(id -u):$(id -g)" \
			-v "$script:/ffmpeg.sh:ro" -v "$(dirname "$prefix"):/out" \
			"$image" sh -c '
				set -e
				export DEBIAN_FRONTEND=noninteractive
				apt-get update -qq
				# shellcheck disable=SC2086 # the packages are split on purpose
				apt-get install -y -qq --no-install-recommends xz-utils make binutils $PACKAGES >/dev/null
				if [ -n "$NASM" ]; then
					mkdir /tmp/nasm
					tar -xJf "/out/$NASM" -C /tmp/nasm --strip-components=1
					(cd /tmp/nasm && ./configure --prefix=/usr/local >/dev/null && make -j"$(nproc)" >/dev/null && make install >/dev/null)
					echo "Built $(nasm -v)"
				fi
				if [ -n "$OPENH264" ]; then
					mkdir /tmp/openh264
					tar -xzf "/out/$OPENH264" -C /tmp/openh264 --strip-components=1
					# x86 assembly is kept (the container has nasm above);
					# the tag tarball has no GMP headers, so that plugin
					# and the unit tests are not built either.
					# Its own Makefile, the mingw-w64 compilers, that assembly,
					# and a static library installed under a prefix of its own.
					(cd /tmp/openh264 &&
						make -j"$(nproc)" OS=mingw_nt ARCH=x86_64 \
							CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
							AR=x86_64-w64-mingw32-ar PREFIX=/opt/pinky-openh264 >/dev/null &&
						make OS=mingw_nt ARCH=x86_64 \
							CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
							AR=x86_64-w64-mingw32-ar PREFIX=/opt/pinky-openh264 install-static >/dev/null)
					# OpenH264 is C++, and its .pc names the C++ runtime
					# (-lstdc++): FFmpeg then links with the C++ compiler,
					# which the flags below keep that runtime static, so
					# libstdc++-6.dll stays out of the imports of the
					# DLLs below.
					export PKG_CONFIG_PATH=/opt/pinky-openh264/lib/pkgconfig
					echo "Built OpenH264 from $OPENH264"
				fi
				status=0
				sh /ffmpeg.sh "$OUT" "$TARGET" || status=$?
				chown -R "$OWNER" "$OUT" "$OUT-src" 2>/dev/null || true
				exit $status'
	fi
	;;
esac

tree=$src/ffmpeg-$VERSION
rm -rf "$tree"
tar -xJf "$tarball" -C "$src"

flags="--enable-shared --disable-static --enable-pic
	--disable-programs --disable-doc --disable-debug
	--disable-autodetect --disable-network
	--disable-everything
	--disable-avdevice --disable-avfilter --disable-swresample
	--enable-decoder=h264,hevc
	--enable-parser=h264,hevc
	--enable-demuxer=mov,h264,hevc
	--enable-muxer=mp4,mov,h264,hevc
	--enable-protocol=file
	--enable-bsf=h264_mp4toannexb,hevc_mp4toannexb,extract_extradata,h264_metadata,hevc_metadata"

# nasm assembles FFmpeg's x86 code; without it, FFMPEG_NO_ASM=1 builds
# without that code, which decodes and converts slower.
need_nasm() {
	command -v nasm >/dev/null 2>&1 && return
	if [ "${FFMPEG_NO_ASM:-}" != 1 ]; then
		echo "ffmpeg.sh: FFmpeg for x86_64 needs nasm (brew install nasm), or FFMPEG_NO_ASM=1 for a build without its assembly, which decodes and converts slower" >&2
		exit 1
	fi
	echo "ffmpeg.sh: no nasm; building without FFmpeg's x86 assembly (FFMPEG_NO_ASM=1)" >&2
	flags="$flags
	--disable-x86asm"
}

case $target in
windows-amd64)
	# Pinky.exe's DLLs, with D3D11VA, which FFmpeg reaches through
	# d3d11.dll at run time, and the compiler's helpers linked in, so that
	# they import nothing but Windows' own DLLs and one another (checked
	# below). FFmpeg uses Windows' threads, not winpthreads.
	# The H.264 encoder is Cisco's OpenH264, built in the container above
	# and linked in from its static library. Media Foundation's (h264_mf)
	# is the fallback: FFmpeg reaches it through mfplat.dll at run time.
	# A cross build looks for $cross-pkg-config, which Ubuntu does not
	# install; --pkg-config names the pkg-config that finds OpenH264.
	# OpenH264 is C++, so the compiler's C++ runtime goes in with it,
	# statically, and the DLLs import nothing they did not before.
	cross=x86_64-w64-mingw32-
	flags="$flags
	--enable-cross-compile --target-os=mingw32 --arch=x86_64 --cross-prefix=$cross
	--pkg-config=pkg-config
	--enable-d3d11va
	--enable-hwaccel=h264_d3d11va,h264_d3d11va2,hevc_d3d11va,hevc_d3d11va2
	--enable-mediafoundation --enable-libopenh264 --enable-encoder=libopenh264,h264_mf
	--extra-ldflags=-static-libgcc --extra-ldflags=-static-libstdc++"
	need_nasm
	what="Windows x86_64"
	jobs=$(nproc)
	;;
linux-amd64)
	need_nasm
	what="Linux x86_64"
	jobs=$(nproc)
	;;
*)
	case $(uname -s) in
	Darwin)
		# Pinky.app runs on macOS 12 and later (minimumSystemVersion in
		# cmd/pinky/mygo.json). The libraries are found through @rpath,
		# which make app points at Contents/Frameworks; the header padding
		# lets install_name_tool rewrite their load commands.
		min=12.0
		flags="$flags
	--install-name-dir=@rpath
	--enable-videotoolbox
	--enable-hwaccel=h264_videotoolbox,hevc_videotoolbox
	--extra-cflags=-mmacosx-version-min=$min
	--extra-ldflags=-mmacosx-version-min=$min
	--extra-ldflags=-Wl,-headerpad_max_install_names"
		host=$(uname -m)
		case ${target:-$host} in
		arm64 | aarch64) arch=arm64 ;;
		amd64 | x86_64) arch=x86_64 ;;
		*)
			echo "ffmpeg.sh: no macOS build for the processor $target (arm64 or amd64)" >&2
			exit 2
			;;
		esac
		if [ "$arch" != "$host" ]; then
			# --target, one word, as the flags are split at spaces.
			flags="$flags
	--enable-cross-compile --target-os=darwin --arch=$arch --cc=clang
	--extra-cflags=--target=$arch-apple-macos$min
	--extra-ldflags=--target=$arch-apple-macos$min"
		fi
		if [ "$arch" = x86_64 ]; then
			need_nasm
		fi
		what=$arch
		jobs=$(sysctl -n hw.ncpu)
		;;
	*)
		what=$(uname -m)
		if [ -n "$target" ] && [ "$target" != "$what" ] && ! { [ "$target" = amd64 ] && [ "$what" = x86_64 ]; } && ! { [ "$target" = arm64 ] && [ "$what" = aarch64 ]; }; then
			echo "ffmpeg.sh: building for another processor ($target) is for macOS, Windows (windows-amd64) and Linux (linux-amd64) only" >&2
			exit 2
		fi
		jobs=$(nproc 2>/dev/null || echo 4)
		;;
	esac
	;;
esac

cd "$tree"
# The libraries record their configure line (avutil_configuration), so a
# fixed prefix keeps this machine's paths out of them; make install puts
# them under a staging folder, from where they move to $prefix.
stage=$src/stage
rm -rf "$stage"
# shellcheck disable=SC2086 # the flags are split on purpose
./configure --prefix=/opt/pinky-ffmpeg $flags
make -j"$jobs"
make install DESTDIR="$stage"
rm -rf "$prefix"
mv "$stage/opt/pinky-ffmpeg" "$prefix"
rm -rf "$stage"

case $target in
windows-amd64)
	# Every DLL may import Windows' own DLLs and FFmpeg's, and nothing else
	# (no libwinpthread-1.dll, no libgcc_s_seh-1.dll, no libstdc++-6.dll).
	imports=$("${cross}objdump" -p "$prefix"/bin/*.dll | sed -n 's/^[[:space:]]*DLL Name: //p' | sort -u)
	echo "The DLLs import: $(echo "$imports" | tr '\n' ' ')"
	bad=$(echo "$imports" | grep -v -i -E '^(avcodec|avformat|avutil|swscale)-[0-9]+\.dll$|^(kernel32|user32|gdi32|advapi32|ole32|oleaut32|shell32|shlwapi|bcrypt|msvcrt|ucrtbase|ws2_32|secur32|crypt32|mfplat|d3d11|dxgi)\.dll$|^api-ms-win-' || true)
	if [ -n "$bad" ]; then
		echo "ffmpeg.sh: the DLLs import libraries that Windows does not have:" >&2
		echo "$bad" >&2
		exit 1
	fi
	;;
linux-amd64)
	# The libraries find one another in their own folder, wherever it is,
	# need nothing but glibc and one another, and say which glibc.
	for f in "$prefix"/lib/lib*.so.*.*.*; do
		patchelf --set-rpath '$ORIGIN' "$f"
	done
	needed=$(for f in "$prefix"/lib/lib*.so.*.*.*; do readelf -d "$f"; done | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p' | sort -u)
	echo "The libraries need: $(echo "$needed" | tr '\n' ' ')"
	bad=$(echo "$needed" | grep -v -E '^lib(avcodec|avformat|avutil|swscale)\.so\.[0-9]+$|^(libc|libm|libpthread|libdl|librt)\.so\.[0-9]+$|^ld-linux-x86-64\.so\.2$' || true)
	if [ -n "$bad" ]; then
		echo "ffmpeg.sh: the libraries need libraries beyond glibc:" >&2
		echo "$bad" >&2
		exit 1
	fi
	glibc=$(objdump -T "$prefix"/lib/lib*.so.*.*.* | grep -o 'GLIBC_[0-9][0-9.]*' | sort -u -V | tail -n 1)
	what="$what, glibc ${glibc#GLIBC_} or later"
	;;
esac

cp COPYING.LGPLv2.1 "$prefix/COPYING.LGPLv2.1"
{
	echo "FFmpeg $VERSION, built for Pinky under the LGPL 2.1 (see COPYING.LGPLv2.1), for $what."
	echo "Source: $URL"
	echo "SHA-256: $SHA256"
	if [ "$target" = windows-amd64 ]; then
		echo "It holds OpenH264 $OPENH264_VERSION, the H.264 encoder, linked in from its static library:"
		echo "  Source: $OPENH264_URL"
		echo "  SHA-256: $OPENH264_SHA256"
		echo "  Licence: BSD-2-Clause. Cisco's patent licence covers Cisco's own binaries, not this build."
	fi
	echo "Configured with:"
	echo "$flags" | sed 's/^[[:space:]]*/  /'
} >"$prefix/BUILD.txt"
rm -rf "$tree"
echo "FFmpeg $VERSION is in $prefix"
