#!/bin/sh
# Rebuild the bundled LGPL-2.0-or-later LAME 3.100 reactor without Docker.
# Download wasi-sdk 34 from https://github.com/WebAssembly/wasi-sdk/releases/tag/wasi-sdk-34
# Set WASI_SDK_PATH to its extracted directory. No host FFmpeg/LAME is used.
set -eu
: "${WASI_SDK_PATH:?set WASI_SDK_PATH to wasi-sdk 34}"
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
build=$(mktemp -d)
trap 'rm -rf "$build"' EXIT
python3 - "$here/lame-3.100.tar.gz" <<'PY'
import hashlib,sys
assert hashlib.sha256(open(sys.argv[1],'rb').read()).hexdigest() == 'ddfe36cab873794038ae2c1210557ad34857a4b6bdc515785d1da9e175b1da1e'
PY
tar -xzf "$here/lame-3.100.tar.gz" -C "$build"
cd "$build/lame-3.100"
# Old config.sub predates WASI. arm-linux is only its cross-compilation sentinel;
# wasi-sdk clang always emits wasm32-wasip1. No architecture assembly is enabled.
CC="$WASI_SDK_PATH/bin/clang" AR="$WASI_SDK_PATH/bin/llvm-ar" RANLIB="$WASI_SDK_PATH/bin/llvm-ranlib" CFLAGS='-O2 -DNDEBUG' ./configure --host=arm-linux --disable-shared --disable-frontend --disable-decoder --disable-nasm --enable-static
make -C libmp3lame -j4 CFLAGS='-O2 -DNDEBUG -D_WASI_EMULATED_SIGNAL'
"$WASI_SDK_PATH/bin/clang" -O2 -mexec-model=reactor -Iinclude "$here/encoder.c" libmp3lame/.libs/libmp3lame.a -lm -lwasi-emulated-signal -Wl,--export=start,--export=input_ptr,--export=output_ptr,--export=encode,--export=finish,--export=tag,--max-memory=33554432 -o "$build/encoder.wasm"
gzip -n -c "$build/encoder.wasm" > "$here/encoder.wasm.gz"
