#!/bin/sh
# Builds Crash Test (dev.pinky.crashtest), the app the device tests in
# logs_device_test.go read the logs and crashes of. Its buttons log a line,
# throw an exception and block the main thread (an ANR).
#
# Usage: build.sh <debuggable: true|false> <out.apk>
#
# Needs the Android SDK (build-tools 36, platform 36) and a JDK; signs with
# a throwaway key made next to the APK.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
sdk=${ANDROID_HOME:-$HOME/Library/Android/sdk}
bt=$sdk/build-tools/36.0.0
jar=$sdk/platforms/android-36/android.jar
jdk=${JAVA_HOME:-/Applications/Android Studio.app/Contents/jbr/Contents/Home}/bin
out=$(cd "$(dirname "$2")" && pwd)/$(basename "$2")
ks=$(dirname "$out")/crashtest.keystore
w=$(mktemp -d)
trap 'rm -rf "$w"' EXIT
sed "s/DEBUGGABLE/$1/" "$here/AndroidManifest.xml" >"$w/AndroidManifest.xml"
"$bt/aapt2" link -o "$w/base.apk" --manifest "$w/AndroidManifest.xml" -I "$jar"
mkdir -p "$w/classes"
"$jdk/javac" --release 8 -classpath "$jar" -d "$w/classes" "$here/src/dev/pinky/crashtest/Main.java"
"$bt/d8" --min-api 29 --lib "$jar" --output "$w" $(find "$w/classes" -name '*.class')
(cd "$w" && zip -q base.apk classes.dex)
"$bt/zipalign" -f 4 "$w/base.apk" "$w/aligned.apk"
[ -f "$ks" ] || "$jdk/keytool" -genkeypair -keystore "$ks" -storepass android -keypass android \
	-alias test -keyalg RSA -validity 3650 -dname "CN=Pinky Test" >/dev/null 2>&1
"$bt/apksigner" sign --ks "$ks" --ks-pass pass:android --key-pass pass:android --out "$out" "$w/aligned.apk"
echo "built $out"
