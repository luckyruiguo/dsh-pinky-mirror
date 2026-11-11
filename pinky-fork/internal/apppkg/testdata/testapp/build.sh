#!/bin/sh
# Build the tiny test app phone_app's tests install, with the Android SDK's
# own tools and no Gradle: aapt2, javac, d8, zipalign, apksigner.
#
# Usage: build.sh <out dir> <version code> <version name> [label]
#
# Writes <out>/pinky-test-<code>.apk (signed v1, v2 and v3), which is also
# the base of a split pair with <out>/pinky-test-<code>-de.apk (the German
# strings), and <out>/pinky-test-<code>-v1.apk (v1 signing only), all signed
# with <out>/test.keystore, which it makes when it is missing. A label other
# than "Pinky Test" makes the app's name that.
set -eu
out="$1"
code="$2"
name="$3"
label="${4:-Pinky Test}"
sdk="${ANDROID_HOME:-$HOME/Library/Android/sdk}"
bt="$sdk/build-tools/$(ls "$sdk/build-tools" | sort -V | tail -1)"
jar="$sdk/platforms/$(ls "$sdk/platforms" | grep -E '^android-[0-9]+$' | sort -t- -k2 -n | tail -1)/android.jar"
here="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

ks="$out/test.keystore"
if [ ! -f "$ks" ]; then
	keytool -genkeypair -keystore "$ks" -storepass pinkytest -keypass pinkytest \
		-alias test -keyalg RSA -keysize 2048 -validity 36500 \
		-dname "CN=Pinky Test, O=Pinky" >/dev/null 2>&1
fi

cp -R "$here/res" "$work/res"
sed "s/>Pinky Test</>$label</" "$here/res/values/strings.xml" >"$work/res/values/strings.xml"
"$bt/aapt2" compile --dir "$work/res" -o "$work/res.zip"
"$bt/aapt2" link -I "$jar" --manifest "$here/AndroidManifest.xml" \
	--version-code "$code" --version-name "$name" \
	--java "$work/gen" -o "$work/base.apk" \
	--split "$work/de.apk:de" "$work/res.zip"
mkdir -p "$work/classes"
javac -source 8 -target 8 -nowarn -bootclasspath "$jar" -d "$work/classes" \
	"$work/gen/app/pinky/testapp/R.java" "$here/src/app/pinky/testapp/MainActivity.java" 2>/dev/null
"$bt/d8" --lib "$jar" --min-api 24 --output "$work" $(find "$work/classes" -name '*.class')
(cd "$work" && zip -q base.apk classes.dex)

signv() { # in out flags...
	in="$1"
	dst="$2"
	shift 2
	"$bt/zipalign" -f 4 "$in" "$work/aligned.apk"
	"$bt/apksigner" sign --ks "$ks" --ks-pass pass:pinkytest --ks-key-alias test \
		"$@" --out "$dst" "$work/aligned.apk" 2>&1 | grep -v -e '^WARNING:' -e '^$' >&2 || true
	[ -f "$dst" ]
	rm -f "$dst.idsig"
}
signv "$work/base.apk" "$out/pinky-test-$code.apk"
signv "$work/base.apk" "$out/pinky-test-$code-v1.apk" --min-sdk-version 24 \
	--v1-signing-enabled true --v2-signing-enabled false --v3-signing-enabled false --v4-signing-enabled false
signv "$work/de.apk" "$out/pinky-test-$code-de.apk"
echo "built $out/pinky-test-$code*.apk"
