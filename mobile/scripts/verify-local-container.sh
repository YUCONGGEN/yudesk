#!/usr/bin/env bash
# Resume inside the existing Linux build container. Copy source into native
# container storage so Windows bind-mount JAR and directory I/O cannot stall it.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
: "${ANDROID_HOME:?Android SDK required}"
: "${ANDROID_NDK_HOME:?Android NDK required}"
export PATH="$(go env GOPATH)/bin:$PATH"
STAGE="$(mktemp -d /tmp/yudesk-android-final.XXXXXX)"
printf 'Local verification workspace: %s\n' "$STAGE"
mkdir -p "$STAGE/internal" "$STAGE/mobile/android/app/libs" "$STAGE/mobile/build"
cp "$ROOT/go.mod" "$ROOT/go.sum" "$STAGE/"
# gomobile's temporary module tidy also follows dependency test imports.
for package in approval identity relay protocol secureconn security stream; do
  cp -a "$ROOT/internal/$package" "$STAGE/internal/"
done
cp -a "$ROOT/mobile/core" "$STAGE/mobile/"
cp "$ROOT/mobile/go.mod" "$ROOT/mobile/go.sum" "$ROOT/mobile/tools.go" "$STAGE/mobile/"
cp "$ROOT/mobile/android/settings.gradle" "$ROOT/mobile/android/build.gradle" "$ROOT/mobile/android/gradle.properties" "$STAGE/mobile/android/"
cp "$ROOT/mobile/android/app/build.gradle" "$STAGE/mobile/android/app/"
cp -a "$ROOT/mobile/android/app/src" "$STAGE/mobile/android/app/"
GRADLE_ROOT=/opt/yudesk-gradle
if [ ! -x "$GRADLE_ROOT/gradle-8.11.1/bin/gradle" ]; then
  printf '%s  %s\n' f397b287023acdba1e9f6fc5ea72d22dd63669d59ed4a289a29b1a76eee151c6 "$ROOT/mobile/build/gradle.zip" | sha256sum --check --strict
  mkdir -p "$GRADLE_ROOT"
  unzip -q -o "$ROOT/mobile/build/gradle.zip" -d "$GRADLE_ROOT"
fi
cd "$STAGE/mobile"
go test -race -count=3 ./core
go vet ./core
gomobile bind -androidapi 26 -target=android/arm64,android/arm,android/amd64,android/386 \
  -javapkg=cn.yucg.bridge -ldflags='-s -w -extldflags=-Wl,-z,max-page-size=16384' \
  -o android/app/libs/yudesk-core.aar ./core
"$GRADLE_ROOT/gradle-8.11.1/bin/gradle" -p android --no-daemon --offline :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
APK=android/app/build/outputs/apk/debug/app-debug.apk
"$ANDROID_HOME/build-tools/35.0.0/apksigner" verify --verbose --print-certs "$APK" | tee build/apksigner.txt
"$ANDROID_HOME/build-tools/35.0.0/zipalign" -c -P 16 -v 4 "$APK" > build/zipalign.txt
tail -n 2 build/zipalign.txt
mkdir -p "$ROOT/mobile/build/verification"
cp -a android/app/build/reports "$ROOT/mobile/build/verification/"
cp build/apksigner.txt build/zipalign.txt "$ROOT/mobile/build/verification/"
cp android/app/libs/yudesk-core.aar "$ROOT/mobile/android/app/libs/yudesk-core.aar.next"
mv "$ROOT/mobile/android/app/libs/yudesk-core.aar.next" "$ROOT/mobile/android/app/libs/yudesk-core.aar"
cp "$APK" "$ROOT/mobile/build/YuDesk-2.0.0-android-preview.apk.next"
mv "$ROOT/mobile/build/YuDesk-2.0.0-android-preview.apk.next" "$ROOT/mobile/build/YuDesk-2.0.0-android-preview.apk"
cd "$ROOT/mobile/build"
sha256sum YuDesk-2.0.0-android-preview.apk | tee SHA256SUMS.txt
printf 'Verified artifact: %s/mobile/build/YuDesk-2.0.0-android-preview.apk\n' "$ROOT"
