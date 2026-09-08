#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MOBILE_VERSION=v0.0.0-20260821190718-4776eadac327
GRADLE_VERSION=8.11.1
GRADLE_SHA256=f397b287023acdba1e9f6fc5ea72d22dd63669d59ed4a289a29b1a76eee151c6
: "${ANDROID_HOME:?Set ANDROID_HOME to an Android SDK with platform 35 and build-tools 35.0.0}"
: "${ANDROID_NDK_HOME:?Set ANDROID_NDK_HOME to NDK 27.3.13750724}"
export PATH="$(go env GOPATH)/bin:$PATH"
cd "$ROOT/mobile"
go test -race ./core
go vet ./core
go install "golang.org/x/mobile/cmd/gomobile@$MOBILE_VERSION"
go install "golang.org/x/mobile/cmd/gobind@$MOBILE_VERSION"
gomobile init
mkdir -p android/app/libs build
gomobile bind -v -androidapi 26 -target=android/arm64,android/arm,android/amd64,android/386 \
  -javapkg=cn.yucg.bridge -ldflags='-s -w -extldflags=-Wl,-z,max-page-size=16384' \
  -o android/app/libs/yudesk-core.aar ./core
if [ ! -x "build/gradle-$GRADLE_VERSION/bin/gradle" ]; then
  if ! printf '%s  %s\n' "$GRADLE_SHA256" build/gradle.zip | sha256sum --check --strict --status; then
    curl --fail --location --connect-timeout 20 --max-time 300 --retry 3 --output build/gradle.zip "https://services.gradle.org/distributions/gradle-$GRADLE_VERSION-bin.zip"
  fi
  printf '%s  %s\n' "$GRADLE_SHA256" build/gradle.zip | sha256sum --check --strict
  unzip -q -o build/gradle.zip -d build
fi
"$ROOT/mobile/build/gradle-$GRADLE_VERSION/bin/gradle" -p android --no-daemon :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
APK_SOURCE=android/app/build/outputs/apk/debug/app-debug.apk
"$ANDROID_HOME/build-tools/35.0.0/apksigner" verify --verbose "$APK_SOURCE"
"$ANDROID_HOME/build-tools/35.0.0/zipalign" -c -P 16 -v 4 "$APK_SOURCE"
cp "$APK_SOURCE" build/YuDesk-2.0.0-android-preview.apk.next
mv build/YuDesk-2.0.0-android-preview.apk.next build/YuDesk-2.0.0-android-preview.apk
(cd build && sha256sum YuDesk-2.0.0-android-preview.apk > SHA256SUMS.txt)
