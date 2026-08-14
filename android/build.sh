#!/usr/bin/env bash
# Builds mediaplayer.apk (a WebView wrapper around the web client) straight
# from the SDK's own tools — aapt2, javac, d8, zipalign, apksigner. No Gradle,
# no Android Gradle Plugin, no network at build time: the whole app is one
# activity with no third-party dependencies, and a Gradle project would pull
# several hundred MB of plugin just to run the same four commands.
#
# Needs a JDK and an Android SDK with build-tools and a platform. Point these
# at your own if they live elsewhere:
#
#   JAVA_HOME=/path/to/jdk ANDROID_HOME=/path/to/sdk ./android/build.sh
#
# The signing key is a self-signed one kept outside the repo (see KEYSTORE
# below). Keep it: Android refuses to install an update signed by a different
# key, so losing it means uninstalling the app before the next install.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"

JAVA_HOME="${JAVA_HOME:-$HOME/.local/opt/jdk-17.0.20+8}"
ANDROID_HOME="${ANDROID_HOME:-$HOME/.local/share/android-sdk}"
BUILD_TOOLS_VERSION="${BUILD_TOOLS_VERSION:-34.0.0}"
PLATFORM="${PLATFORM:-android-34}"
KEYSTORE="${KEYSTORE:-$HOME/.config/mediaplayer-android.jks}"
KEY_ALIAS="${KEY_ALIAS:-mediaplayer}"
KEY_PASS="${KEY_PASS:-mediaplayer}"
OUT="${OUT:-$root/mediaplayer.apk}"

# d8, apksigner and zipalign are shell wrappers that exec a bare `java`, so the
# JDK has to be on PATH and not merely in JAVA_HOME.
export JAVA_HOME
export PATH="$JAVA_HOME/bin:$PATH"

bt="$ANDROID_HOME/build-tools/$BUILD_TOOLS_VERSION"
android_jar="$ANDROID_HOME/platforms/$PLATFORM/android.jar"
for f in "$JAVA_HOME/bin/javac" "$bt/aapt2" "$bt/d8" "$bt/zipalign" "$bt/apksigner" "$android_jar"; do
    [ -e "$f" ] || { echo "missing: $f" >&2; exit 1; }
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "==> resources"
mkdir -p "$work/compiled"
"$bt/aapt2" compile --dir "$here/res" -o "$work/compiled/res.zip"

echo "==> manifest + resource table"
# --java is deliberately not passed: nothing in the app references R, so there
# is no generated source to compile.
"$bt/aapt2" link \
    -o "$work/base.apk" \
    -I "$android_jar" \
    --manifest "$here/AndroidManifest.xml" \
    --min-sdk-version 26 \
    --target-sdk-version 34 \
    "$work/compiled/res.zip"

echo "==> java"
mkdir -p "$work/classes"
"$JAVA_HOME/bin/javac" \
    --release 11 \
    -Xlint:-options -nowarn \
    -classpath "$android_jar" \
    -d "$work/classes" \
    $(find "$here/src" -name '*.java')

echo "==> dex"
"$bt/d8" --release --min-api 26 --lib "$android_jar" --output "$work" \
    $(find "$work/classes" -name '*.class')

echo "==> package"
# classes.dex has to sit at the archive root. Either tool will do it; python3
# is the fallback because a minimal Arch install has no zip(1).
if command -v zip >/dev/null 2>&1; then
    (cd "$work" && zip -q base.apk classes.dex)
elif command -v python3 >/dev/null 2>&1; then
    python3 - "$work/base.apk" "$work/classes.dex" <<'PY'
import sys, zipfile
apk, dex = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(apk, "a", zipfile.ZIP_DEFLATED) as z:
    z.write(dex, "classes.dex")
PY
else
    echo "need either zip or python3 to add classes.dex to the apk" >&2
    exit 1
fi

echo "==> align + sign"
if [ ! -f "$KEYSTORE" ]; then
    echo "    generating $KEYSTORE (keep it — it identifies app updates)"
    mkdir -p "$(dirname "$KEYSTORE")"
    "$JAVA_HOME/bin/keytool" -genkeypair -v \
        -keystore "$KEYSTORE" -alias "$KEY_ALIAS" \
        -storepass "$KEY_PASS" -keypass "$KEY_PASS" \
        -keyalg RSA -keysize 2048 -validity 10000 \
        -dname "CN=mediaplayer, OU=self-signed, O=mediaplayer, C=CH" >/dev/null
fi
"$bt/zipalign" -f -p 4 "$work/base.apk" "$work/aligned.apk"
"$bt/apksigner" sign \
    --ks "$KEYSTORE" --ks-key-alias "$KEY_ALIAS" \
    --ks-pass "pass:$KEY_PASS" --key-pass "pass:$KEY_PASS" \
    --out "$OUT" "$work/aligned.apk"
"$bt/apksigner" verify --print-certs "$OUT" | head -2

echo "==> $OUT"
ls -lh "$OUT"
