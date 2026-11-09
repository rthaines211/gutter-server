#!/bin/sh
# Xcode build phase: builds gutter-server (repo root) as a universal CGO binary,
# puts it in Contents/MacOS and signs it. HELPER_ENTITLEMENTS is set per
# configuration: sandbox + inherit for the App Store build, empty for Developer ID.
set -eu
export PATH="/opt/homebrew/bin:/usr/local/go/bin:/usr/local/bin:$PATH"
cd "$SRCROOT/.."
OUT="$DERIVED_FILE_DIR/helper"; mkdir -p "$OUT"
MIN="-mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"
for arch in arm64 amd64; do
	carch=$arch; [ $arch = amd64 ] && carch=x86_64
	CGO_ENABLED=1 GOOS=darwin GOARCH=$arch CC="clang -arch $carch" \
		CGO_CFLAGS="-O2 $MIN" CGO_LDFLAGS="$MIN" \
		go build -trimpath -o "$OUT/gutter-server-$arch" .
done
DEST="$TARGET_BUILD_DIR/$EXECUTABLE_FOLDER_PATH/gutter-server"
lipo -create "$OUT/gutter-server-arm64" "$OUT/gutter-server-amd64" -output "$DEST"
set -- -f -s "${EXPANDED_CODE_SIGN_IDENTITY:--}" -o runtime --timestamp=none
[ -n "${HELPER_ENTITLEMENTS:-}" ] && set -- "$@" --entitlements "$HELPER_ENTITLEMENTS"
codesign "$@" "$DEST"
