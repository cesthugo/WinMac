#!/usr/bin/env bash
# engine/build.sh — builds the Wine that CodeWeavers publishes with CrossOver
# into a cave engine (x86_64, WoW64; Apple silicon runs it through Rosetta 2).
#
# Adapted from conversio-wine's scripts/build.sh (MIT, (c) 2026 Theoron,
# https://github.com/theoron-apps/conversio-wine), see engine/NOTICE.
# Simplified for cave: no patches, no GStreamer (game videos that go through
# Media Foundation will not play yet).
#
# D3DMetal is NOT part of the build: Apple does not allow redistributing it.
# `cave engine import-d3dmetal` copies it from the user's own GPTK install.
#
# Usage: engine/build.sh deps|fetch|configure|build|install|addons|bundle|test-native|test|package|all
# Environment: CX_VER (default 26.3.0), REVISION (default 1), CX_SHA256, BUILD_DIR, JOBS.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
CX_VER="${CX_VER:-26.3.0}"
REVISION="${REVISION:-1}"
BUILD_DIR="${BUILD_DIR:-$REPO/build}"
DIST="${DIST:-$REPO/dist}"
JOBS="${JOBS:-$(sysctl -n hw.ncpu)}"

SRC_URL="https://media.codeweavers.com/pub/crossover/source/crossover-sources-${CX_VER}.tar.gz"
SRC_TGZ="$BUILD_DIR/crossover-sources-${CX_VER}.tar.gz"
WINE_SRC="$BUILD_DIR/wine-src"
WINE_BUILD="$BUILD_DIR/wine-build"
STAGE="$BUILD_DIR/stage"
WINE_ROOT="$STAGE/wine"
PACKAGE="cave-wine-cx${CX_VER}-${REVISION}-x86_64"

export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-11.0}"
export HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_INSTALL_CLEANUP=1 HOMEBREW_NO_ENV_HINTS=1

# Wine for macOS is an Intel program: on Apple silicon, build under Rosetta with Intel Homebrew.
if [ "$(uname -m)" = "arm64" ] && [ -z "${CAVE_UNDER_ROSETTA:-}" ]; then
  if [ ! -x /usr/local/bin/brew ]; then
    echo "On Apple silicon this build needs Intel Homebrew in /usr/local:"
    echo '  arch -x86_64 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'
    exit 1
  fi
  export CAVE_UNDER_ROSETTA=1
  exec arch -x86_64 /bin/bash "$0" "$@"
fi

BREW="${BREW:-$( [ -x /usr/local/bin/brew ] && echo /usr/local/bin/brew || command -v brew )}"
BREW_PREFIX="$("$BREW" --prefix)"

BUILD_DEPS="bison flex mingw-w64 pkgconf ccache"
RUNTIME_DEPS="freetype gnutls sdl2 sdl3 molten-vk"

log() { printf '\n==> %s\n' "$*"; }

# SHA-256 of CrossOver source tarballs we accept (same pin as conversio-wine).
pinned_sha256() {
  case "$1" in
    26.3.0) echo ac99c8ca4b3848f3e81784135f023df266b61c2345726ea55a50b3e030dd6872 ;;
  esac
}

use_tools() {
  export PATH="$("$BREW" --prefix bison)/bin:$("$BREW" --prefix flex)/bin:$BREW_PREFIX/bin:$PATH"
}

cmd_deps() {
  log "Installing Homebrew dependencies"
  # shellcheck disable=SC2086
  "$BREW" install $BUILD_DEPS $RUNTIME_DEPS
}

cmd_fetch() {
  log "Fetching CrossOver $CX_VER sources"
  mkdir -p "$BUILD_DIR"
  if [ ! -f "$SRC_TGZ" ]; then
    curl -fL --retry 3 --retry-delay 5 -o "$SRC_TGZ.part" "$SRC_URL"
    mv "$SRC_TGZ.part" "$SRC_TGZ"
  fi
  local expected actual
  expected="${CX_SHA256:-$(pinned_sha256 "$CX_VER")}"
  actual="$(shasum -a 256 "$SRC_TGZ" | cut -d ' ' -f 1)"
  if [ -z "$expected" ]; then
    echo "No checksum pinned for CrossOver $CX_VER. Its SHA-256 is: $actual"
    echo "Review the source, then pin it in engine/build.sh or pass CX_SHA256."
    exit 1
  fi
  [ "$actual" = "$expected" ] || { echo "Checksum mismatch: expected $expected, got $actual"; exit 1; }
  echo "Checksum OK"
  rm -rf "$WINE_SRC" "$BUILD_DIR/sources"
  tar -xzf "$SRC_TGZ" -C "$BUILD_DIR" sources/wine
  mv "$BUILD_DIR/sources/wine" "$WINE_SRC"
  rm -rf "$BUILD_DIR/sources"
  echo "Wine source: $(cat "$WINE_SRC/VERSION")"
}

cmd_configure() {
  log "Configuring Wine (x86_64 + i386 through WoW64)"
  use_tools
  rm -rf "$WINE_BUILD"; mkdir -p "$WINE_BUILD"; cd "$WINE_BUILD"
  export ac_cv_lib_soname_vulkan=""   # use MoltenVK directly, like CrossOver
  if command -v ccache >/dev/null 2>&1; then
    export x86_64_CC="ccache x86_64-w64-mingw32-gcc" i386_CC="ccache i686-w64-mingw32-gcc"
  fi
  CC="${CC:-clang}" CXX="${CXX:-clang++}" \
  CFLAGS="-O2 -g0 -Wno-deprecated-declarations -Wno-format -Wno-incompatible-function-pointer-types -Wno-int-conversion" \
  CROSSCFLAGS="-O2 -g0" \
  CPPFLAGS="-I$BREW_PREFIX/include" \
  LDFLAGS="-Wl,-headerpad_max_install_names -L$BREW_PREFIX/lib" \
  PKG_CONFIG_PATH="$BREW_PREFIX/lib/pkgconfig" \
  "$WINE_SRC/configure" \
    --prefix=/wine --enable-archs=i386,x86_64 --disable-tests \
    --with-coreaudio --with-freetype --with-gnutls --with-sdl --with-vulkan \
    --without-gstreamer --without-alsa --without-capi --without-cups --without-dbus \
    --without-gphoto --without-krb5 --without-oss --without-pcap --without-pulse \
    --without-sane --without-udev --without-usb --without-v4l2 --without-x
  local missing="" name
  for name in LIBFREETYPE LIBGNUTLS LIBSDL2 LIBMOLTENVK; do
    grep -q "#define SONAME_$name " include/config.h || missing="$missing $name"
  done
  [ -z "$missing" ] || { echo "Configure did not find:$missing"; exit 1; }
}

cmd_build()   { log "Building with $JOBS jobs"; use_tools; make -C "$WINE_BUILD" -j"$JOBS"; }

cmd_install() {
  log "Installing into $WINE_ROOT"
  use_tools
  rm -rf "$STAGE"
  make -C "$WINE_BUILD" install-lib DESTDIR="$STAGE"
  [ -x "$WINE_ROOT/bin/wine" ] || { echo "No wine binary in $WINE_ROOT/bin"; exit 1; }
}

# Wine Mono and Gecko in the versions this Wine expects, so it never asks to download them.
cmd_addons() {
  log "Adding Wine Mono and Wine Gecko"
  local addons="$WINE_SRC/dlls/appwiz.cpl/addons.c" mono gecko cache="$BUILD_DIR/addons"
  mono="$(sed -n 's/^#define MONO_VERSION "\(.*\)"/\1/p' "$addons")"
  gecko="$(sed -n 's/^#define GECKO_VERSION "\(.*\)"/\1/p' "$addons")"
  [ -n "$mono" ] && [ -n "$gecko" ] || { echo "Could not read add-on versions"; exit 1; }
  mkdir -p "$cache" "$WINE_ROOT/share/wine/mono" "$WINE_ROOT/share/wine/gecko"
  fetch_addon "https://dl.winehq.org/wine/wine-mono/$mono/wine-mono-$mono-x86.msi" "$WINE_ROOT/share/wine/mono"
  fetch_addon "https://dl.winehq.org/wine/wine-gecko/$gecko/wine-gecko-$gecko-x86.msi" "$WINE_ROOT/share/wine/gecko"
  fetch_addon "https://dl.winehq.org/wine/wine-gecko/$gecko/wine-gecko-$gecko-x86_64.msi" "$WINE_ROOT/share/wine/gecko"
}

fetch_addon() { # url target-dir
  local name cache="$BUILD_DIR/addons"
  name="$(basename "$1")"
  [ -f "$cache/$name" ] || curl -fL --retry 3 --retry-delay 5 -o "$cache/$name" "$1"
  cp "$cache/$name" "$2/$name"
}

# Copy a Homebrew dylib and its Homebrew dependencies into wine/lib, relinked with @loader_path.
bundle_dylib() {
  local source="$1" name target dependency dependency_name
  name="$(basename "$source")"; target="$WINE_ROOT/lib/$name"
  [ -f "$target" ] && return 0
  cp -L "$source" "$target"; chmod u+w "$target"
  install_name_tool -id "@loader_path/$name" "$target"
  while read -r dependency; do
    case "$dependency" in
      @rpath/*)
        dependency_name="$(basename "$dependency")"
        [ -f "$BREW_PREFIX/lib/$dependency_name" ] || { echo "Unresolved $dependency in $target"; exit 1; }
        bundle_dylib "$BREW_PREFIX/lib/$dependency_name"
        install_name_tool -change "$dependency" "@loader_path/$dependency_name" "$target" ;;
      "$BREW_PREFIX"/*|/usr/local/*|/opt/homebrew/*)
        dependency_name="$(basename "$dependency")"
        bundle_dylib "$dependency"
        install_name_tool -change "$dependency" "@loader_path/$dependency_name" "$target" ;;
    esac
  done < <(otool -L "$target" | tail -n +2 | awk '{print $1}')
}

cmd_bundle() {
  log "Bundling runtime libraries"
  mkdir -p "$WINE_ROOT/lib"
  local soname
  for soname in $(grep -E '^#define SONAME_' "$WINE_BUILD/include/config.h" | sed -E 's/.*"(.*)".*/\1/'); do
    if [ -f "$BREW_PREFIX/lib/$soname" ]; then bundle_dylib "$BREW_PREFIX/lib/$soname"; else echo "from macOS: $soname"; fi
  done
  # Homebrew's SDL2 is sdl2-compat and dlopens SDL3; without it winebus crashes.
  if [ -f "$WINE_ROOT/lib/libSDL2-2.0.0.dylib" ]; then
    local sdl3
    sdl3="$(ls "$BREW_PREFIX"/lib/libSDL3.0.dylib "$BREW_PREFIX"/lib/libSDL3.*.dylib 2>/dev/null | head -n 1)"
    [ -n "$sdl3" ] || { echo "SDL2 needs SDL3"; exit 1; }
    bundle_dylib "$sdl3"
    ln -sf "$(basename "$sdl3")" "$WINE_ROOT/lib/libSDL3.dylib"
  fi
  log "Signing ad hoc"
  local file
  local signed=0
  while IFS= read -r -d '' file; do
    # An `if`, not `test && sign`: with set -e, a final non-Mach-O file would
    # otherwise make the whole function fail silently.
    if file -b "$file" | grep -q 'Mach-O'; then
      codesign --force --sign - "$file" 2>/dev/null || echo "could not sign $file"
      signed=$((signed + 1))
    fi
  done < <(find "$WINE_ROOT" -type f -print0)
  echo "signed $signed Mach-O files"
}

# macOS has no coreutils `timeout`.
with_timeout() { # seconds cmd...
  python3 -c 'import subprocess, sys
try:
    sys.exit(subprocess.run(sys.argv[2:], timeout=int(sys.argv[1])).returncode)
except subprocess.TimeoutExpired:
    sys.exit(124)' "$@"
}

# Runs anywhere, including the Intel build runner.
cmd_test_native() {
  log "Native test"
  export DYLD_FALLBACK_LIBRARY_PATH="$WINE_ROOT/lib:/usr/lib"
  "$WINE_ROOT/bin/wine" --version
}

# Starts Windows inside Wine. On hosted Intel runners Wine hangs in explorer,
# so CI runs this on Apple silicon (see the smoke job).
cmd_test() {
  cmd_test_native
  log "Smoke test"
  local prefix="$BUILD_DIR/test-prefix" trace="$BUILD_DIR/smoke-test.log"
  rm -rf "$prefix"; mkdir -p "$prefix"
  # Hosted runners have no WindowServer session: use Wine's null display driver.
  cat > "$prefix/user.reg" <<'REG'
WINE REGISTRY Version 2

[Software\\Wine\\DllOverrides] 0
"winemac.drv"=""

[Software\\Wine\\Drivers] 0
"Graphics"="null"
REG
  if WINEPREFIX="$prefix" WINEDEBUG="err+all" \
     WINEDLLOVERRIDES='winemac.drv=d;winemenubuilder.exe=d;winedbg.exe=d' \
     with_timeout 300 "$WINE_ROOT/bin/wine" cmd /c echo cave-ok > "$trace" 2>&1 && grep -aq cave-ok "$trace"; then
    echo "Windows command line works"
  else
    echo "error: wine cmd did not finish"; tail -n 60 "$trace"; exit 1
  fi
  WINEPREFIX="$prefix" "$WINE_ROOT/bin/wineserver" -k 2>/dev/null || true
}

cmd_package() {
  log "Packaging $PACKAGE"
  mkdir -p "$DIST"
  cp "$WINE_SRC/COPYING.LIB" "$WINE_ROOT/COPYING.LIB"
  {
    echo "cave engine: Wine from CrossOver $CX_VER sources, build $REVISION"
    echo "Wine: $(cat "$WINE_SRC/VERSION")"
    echo "Source: $SRC_URL"
    echo "Source SHA-256: $(shasum -a 256 "$SRC_TGZ" | cut -d ' ' -f 1)"
    echo "Built: $(date -u +%Y-%m-%dT%H:%M:%SZ) on macOS $(sw_vers -productVersion) $(uname -m)"
    echo "License: LGPL-2.1-or-later (see COPYING.LIB). Sources are attached to the release."
    echo "D3DMetal: not included; import it with \`cave engine import-d3dmetal\`."
  } > "$WINE_ROOT/CAVE-ENGINE.txt"
  XZ_OPT="-T0 -6" tar -C "$STAGE" -cJf "$DIST/$PACKAGE.tar.xz" wine
  (cd "$DIST" && shasum -a 256 "$PACKAGE.tar.xz" > "$PACKAGE.tar.xz.sha256")
  ls -lh "$DIST"
}

case "${1:-all}" in
  test-native) cmd_test_native ;;
  deps|fetch|configure|build|install|addons|bundle|test|package) "cmd_$1" ;;
  all) cmd_deps; cmd_fetch; cmd_configure; cmd_build; cmd_install; cmd_addons; cmd_bundle; cmd_test; cmd_package ;;
  *) echo "usage: $0 [all|deps|fetch|configure|build|install|addons|bundle|test|package]"; exit 1 ;;
esac
