#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
#
# Build a static musl hello-world for TerranoxOS userspace testing.
#
# Prerequisites: glibc-stage1 toolchain + musl sysroot from Dagger bootstrap
#
# Usage (from repo root):
#   ./containers/toolchain-musl/build-musl-hello.sh
#
# Output: build/hello_musl.elf (statically linked, ~4 KB stripped)

set -e

CLANG="toolchain/glibc-stage1/bin/clang"
LLD="toolchain/glibc-stage1/bin/ld.lld"
SYSROOT="toolchain/llvm/sysroot"
STRIP="llvm-strip"
OUTDIR="build"

if [ ! -x "$CLANG" ]; then
    echo "Error: $CLANG not found. Run Dagger bootstrap first."
    exit 1
fi

mkdir -p "$OUTDIR"

# Compile
$CLANG --target=x86_64-linux-musl \
    -nostdinc \
    -isystem "$SYSROOT/usr/include" \
    -c -o "$OUTDIR/hello_musl.o" /dev/stdin << 'CEOF'
#include <unistd.h>

int main(void) {
    const char msg[] = "[musl] Hello from TerranoxOS userspace!\n";
    write(1, msg, sizeof(msg) - 1);
    return 0;
}
CEOF

# Link statically with musl
$LLD -static \
    -o "$OUTDIR/hello_musl.elf" \
    "$SYSROOT/usr/lib/crt1.o" \
    "$SYSROOT/usr/lib/crti.o" \
    "$OUTDIR/hello_musl.o" \
    "$SYSROOT/usr/lib/libc.a" \
    "$SYSROOT/usr/lib/linux/libclang_rt.builtins-x86_64.a" \
    "$SYSROOT/usr/lib/crtn.o"

# Strip
$STRIP "$OUTDIR/hello_musl.elf" -o "$OUTDIR/hello_musl_stripped.elf"

echo "Built: $OUTDIR/hello_musl.elf ($(du -h "$OUTDIR/hello_musl.elf" | cut -f1))"
echo "Stripped: $OUTDIR/hello_musl_stripped.elf ($(du -h "$OUTDIR/hello_musl_stripped.elf" | cut -f1))"
echo ""
echo "This binary uses Linux x86_64 syscall numbers."
echo "TerranoxOS translates them via the Linux compat dispatch layer."
