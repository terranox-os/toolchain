# SPDX-License-Identifier: Apache-2.0
#
# TerranoxOS Toolchain Container
#
# Packages the glibc-linked LLVM/Clang 21.1.8 toolchain with musl sysroot
# for building the TerranoxOS kernel and userspace on any Linux host.
#
# Build context must be the repository root so that toolchain/ is accessible.
#
# Build:
#   docker build -f containers/toolchain/Dockerfile \
#       -t ghcr.io/terranox-os/terranox-toolchain:21.1.8 .
#
# Usage:
#   docker run --rm -v $(pwd):/src -w /src/kernel \
#       ghcr.io/terranox-os/terranox-toolchain:21.1.8 just build
#

FROM ubuntu:24.04 AS base

# Install minimal runtime dependencies + build tools
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        # Runtime libs for glibc-linked clang
        libc6 libstdc++6 zlib1g \
        # Build tools
        make cmake ninja-build curl \
        # Utilities needed for kernel build
        xorriso mtools git \
        # QEMU for boot testing
        qemu-system-x86 \
        # Misc
        file ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    # Install just (not in Ubuntu repos)
    && curl -fsSL https://just.systems/install.sh | bash -s -- --to /usr/local/bin

# ── Copy pre-built toolchain ───────────────────────────────

# Copy the glibc-linked stage1 toolchain (clang, lld, llvm tools)
COPY toolchain/glibc-stage1/ /opt/terranox/tools/

# Copy the musl sysroot (headers + libs for cross-compilation)
COPY toolchain/glibc-sysroot/ /opt/terranox/sysroot/

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

# Default working directory
WORKDIR /src

# Default command: show toolchain info
CMD ["clang", "--version"]
