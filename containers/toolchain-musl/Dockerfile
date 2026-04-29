# SPDX-License-Identifier: Apache-2.0
#
# TerranoxOS Toolchain Container (musl variant)
#
# Packages the musl-linked LLVM/Clang 21.1.8 toolchain with musl sysroot.
# This is the canonical toolchain — smaller image, musl-native, matches
# the production kernel build target (x86_64-linux-musl).
#
# Build context must be the repository root so that toolchain/ is accessible.
#
# Build:
#   docker build -f containers/toolchain-musl/Dockerfile \
#       -t ghcr.io/terranox-os/terranox-toolchain-musl:21.1.8 .
#
# Usage:
#   docker run --rm -v $(pwd):/src -v /path/to/kernel-libs:/kernel-libs:ro \
#       -w /src/kernel ghcr.io/terranox-os/terranox-toolchain-musl:21.1.8 just build
#

FROM alpine:3.21 AS base

# Install runtime dependencies + build tools
RUN apk add --no-cache \
        # Runtime libs for musl-linked clang
        musl libstdc++ zlib \
        # Build tools
        make cmake samurai curl \
        # Utilities needed for kernel build
        xorriso mtools git \
        # QEMU for boot testing
        qemu-system-x86_64 \
        # Misc
        file bash coreutils \
    # Install just
    && curl -fsSL https://just.systems/install.sh | bash -s -- --to /usr/local/bin

# ── Copy pre-built toolchain ───────────────────────────────

# Copy the musl-linked toolchain (clang, lld, llvm tools)
COPY toolchain/llvm/tools/ /opt/terranox/tools/

# Copy the musl sysroot (headers + libs for cross-compilation)
COPY toolchain/llvm/sysroot/ /opt/terranox/sysroot/

# Copy manifest
COPY toolchain/llvm/manifest.json /opt/terranox/manifest.json

# ── Fix known toolchain issues ──────────────────────────────
#
# These fixes address three issues discovered during world-build
# package testing (see terranox-tools test-musl-toolchain.sh):

# Fix 1: Create crtbeginT.o symlink (static CRT object).
# Clang's static linking mode looks for crtbeginT.o but the
# toolchain only ships crtbegin.o. For compiler-rt, they're equivalent.
RUN CLANG_MAJOR=$(clang --version | grep -oP 'clang version \K\d+') \
    && RT_DIR="/opt/terranox/tools/lib/clang/${CLANG_MAJOR}/lib/x86_64-unknown-linux-musl" \
    && [ -d "$RT_DIR" ] && ln -sf clang_rt.crtbegin-x86_64.o "$RT_DIR/crtbeginT.o" || true

# Fix 2: Copy libunwind.a to the clang resource directory so the
# linker finds it during static linking.
RUN CLANG_MAJOR=$(clang --version | grep -oP 'clang version \K\d+') \
    && RT_DIR="/opt/terranox/tools/lib/clang/${CLANG_MAJOR}/lib/x86_64-unknown-linux-musl" \
    && [ -f "/opt/terranox/sysroot/usr/lib/libunwind.a" ] \
    && cp /opt/terranox/sysroot/usr/lib/libunwind.a "$RT_DIR/" || true

# Fix 3: Strip ELFCOMPRESS_ZLIB debug sections from Alpine static libs.
# LLD in this toolchain is built without zlib support.
RUN find /opt/terranox/sysroot/usr/lib -name "*.a" \
    -exec /opt/terranox/tools/bin/llvm-strip --strip-debug {} \; 2>/dev/null || true

# Set up PATH and environment
ENV PATH="/opt/terranox/tools/bin:${PATH}" \
    CC="/opt/terranox/tools/bin/clang" \
    CXX="/opt/terranox/tools/bin/clang++" \
    LD="/opt/terranox/tools/bin/ld.lld" \
    AR="/opt/terranox/tools/bin/llvm-ar" \
    RANLIB="/opt/terranox/tools/bin/llvm-ranlib" \
    STRIP="/opt/terranox/tools/bin/llvm-strip" \
    OBJCOPY="/opt/terranox/tools/bin/llvm-objcopy" \
    TERRANOX_SYSROOT="/opt/terranox/sysroot" \
    TERRANOX_TOOLCHAIN="/opt/terranox/tools"

# Verify toolchain works
RUN clang --version && ld.lld --version

WORKDIR /src
CMD ["clang", "--version"]
