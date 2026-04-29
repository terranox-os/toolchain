<!--
SPDX-License-Identifier: CC-BY-4.0

doc-id: TRX-DOC-0204
title: Clang Linux From Scratch Guide
status: implemented
version: 1.0.0
date: 2026-03-21
author: Antonette Caldwell
scope: toolchain
milestone: v0.1.0
-->

<!--
SPDX-License-Identifier: CC-BY-4.0
-->

# Clang Linux From Scratch (CLFS)
## Building a Complete Linux System with a Pure LLVM/Clang + musl Toolchain

**Version:** 0.1.0-draft
**Target:** x86_64-linux-musl
**LLVM Version:** 19.x / 20.x (adjust as needed)
**Author:** Antonette Caldwell — TerranoxOS Project

---

## Table of Contents

1. [Philosophy & Goals](#1-philosophy--goals)
2. [Host Requirements](#2-host-requirements)
3. [Validating Your Compiler](#3-validating-your-compiler)
4. [Directory Layout](#4-directory-layout)
5. [Stage 0: Seed Compiler](#5-stage-0-seed-compiler)
6. [Stage 1: Cross-Compile musl](#6-stage-1-cross-compile-musl)
7. [Stage 2: Build LLVM Runtimes Against musl](#7-stage-2-build-llvm-runtimes-against-musl)
8. [Stage 3: Self-Hosted Clang](#8-stage-3-self-hosted-clang)
9. [Stage 4: Verified Rebuild](#9-stage-4-verified-rebuild)
10. [Building the Userspace](#10-building-the-userspace)
11. [Making It Bootable](#11-making-it-bootable)
12. [Troubleshooting](#12-troubleshooting)

---

## 1. Philosophy & Goals

This guide builds a complete, bootable Linux system where:

- **Clang** is the C/C++ compiler (no GCC in the final system)
- **lld** is the linker (no GNU ld / binutils ld)
- **llvm-ar, llvm-nm, llvm-objdump, llvm-objcopy, llvm-strip,
  llvm-ranlib, llvm-readelf, llvm-strings** replace binutils
- **compiler-rt** replaces libgcc
- **libunwind** (LLVM) replaces GCC's unwinder
- **libc++** / **libc++abi** replace libstdc++
- **musl** replaces glibc
- **Zero GNU runtime dependencies** in the final system

GCC may appear as a *bootstrap seed* in Stage 0 if you don't already
have a working Clang. It is discarded after Stage 1.

### What this guide does NOT cover

- Building the Linux kernel (use `make CC=clang LD=ld.lld` — well supported upstream)
- Package management (intentionally omitted, build your own)
- Desktop environments / X11 / Wayland
- Networking beyond basic iproute2

---

## 2. Host Requirements

You need a working Linux system to build on. Any modern distro works.

### Required host packages

```bash
# Debian/Ubuntu
sudo apt install -y \
    build-essential cmake ninja-build python3 python3-pip \
    git wget curl xz-utils bzip2 tar \
    libssl-dev zlib1g-dev libncurses-dev \
    qemu-system-x86 grub-pc-bin xorriso mtools

# Fedora
sudo dnf install -y \
    gcc gcc-c++ cmake ninja-build python3 \
    git wget curl xz bzip2 tar \
    openssl-devel zlib-devel ncurses-devel \
    qemu-system-x86 grub2-tools-extra xorriso mtools
```

### Disk space

- Source downloads: ~2 GB
- Build artifacts: ~15-25 GB (LLVM builds are large)
- Final system: ~500 MB - 1 GB

### Time estimate

Full bootstrap from zero: 2-6 hours depending on CPU cores.
Most time is spent building LLVM (3x: stage0, stage1, stage2).

---

## 3. Validating Your Compiler

> **Note:** These tests are shown as bash for readability.
> The actual implementation uses Dagger Go functions
> (see TERRANOX_BOOTSTRAP_DAGGER.md). Run them with:
> `dagger call validate` (all 8) or `dagger call test-basic` (one).

**DO THIS FIRST.** Before building anything, verify your compiler
actually works. Each test below checks a specific capability.
If any test fails, your toolchain has a problem that will cascade
into every package you try to build.

### 3.1 Basic Compiler Check

```bash
#!/usr/bin/env bash
# test_01_basic.sh — Can the compiler produce an executable?

set -euo pipefail

CLANG="${CLANG:-clang}"
echo "=== Test 01: Basic Compilation ==="
echo "Using: $(${CLANG} --version | head -1)"

cat > /tmp/test_basic.c << 'EOF'
#include <stdio.h>

int main(void) {
    printf("Compiler works: %s %s\n", __clang_version__, __DATE__);
    return 0;
}
EOF

# Compile and run
${CLANG} -o /tmp/test_basic /tmp/test_basic.c
/tmp/test_basic

echo "PASS: Basic compilation works"
rm -f /tmp/test_basic /tmp/test_basic.c
```

### 3.2 Target Triple Check

```bash
#!/usr/bin/env bash
# test_02_target.sh — Is the compiler targeting the right triple?

set -euo pipefail

CLANG="${CLANG:-clang}"
echo "=== Test 02: Target Triple ==="

# What triple does the compiler default to?
DEFAULT_TRIPLE=$(${CLANG} -dumpmachine)
echo "Default target triple: ${DEFAULT_TRIPLE}"

# What targets are supported?
echo "Supported targets:"
${CLANG} --print-targets 2>&1 | head -20

# Can it target x86_64-linux-musl?
echo ""
echo "Testing x86_64-linux-musl target..."
cat > /tmp/test_triple.c << 'EOF'
int main(void) { return 0; }
EOF

# This should produce an object file without errors
${CLANG} --target=x86_64-linux-musl -c /tmp/test_triple.c \
    -o /tmp/test_triple.o 2>&1 && \
    echo "PASS: Can target x86_64-linux-musl" || \
    echo "FAIL: Cannot target x86_64-linux-musl"

# Check the object file format
file /tmp/test_triple.o
readelf -h /tmp/test_triple.o | grep -E "Class|Machine|OS/ABI"

rm -f /tmp/test_triple.c /tmp/test_triple.o
```

### 3.3 Linker Check

```bash
#!/usr/bin/env bash
# test_03_linker.sh — Does lld work? Can it produce executables?

set -euo pipefail

CLANG="${CLANG:-clang}"
echo "=== Test 03: Linker (lld) ==="

# Check which linker clang uses by default
echo "Default linker:"
${CLANG} -### -fuse-ld=lld /dev/null -o /dev/null 2>&1 | grep -oP '"[^"]*lld[^"]*"' | head -1

# Check lld is available
if command -v ld.lld &>/dev/null; then
    echo "lld version: $(ld.lld --version)"
elif command -v "${CLANG%clang}ld.lld" &>/dev/null; then
    echo "lld version: $("${CLANG%clang}ld.lld" --version)"
else
    echo "FAIL: ld.lld not found in PATH"
    exit 1
fi

# Test static linking
cat > /tmp/test_link.c << 'EOF'
#include <stdio.h>
int main(void) {
    puts("Static link works");
    return 0;
}
EOF

# Dynamic link
${CLANG} -fuse-ld=lld -o /tmp/test_link_dyn /tmp/test_link.c && \
    echo "PASS: Dynamic linking with lld" || \
    echo "FAIL: Dynamic linking with lld"

# Static link
${CLANG} -fuse-ld=lld -static -o /tmp/test_link_static /tmp/test_link.c 2>/dev/null && \
    echo "PASS: Static linking with lld" || \
    echo "WARN: Static linking failed (may need static libc)"

file /tmp/test_link_dyn 2>/dev/null
ldd /tmp/test_link_dyn 2>/dev/null || true

rm -f /tmp/test_link.c /tmp/test_link_dyn /tmp/test_link_static
```

### 3.4 LLVM Tools Check

```bash
#!/usr/bin/env bash
# test_04_tools.sh — Are all required LLVM tools available?

set -euo pipefail

echo "=== Test 04: LLVM Tools ==="

TOOLS=(
    "clang:C compiler"
    "clang++:C++ compiler"
    "ld.lld:Linker"
    "llvm-ar:Archiver (replaces ar)"
    "llvm-nm:Symbol table (replaces nm)"
    "llvm-objdump:Disassembler (replaces objdump)"
    "llvm-objcopy:Object copy (replaces objcopy)"
    "llvm-strip:Strip symbols (replaces strip)"
    "llvm-ranlib:Archive index (replaces ranlib)"
    "llvm-readelf:ELF reader (replaces readelf)"
    "llvm-strings:String finder (replaces strings)"
    "llvm-config:LLVM configuration"
    "llvm-size:Section sizes (replaces size)"
)

PASS=0
FAIL=0

for entry in "${TOOLS[@]}"; do
    tool="${entry%%:*}"
    desc="${entry#*:}"
    if command -v "${tool}" &>/dev/null; then
        version=$(${tool} --version 2>&1 | head -1)
        printf "  %-20s %-35s %s\n" "${tool}" "${desc}" "OK"
        PASS=$((PASS + 1))
    else
        printf "  %-20s %-35s %s\n" "${tool}" "${desc}" "MISSING"
        FAIL=$((FAIL + 1))
    fi
done

echo ""
echo "Results: ${PASS} found, ${FAIL} missing"

if [ ${FAIL} -gt 0 ]; then
    echo "WARN: Some tools are missing. The build may still work"
    echo "      if they are available under different names or paths."
fi
```

### 3.5 compiler-rt Check

```bash
#!/usr/bin/env bash
# test_05_compiler_rt.sh — Is compiler-rt available and working?

set -euo pipefail

CLANG="${CLANG:-clang}"
echo "=== Test 05: compiler-rt ==="

# Where does clang look for compiler-rt?
RESOURCE_DIR=$(${CLANG} --print-resource-dir)
echo "Resource directory: ${RESOURCE_DIR}"

# List what's in the runtime library directory
echo ""
echo "Runtime libraries found:"
if [ -d "${RESOURCE_DIR}/lib" ]; then
    find "${RESOURCE_DIR}/lib" -name "*.a" -o -name "*.so" 2>/dev/null | \
        while read -r lib; do
            echo "  $(basename "$lib")"
        done
else
    echo "  WARNING: No lib/ directory in resource dir"
fi

# Test if compiler-rt builtins work
cat > /tmp/test_crt.c << 'EOF'
// This uses __divdi3, which comes from compiler-rt builtins
#include <stdio.h>

long long divide(long long a, long long b) {
    return a / b;
}

int main(void) {
    printf("100 / 7 = %lld\n", divide(100, 7));
    return 0;
}
EOF

# Try with --rtlib=compiler-rt
${CLANG} --rtlib=compiler-rt -o /tmp/test_crt /tmp/test_crt.c 2>&1 && {
    /tmp/test_crt
    echo "PASS: compiler-rt builtins work"
} || {
    echo "FAIL: compiler-rt builtins not working"
    echo "      Trying with default rtlib..."
    ${CLANG} -o /tmp/test_crt /tmp/test_crt.c && {
        /tmp/test_crt
        echo "PASS: default rtlib works (likely libgcc)"
    } || echo "FAIL: no working runtime library"
}

rm -f /tmp/test_crt /tmp/test_crt.c
```

### 3.6 C++ / libc++ Check

```bash
#!/usr/bin/env bash
# test_06_libcxx.sh — Does libc++ work?

set -euo pipefail

CLANGXX="${CLANGXX:-clang++}"
echo "=== Test 06: libc++ ==="

cat > /tmp/test_cxx.cpp << 'EOF'
#include <iostream>
#include <string>
#include <vector>
#include <algorithm>
#include <memory>

int main() {
    // Test basic I/O
    std::cout << "C++ standard: " << __cplusplus << std::endl;

    // Test containers
    std::vector<std::string> v = {"clang", "lld", "musl", "libc++"};
    std::sort(v.begin(), v.end());

    std::cout << "Sorted toolchain: ";
    for (const auto& s : v) std::cout << s << " ";
    std::cout << std::endl;

    // Test smart pointers
    auto ptr = std::make_unique<int>(42);
    std::cout << "unique_ptr: " << *ptr << std::endl;

    return 0;
}
EOF

# Try with libc++ explicitly
${CLANGXX} -stdlib=libc++ -o /tmp/test_cxx /tmp/test_cxx.cpp 2>&1 && {
    /tmp/test_cxx
    echo "PASS: libc++ works"
} || {
    echo "WARN: libc++ not available, trying libstdc++..."
    ${CLANGXX} -stdlib=libstdc++ -o /tmp/test_cxx /tmp/test_cxx.cpp && {
        /tmp/test_cxx
        echo "PASS: libstdc++ works (not ideal for pure LLVM build)"
    } || echo "FAIL: no working C++ standard library"
}

rm -f /tmp/test_cxx /tmp/test_cxx.cpp
```

### 3.7 Cross-Compilation Check

```bash
#!/usr/bin/env bash
# test_07_cross.sh — Can we cross-compile for a freestanding target?

set -euo pipefail

CLANG="${CLANG:-clang}"
echo "=== Test 07: Cross-Compilation / Freestanding ==="

cat > /tmp/test_freestanding.c << 'EOF'
// Freestanding — no libc, no OS, just raw code
// This is what kernel code looks like to the compiler

typedef unsigned long uint64_t;

void _start(void) {
    // Write to VGA text buffer (0xB8000)
    volatile char *vga = (volatile char *)0xB8000;
    const char *msg = "Hello from freestanding";

    for (int i = 0; msg[i]; i++) {
        vga[i * 2] = msg[i];
        vga[i * 2 + 1] = 0x0F;  // White on black
    }

    // Halt
    while (1) {
        __asm__ volatile ("hlt");
    }
}
EOF

# Compile for freestanding x86_64
${CLANG} \
    --target=x86_64-elf \
    -ffreestanding \
    -fno-builtin \
    -nostdlib \
    -nostdinc \
    -mno-red-zone \
    -mno-sse \
    -mno-sse2 \
    -mcmodel=kernel \
    -c /tmp/test_freestanding.c \
    -o /tmp/test_freestanding.o 2>&1 && {
    echo "PASS: Freestanding compilation works"
    file /tmp/test_freestanding.o
    llvm-objdump -d /tmp/test_freestanding.o | head -30
} || {
    echo "FAIL: Freestanding compilation broken"
}

# Test freestanding + lld linking
cat > /tmp/test_link.ld << 'EOF'
ENTRY(_start)
SECTIONS {
    . = 0x100000;
    .text : { *(.text) }
    .rodata : { *(.rodata) }
    .data : { *(.data) }
    .bss : { *(.bss) }
}
EOF

ld.lld \
    -T /tmp/test_link.ld \
    -nostdlib \
    -static \
    /tmp/test_freestanding.o \
    -o /tmp/test_freestanding.elf 2>&1 && {
    echo "PASS: Freestanding linking with lld works"
    file /tmp/test_freestanding.elf
    llvm-size /tmp/test_freestanding.elf
} || {
    echo "FAIL: Freestanding linking broken"
}

rm -f /tmp/test_freestanding.* /tmp/test_link.ld
```

### 3.8 musl Sysroot Check

```bash
#!/usr/bin/env bash
# test_08_musl.sh — Can we compile and link against musl?

set -euo pipefail

CLANG="${CLANG:-clang}"
MUSL_PREFIX="${MUSL_PREFIX:-/usr/local/musl}"

echo "=== Test 08: musl Integration ==="
echo "musl prefix: ${MUSL_PREFIX}"

# Check musl is installed
if [ ! -f "${MUSL_PREFIX}/lib/libc.a" ]; then
    echo "FAIL: musl not found at ${MUSL_PREFIX}"
    echo "      Set MUSL_PREFIX to your musl install directory"
    echo "      Expected: ${MUSL_PREFIX}/lib/libc.a"
    exit 1
fi

echo "musl libc.a: $(ls -lh ${MUSL_PREFIX}/lib/libc.a)"
echo "musl headers: $(ls ${MUSL_PREFIX}/include/stdio.h 2>/dev/null && echo 'found' || echo 'MISSING')"
echo "musl crt1.o:  $(ls ${MUSL_PREFIX}/lib/crt1.o 2>/dev/null && echo 'found' || echo 'MISSING')"
echo "musl crti.o:  $(ls ${MUSL_PREFIX}/lib/crti.o 2>/dev/null && echo 'found' || echo 'MISSING')"
echo "musl crtn.o:  $(ls ${MUSL_PREFIX}/lib/crtn.o 2>/dev/null && echo 'found' || echo 'MISSING')"

# Dynamic linker
echo "musl dynlinker: $(ls ${MUSL_PREFIX}/lib/ld-musl-*.so* 2>/dev/null || echo 'MISSING')"

cat > /tmp/test_musl.c << 'EOF'
#include <stdio.h>
#include <string.h>
#include <stdlib.h>

int main(void) {
    char buf[64];
    snprintf(buf, sizeof(buf), "musl works: strlen test = %zu", strlen("hello"));
    puts(buf);

    // Test malloc/free
    char *p = malloc(1024);
    if (p) {
        memset(p, 'A', 1024);
        free(p);
        puts("malloc/free: OK");
    }

    return 0;
}
EOF

# Compile against musl
${CLANG} \
    --target=x86_64-linux-musl \
    --sysroot="${MUSL_PREFIX}" \
    -isystem "${MUSL_PREFIX}/include" \
    -L"${MUSL_PREFIX}/lib" \
    -fuse-ld=lld \
    -static \
    -o /tmp/test_musl \
    /tmp/test_musl.c 2>&1 && {
    echo ""
    file /tmp/test_musl
    /tmp/test_musl
    echo "PASS: Static musl compilation works"
} || {
    echo "FAIL: Cannot compile against musl"
    echo ""
    echo "Debugging: trying with verbose output..."
    ${CLANG} \
        --target=x86_64-linux-musl \
        --sysroot="${MUSL_PREFIX}" \
        -isystem "${MUSL_PREFIX}/include" \
        -L"${MUSL_PREFIX}/lib" \
        -fuse-ld=lld \
        -static \
        -v \
        -o /tmp/test_musl \
        /tmp/test_musl.c 2>&1
}

rm -f /tmp/test_musl /tmp/test_musl.c
```

### 3.9 Run All Tests

```bash
#!/usr/bin/env bash
# test_all.sh — Run all compiler validation tests

set -euo pipefail

echo "╔══════════════════════════════════════════╗"
echo "║  CLFS Compiler Validation Suite          ║"
echo "╚══════════════════════════════════════════╝"
echo ""

# Set these to your compiler paths
export CLANG="${CLANG:-clang}"
export CLANGXX="${CLANGXX:-clang++}"
export MUSL_PREFIX="${MUSL_PREFIX:-/usr/local/musl}"

PASS=0
FAIL=0

run_test() {
    local name="$1"
    local script="$2"
    echo ""
    echo "────────────────────────────────────────"
    if bash "$script" 2>&1; then
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
        echo "^^^ FAILURE in: ${name} ^^^"
    fi
}

run_test "Basic Compilation" test_01_basic.sh
run_test "Target Triple" test_02_target.sh
run_test "Linker (lld)" test_03_linker.sh
run_test "LLVM Tools" test_04_tools.sh
run_test "compiler-rt" test_05_compiler_rt.sh
run_test "libc++ (C++)" test_06_libcxx.sh
run_test "Cross/Freestanding" test_07_cross.sh
run_test "musl Integration" test_08_musl.sh

echo ""
echo "════════════════════════════════════════════"
echo "Results: ${PASS} passed, ${FAIL} failed"
echo "════════════════════════════════════════════"

if [ ${FAIL} -gt 0 ]; then
    echo ""
    echo "FIX THE FAILURES ABOVE BEFORE PROCEEDING."
    echo "Each failure will cascade into build errors later."
    exit 1
else
    echo ""
    echo "All tests passed. Your toolchain is ready."
fi
```

---

## 4. Directory Layout

```bash
# Set the build root — everything lives under here
export CLFS=/mnt/clfs
export CLFS_SOURCES=${CLFS}/sources
export CLFS_TOOLS=${CLFS}/tools         # Temporary toolchain
export CLFS_CROSS=${CLFS}/cross-tools   # Cross-compiler
export CLFS_SYSROOT=${CLFS}/sysroot     # Target sysroot

export CLFS_TARGET="x86_64-linux-musl"
export CLFS_HOST="$(clang -dumpmachine)"
export CLFS_ARCH="x86_64"

# Create directory structure
sudo mkdir -pv ${CLFS}
sudo mkdir -pv ${CLFS_SOURCES}
sudo mkdir -pv ${CLFS_TOOLS}
sudo mkdir -pv ${CLFS_CROSS}
sudo mkdir -pv ${CLFS_SYSROOT}/{usr/{lib,include,bin,sbin},lib,etc,var,dev,proc,sys,tmp,run}

# Symlinks for compatibility
sudo ln -sv usr/lib  ${CLFS_SYSROOT}/lib64
sudo ln -sv usr/bin  ${CLFS_SYSROOT}/bin
sudo ln -sv usr/sbin ${CLFS_SYSROOT}/sbin

# Create the build user (avoids building as root)
sudo groupadd -f clfs
sudo useradd -s /bin/bash -g clfs -m -k /dev/null clfs 2>/dev/null || true
sudo chown -Rv clfs:clfs ${CLFS}

# Switch to build user
sudo su - clfs
```

### Environment Setup (~/.bashrc for clfs user)

```bash
cat > ~/.bashrc << 'BASHEOF'
set +h                           # Disable hash (force PATH lookup)
umask 022
export CLFS=/mnt/clfs
export CLFS_SOURCES=${CLFS}/sources
export CLFS_TOOLS=${CLFS}/tools
export CLFS_CROSS=${CLFS}/cross-tools
export CLFS_SYSROOT=${CLFS}/sysroot
export CLFS_TARGET="x86_64-linux-musl"
export CLFS_ARCH="x86_64"
export LC_ALL=POSIX

# The toolchain path — our built tools take priority
export PATH="${CLFS_CROSS}/bin:${CLFS_TOOLS}/bin:${PATH}"

# Compiler defaults for cross-compilation
export CC="${CLFS_CROSS}/bin/clang --target=${CLFS_TARGET} --sysroot=${CLFS_SYSROOT}"
export CXX="${CLFS_CROSS}/bin/clang++ --target=${CLFS_TARGET} --sysroot=${CLFS_SYSROOT}"
export AR="llvm-ar"
export AS="llvm-as"
export NM="llvm-nm"
export RANLIB="llvm-ranlib"
export STRIP="llvm-strip"
export OBJDUMP="llvm-objdump"
export OBJCOPY="llvm-objcopy"
export READELF="llvm-readelf"
export SIZE="llvm-size"
export STRINGS="llvm-strings"
export LD="ld.lld"

BASHEOF

source ~/.bashrc
```

---

## 5. Stage 0: Seed Compiler

The seed is any working C/C++ compiler. If you have a system
Clang already, use it. If not, use GCC — it gets thrown away
after this stage.

### Option A: System Clang as seed (preferred)

```bash
# If your distro has clang installed
which clang && echo "Using system clang as seed"
```

### Option B: Download prebuilt Clang

```bash
cd ${CLFS_SOURCES}

LLVM_VER="19.1.0"
wget "https://github.com/llvm/llvm-project/releases/download/llvmorg-${LLVM_VER}/clang+llvm-${LLVM_VER}-x86_64-linux-gnu-ubuntu-22.04.tar.xz"
tar xf clang+llvm-${LLVM_VER}-*.tar.xz
export SEED="${CLFS_SOURCES}/clang+llvm-${LLVM_VER}-x86_64-linux-gnu-ubuntu-22.04"
export PATH="${SEED}/bin:${PATH}"
```

### Option C: System GCC as seed (last resort)

```bash
# GCC works as a seed but will be completely replaced
which gcc && echo "Using system gcc as seed (will be replaced)"
```

### Download Sources

```bash
cd ${CLFS_SOURCES}

# LLVM monorepo (contains clang, lld, compiler-rt, libc++, etc.)
LLVM_VER="19.1.7"  # Use latest stable
wget "https://github.com/llvm/llvm-project/releases/download/llvmorg-${LLVM_VER}/llvm-project-${LLVM_VER}.src.tar.xz"
tar xf llvm-project-${LLVM_VER}.src.tar.xz
mv llvm-project-${LLVM_VER}.src llvm-project

# musl
MUSL_VER="1.2.5"
wget "https://musl.libc.org/releases/musl-${MUSL_VER}.tar.gz"
tar xf musl-${MUSL_VER}.tar.gz

# Linux kernel headers (needed by musl)
LINUX_VER="6.12.8"
wget "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${LINUX_VER}.tar.xz"
tar xf linux-${LINUX_VER}.tar.xz
```

---

## 6. Stage 1: Cross-Compile musl

Before building Clang, we need musl headers and a minimal libc
in the sysroot. This is the chicken-and-egg: Clang needs libc
headers to build runtimes, and a full libc needs a compiler.
The solution is the same as LFS: install headers first, build
a minimal compiler, then build the full libc.

### 6.1 Install Linux Kernel Headers

```bash
cd ${CLFS_SOURCES}/linux-${LINUX_VER}

make mrproper

make ARCH=${CLFS_ARCH} \
     INSTALL_HDR_PATH=${CLFS_SYSROOT}/usr \
     headers_install

# Verify
ls ${CLFS_SYSROOT}/usr/include/linux/types.h && \
    echo "PASS: Kernel headers installed"
```

### 6.2 Install musl Headers

```bash
cd ${CLFS_SOURCES}/musl-${MUSL_VER}

# Install just the headers (no compilation yet)
make ARCH=${CLFS_ARCH} \
     prefix=/usr \
     DESTDIR=${CLFS_SYSROOT} \
     install-headers

# Verify
ls ${CLFS_SYSROOT}/usr/include/stdio.h && \
    echo "PASS: musl headers installed"
```

### 6.3 Build Stage 0 Clang (Cross-Compiler)

This builds a minimal Clang that can target x86_64-linux-musl.
It doesn't need to be perfect — it just needs to compile musl
and the LLVM runtimes.

```bash
cd ${CLFS_SOURCES}
mkdir -p build-stage0 && cd build-stage0

cmake -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX="${CLFS_CROSS}" \
    \
    -DLLVM_ENABLE_PROJECTS="clang;lld" \
    -DLLVM_ENABLE_RUNTIMES="" \
    -DLLVM_TARGETS_TO_BUILD="X86" \
    -DLLVM_DEFAULT_TARGET_TRIPLE="${CLFS_TARGET}" \
    \
    -DCLANG_DEFAULT_LINKER="lld" \
    -DCLANG_DEFAULT_CXX_STDLIB="libc++" \
    -DCLANG_DEFAULT_RTLIB="compiler-rt" \
    -DCLANG_DEFAULT_UNWINDLIB="libunwind" \
    \
    -DLLVM_INSTALL_TOOLCHAIN_ONLY=ON \
    -DLLVM_INCLUDE_TESTS=OFF \
    -DLLVM_INCLUDE_EXAMPLES=OFF \
    -DLLVM_INCLUDE_BENCHMARKS=OFF \
    -DLLVM_INCLUDE_DOCS=OFF \
    \
    ../llvm-project/llvm

ninja -j$(nproc)
ninja install

# Create symlinks for binutils-compatible names
cd ${CLFS_CROSS}/bin
for tool in ar nm objdump objcopy strip ranlib readelf strings size; do
    ln -sf llvm-${tool} ${CLFS_TARGET}-${tool} 2>/dev/null || true
done
ln -sf clang ${CLFS_TARGET}-cc
ln -sf clang++ ${CLFS_TARGET}-c++
ln -sf ld.lld ${CLFS_TARGET}-ld
ln -sf lld ld

# Verify
${CLFS_CROSS}/bin/clang --version
echo "PASS: Stage 0 Clang installed"
```

### 6.4 Build musl (Full)

```bash
cd ${CLFS_SOURCES}/musl-${MUSL_VER}
make distclean

# Configure musl to be built by our stage0 clang
CC="${CLFS_CROSS}/bin/clang --target=${CLFS_TARGET}" \
AR="${CLFS_CROSS}/bin/llvm-ar" \
RANLIB="${CLFS_CROSS}/bin/llvm-ranlib" \
LIBCC="-lclang_rt.builtins" \
./configure \
    --prefix=/usr \
    --target=${CLFS_TARGET} \
    --disable-wrapper

make -j$(nproc)
make DESTDIR=${CLFS_SYSROOT} install

# Create the dynamic linker symlink
mkdir -p ${CLFS_SYSROOT}/lib
ln -sf /usr/lib/libc.so ${CLFS_SYSROOT}/lib/ld-musl-x86_64.so.1

# Verify
ls -la ${CLFS_SYSROOT}/usr/lib/libc.a
ls -la ${CLFS_SYSROOT}/usr/lib/libc.so
ls -la ${CLFS_SYSROOT}/usr/lib/crt1.o
echo "PASS: musl installed in sysroot"
```

---

## 7. Stage 2: Build LLVM Runtimes Against musl

Now we build the LLVM runtime libraries (compiler-rt, libunwind,
libc++abi, libc++) against our musl sysroot. These replace all
GCC runtime dependencies.

```bash
cd ${CLFS_SOURCES}
mkdir -p build-runtimes && cd build-runtimes

cmake -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX="${CLFS_SYSROOT}/usr" \
    \
    -DCMAKE_C_COMPILER="${CLFS_CROSS}/bin/clang" \
    -DCMAKE_CXX_COMPILER="${CLFS_CROSS}/bin/clang++" \
    -DCMAKE_C_COMPILER_TARGET="${CLFS_TARGET}" \
    -DCMAKE_CXX_COMPILER_TARGET="${CLFS_TARGET}" \
    -DCMAKE_ASM_COMPILER_TARGET="${CLFS_TARGET}" \
    -DCMAKE_SYSROOT="${CLFS_SYSROOT}" \
    \
    -DLLVM_ENABLE_RUNTIMES="compiler-rt;libunwind;libcxxabi;libcxx" \
    \
    -DCOMPILER_RT_BUILD_BUILTINS=ON \
    -DCOMPILER_RT_BUILD_SANITIZERS=OFF \
    -DCOMPILER_RT_BUILD_XRAY=OFF \
    -DCOMPILER_RT_BUILD_LIBFUZZER=OFF \
    -DCOMPILER_RT_BUILD_PROFILE=OFF \
    -DCOMPILER_RT_BUILD_MEMPROF=OFF \
    -DCOMPILER_RT_BUILD_ORC=OFF \
    -DCOMPILER_RT_DEFAULT_TARGET_ONLY=ON \
    \
    -DLIBUNWIND_USE_COMPILER_RT=ON \
    -DLIBUNWIND_ENABLE_STATIC=ON \
    -DLIBUNWIND_ENABLE_SHARED=ON \
    \
    -DLIBCXXABI_USE_COMPILER_RT=ON \
    -DLIBCXXABI_USE_LLVM_UNWINDER=ON \
    -DLIBCXXABI_ENABLE_STATIC=ON \
    \
    -DLIBCXX_USE_COMPILER_RT=ON \
    -DLIBCXX_HAS_MUSL_LIBC=ON \
    -DLIBCXX_ENABLE_STATIC=ON \
    -DLIBCXX_CXX_ABI=libcxxabi \
    \
    ../llvm-project/runtimes

ninja -j$(nproc)
ninja install

# Verify runtime libraries exist in sysroot
echo ""
echo "Installed runtime libraries:"
ls -la ${CLFS_SYSROOT}/usr/lib/libunwind* 2>/dev/null
ls -la ${CLFS_SYSROOT}/usr/lib/libc++* 2>/dev/null
ls -la ${CLFS_SYSROOT}/usr/lib/libclang_rt* 2>/dev/null || \
    ls -la ${CLFS_SYSROOT}/usr/lib/linux/libclang_rt* 2>/dev/null

echo "PASS: LLVM runtimes installed in sysroot"
```

---

## 8. Stage 3: Self-Hosted Clang

Now we rebuild Clang itself using the Stage 0 Clang, but this
time linked against our musl sysroot and LLVM runtimes. The
result is a Clang that has ZERO GCC dependencies.

```bash
cd ${CLFS_SOURCES}
mkdir -p build-stage1 && cd build-stage1

cmake -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX="${CLFS_TOOLS}" \
    \
    -DCMAKE_C_COMPILER="${CLFS_CROSS}/bin/clang" \
    -DCMAKE_CXX_COMPILER="${CLFS_CROSS}/bin/clang++" \
    -DCMAKE_C_FLAGS="--target=${CLFS_TARGET} --sysroot=${CLFS_SYSROOT}" \
    -DCMAKE_CXX_FLAGS="--target=${CLFS_TARGET} --sysroot=${CLFS_SYSROOT} -stdlib=libc++" \
    \
    -DLLVM_ENABLE_PROJECTS="clang;lld" \
    -DLLVM_ENABLE_RUNTIMES="" \
    -DLLVM_TARGETS_TO_BUILD="X86" \
    -DLLVM_DEFAULT_TARGET_TRIPLE="${CLFS_TARGET}" \
    -DLLVM_HOST_TRIPLE="${CLFS_TARGET}" \
    \
    -DCLANG_DEFAULT_LINKER="lld" \
    -DCLANG_DEFAULT_CXX_STDLIB="libc++" \
    -DCLANG_DEFAULT_RTLIB="compiler-rt" \
    -DCLANG_DEFAULT_UNWINDLIB="libunwind" \
    \
    -DDEFAULT_SYSROOT="${CLFS_SYSROOT}" \
    \
    -DLLVM_INSTALL_TOOLCHAIN_ONLY=ON \
    -DLLVM_INCLUDE_TESTS=OFF \
    -DLLVM_INCLUDE_EXAMPLES=OFF \
    -DLLVM_INCLUDE_BENCHMARKS=OFF \
    \
    ../llvm-project/llvm

ninja -j$(nproc)
ninja install

# Verify: this clang should link against musl, not glibc
echo ""
echo "Stage 1 Clang dependencies:"
ldd ${CLFS_TOOLS}/bin/clang 2>/dev/null || echo "(statically linked)"
file ${CLFS_TOOLS}/bin/clang

# The output should show ld-musl-x86_64.so.1, NOT ld-linux-x86-64.so.2

echo "PASS: Self-hosted Clang built"
```

---

## 9. Stage 4: Verified Rebuild (Optional but Recommended)

Rebuild Clang one more time using the Stage 1 Clang. Compare
outputs to verify reproducibility.

```bash
cd ${CLFS_SOURCES}
mkdir -p build-stage2 && cd build-stage2

# Same cmake config as Stage 1, but using Stage 1 as the compiler
cmake -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX="${CLFS_TOOLS}-stage2" \
    \
    -DCMAKE_C_COMPILER="${CLFS_TOOLS}/bin/clang" \
    -DCMAKE_CXX_COMPILER="${CLFS_TOOLS}/bin/clang++" \
    \
    # ... (same flags as Stage 1) ...
    ../llvm-project/llvm

ninja -j$(nproc)

# Compare: stage1 and stage2 should produce identical binaries
# (or very close — timestamps may differ)
diff <(llvm-objdump -d ${CLFS_TOOLS}/bin/clang) \
     <(llvm-objdump -d ${CLFS_TOOLS}-stage2/bin/clang) && \
    echo "PASS: Stage 1 and Stage 2 are identical (reproducible build!)" || \
    echo "WARN: Stage 1 and Stage 2 differ (check timestamps, debug info)"
```

---

## 14. Building the Userspace

With a working, self-hosted Clang + musl toolchain, we can
now build userspace packages. This is the LFS equivalent of
Chapter 8 — building the final system.

### Build order (essential packages)

```
1.  Linux kernel headers (already installed)
2.  musl (already installed)
3.  LLVM runtimes (already installed)
4.  zlib-ng (compression, needed by many packages)
5.  toybox or busybox (coreutils + shell in one binary)
6.  make (GNU make, needed to build other packages)
7.  bash (interactive shell)
8.  coreutils (full GNU coreutils if desired)
9.  findutils
10. grep
11. sed
12. gawk
13. diffutils
14. patch
15. tar
16. xz
17. file
18. ncurses (terminal handling)
19. readline (line editing)
20. util-linux (mount, fdisk, etc.)
21. e2fsprogs (ext4 filesystem tools)
22. kmod (kernel module tools)
23. eudev or mdevd (device manager)
24. iproute2 (networking)
25. Linux kernel (built with CC=clang)
26. GRUB or bootloader of choice
```

### Example: Building zlib-ng

```bash
cd ${CLFS_SOURCES}
wget https://github.com/zlib-ng/zlib-ng/archive/refs/tags/2.2.3.tar.gz
tar xf 2.2.3.tar.gz && cd zlib-ng-2.2.3

cmake -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX=/usr \
    -DCMAKE_C_COMPILER="${CLFS_TOOLS}/bin/clang" \
    -DCMAKE_SYSROOT="${CLFS_SYSROOT}" \
    -DZLIB_COMPAT=ON \
    -B build

ninja -C build -j$(nproc)
DESTDIR=${CLFS_SYSROOT} ninja -C build install
```

### Example: Building toybox (replaces coreutils + busybox)

```bash
cd ${CLFS_SOURCES}
wget https://landley.net/toybox/downloads/toybox-0.8.11.tar.gz
tar xf toybox-0.8.11.tar.gz && cd toybox-0.8.11

# toybox builds with a simple make
make defconfig
make \
    CC="${CLFS_TOOLS}/bin/clang --sysroot=${CLFS_SYSROOT}" \
    LDFLAGS="-static -fuse-ld=lld" \
    -j$(nproc)

# Install
make PREFIX=${CLFS_SYSROOT} install
```

### Example: Building the Linux kernel with Clang

```bash
cd ${CLFS_SOURCES}/linux-${LINUX_VER}

make mrproper
make defconfig

make \
    ARCH=x86_64 \
    CC="${CLFS_TOOLS}/bin/clang" \
    LD="ld.lld" \
    AR="llvm-ar" \
    NM="llvm-nm" \
    STRIP="llvm-strip" \
    OBJCOPY="llvm-objcopy" \
    OBJDUMP="llvm-objdump" \
    READELF="llvm-readelf" \
    HOSTCC="clang" \
    HOSTCXX="clang++" \
    HOSTLD="ld.lld" \
    HOSTAR="llvm-ar" \
    LLVM=1 \
    -j$(nproc)

# Install
make INSTALL_PATH=${CLFS_SYSROOT}/boot install
make INSTALL_MOD_PATH=${CLFS_SYSROOT} modules_install
```

---

## 15. Making It Bootable

### Create init script

```bash
cat > ${CLFS_SYSROOT}/init << 'EOF'
#!/bin/sh

# Mount essential filesystems
mount -t proc proc /proc
mount -t sysfs sys /sys
mount -t devtmpfs dev /dev

# Create device nodes if devtmpfs isn't available
[ -c /dev/null ] || mknod -m 666 /dev/null c 1 3
[ -c /dev/console ] || mknod -m 600 /dev/console c 5 1

# Set hostname
hostname clfs

echo ""
echo "╔══════════════════════════════════════╗"
echo "║  Clang Linux From Scratch            ║"
echo "║  Pure LLVM/Clang + musl toolchain    ║"
echo "╚══════════════════════════════════════╝"
echo ""

# Drop to shell
exec /bin/sh
EOF

chmod +x ${CLFS_SYSROOT}/init
```

### Create bootable ISO

```bash
cd ${CLFS}

# Create initramfs
cd ${CLFS_SYSROOT}
find . | cpio -o -H newc | gzip > ${CLFS}/initramfs.gz

# Create ISO with GRUB
mkdir -p ${CLFS}/iso/boot/grub
cp ${CLFS_SYSROOT}/boot/vmlinuz* ${CLFS}/iso/boot/vmlinuz
cp ${CLFS}/initramfs.gz ${CLFS}/iso/boot/initramfs.gz

cat > ${CLFS}/iso/boot/grub/grub.cfg << 'EOF'
set timeout=5
set default=0

menuentry "Clang Linux From Scratch" {
    linux /boot/vmlinuz root=/dev/ram0 rdinit=/init
    initrd /boot/initramfs.gz
}
EOF

grub-mkrescue -o ${CLFS}/clfs.iso ${CLFS}/iso

echo ""
echo "Bootable ISO: ${CLFS}/clfs.iso"
echo "Test: qemu-system-x86_64 -cdrom ${CLFS}/clfs.iso -m 512M"
```

---

## 16. Troubleshooting

### "cannot find crtbegin.o"

Clang is looking for GCC's crt files. Fix by ensuring compiler-rt
is installed and visible:

```bash
# Check where clang looks for runtime files
clang --print-resource-dir
clang --print-file-name=crtbegin.o

# If it returns empty, compiler-rt builtins aren't installed properly
# Rebuild compiler-rt with -DCOMPILER_RT_BUILD_CRT=ON
```

### "undefined reference to __gcc_personality_v0"

You're linking against something that expects GCC's exception
handling. Fix: ensure libunwind is in the sysroot and add
`-lunwind` to link flags, or rebuild the offending library
with `-DCMAKE_C_COMPILER_WORKS=ON`.

### "cannot find -lgcc_s"

A package is explicitly linking against libgcc_s. Create a
stub that redirects to compiler-rt:

```bash
# In your sysroot's lib directory
echo 'INPUT(-lclang_rt.builtins -lunwind)' > ${CLFS_SYSROOT}/usr/lib/libgcc_s.so
```

### Autoconf "checking whether the C compiler works... no"

Autoconf scripts often fail with Clang when cross-compiling
because they try to run the compiled binary. Fix:

```bash
# For autoconf-based packages, always pass:
./configure \
    --host=${CLFS_TARGET} \
    --build=${CLFS_HOST} \
    CC="${CLFS_TOOLS}/bin/clang --sysroot=${CLFS_SYSROOT}" \
    LD="ld.lld" \
    AR="llvm-ar" \
    RANLIB="llvm-ranlib"
```

### "error: unknown target triple 'x86_64-linux-musl'"

Your Clang wasn't built with musl support. Check:

```bash
clang --print-supported-cpus  # Should list x86-64 variants
clang -target x86_64-linux-musl -E -x c /dev/null
# Should succeed without "unknown triple" error
```

### Package uses GNU-specific extensions

Some packages won't compile with Clang. Options:

1. Check Chimera Linux patches: https://github.com/chimera-linux
2. Add `-Wno-error` to disable warnings-as-errors
3. Use `--rtlib=compiler-rt --unwindlib=libunwind` explicitly
4. For truly GCC-only packages, build GCC as a secondary compiler
   (install to a separate prefix, only use when necessary)

---

## 10. The x86_64-pc-terranox Triple

LLVM doesn't know about TerranoxOS. You need to teach it.
A target triple tells the compiler what OS, ABI, and environment
the generated code targets. The triple `x86_64-pc-terranox`
means: x86_64 CPU, PC-class machine, TerranoxOS.

### What the triple controls

When Clang sees `--target=x86_64-pc-terranox`, it decides:
- Which system headers to search for
- Which linker defaults to use
- Which `#define` macros to set (`__terranox__` instead of `__linux__`)
- Which C runtime files (crt1.o, crti.o, crtn.o) to link
- Which dynamic linker path to embed in executables

### Files to patch (5 files in llvm-project)

Maintain these as a quilt patch series or git diff.
Apply after fetching LLVM source, before building Stage 1.

#### File 1: llvm/include/llvm/TargetParser/Triple.h

Add TerranoxOS to the OS enum:

```cpp
// In the OS enum, after the last entry before LastOSType:
    Terranox,     // TerranoxOS
```

#### File 2: llvm/lib/TargetParser/Triple.cpp

Teach the parser to recognize "terranox":

```cpp
// In getOSTypeName():
  case Terranox: return "terranox";

// In parseOS():
  .StartsWith("terranox", Triple::Terranox)
```

#### File 3: clang/lib/Basic/Targets.cpp

Route Terranox to the Linux-compatible target info.
Initially, TerranoxOS is ABI-compatible with Linux/musl —
same syscall numbers, same ELF format. You diverge later.

```cpp
// In AllocateTarget(), inside the switch on OS:
  case llvm::Triple::Terranox:
    switch (Triple.getArch()) {
    case llvm::Triple::x86_64:
      return std::make_unique<X86_64TargetInfo>(Triple, Opts);
    default:
      return nullptr;
    }
```

#### File 4: clang/lib/Basic/Targets/OSTargets.h

Define the `__terranox__` macro and Terranox-specific defaults:

```cpp
// Add a new class after LinuxTargetInfo:
template <typename Target>
class LLVM_LIBRARY_VISIBILITY TerranoxTargetInfo : public OSTargetInfo<Target> {
protected:
  void getOSDefines(const LangOptions &Opts, const llvm::Triple &Triple,
                    MacroBuilder &Builder) const override {
    // TerranoxOS identification
    Builder.defineMacro("__terranox__");
    Builder.defineMacro("__Terranox__");
    Builder.defineMacro("__TERRANOX__");

    // POSIX-like (initially Linux-compatible)
    Builder.defineMacro("__unix__");
    Builder.defineMacro("__ELF__");

    // NOT Linux — this is the critical distinction
    // Do NOT define __linux__ or __gnu_linux__
  }

public:
  TerranoxTargetInfo(const llvm::Triple &Triple, const TargetOptions &Opts)
      : OSTargetInfo<Target>(Triple, Opts) {
    // Use musl dynamic linker path
    this->MCountName = "__mcount";
  }
};
```

#### File 5: clang/lib/Driver/ToolChains/Terranox.h + Terranox.cpp

Create a toolchain driver (tells Clang how to invoke lld,
where to find headers, CRT files, etc.):

```cpp
// Terranox.h
class LLVM_LIBRARY_VISIBILITY Terranox : public Generic_ELF {
public:
  Terranox(const Driver &D, const llvm::Triple &Triple,
           const llvm::opt::ArgList &Args);

  bool HasNativeLLVMSupport() const override { return true; }
  bool isPIEDefault(const llvm::opt::ArgList &Args) const override { return true; }
  bool isNoExecStackDefault() const override { return true; }

  // Use our paths
  void AddClangSystemIncludeArgs(const llvm::opt::ArgList &DriverArgs,
                                 llvm::opt::ArgStringList &CC1Args) const override;

  std::string getDynamicLinker(const llvm::opt::ArgList &Args) const override {
    return "/lib/ld-musl-x86_64.so.1";
  }

  // Default to lld, compiler-rt, libc++, libunwind
  RuntimeLibType GetDefaultRuntimeLibType() const override {
    return ToolChain::RLT_CompilerRT;
  }
  CXXStdlibType GetDefaultCXXStdlibType() const override {
    return ToolChain::CST_Libcxx;
  }
  UnwindLibType GetDefaultUnwindLibType() const override {
    return ToolChain::UNW_Libunwind;
  }
  const char *getDefaultLinker() const override { return "lld"; }
};
```

### Testing the triple

After rebuilding with the patch:

```bash
# Verify the triple is recognized
clang --target=x86_64-pc-terranox -v 2>&1 | head -5
# Should show: Target: x86_64-pc-terranox

# Verify __terranox__ is defined
echo '__terranox__' | clang --target=x86_64-pc-terranox -E -x c - 2>/dev/null
# Should output: 1 (not __terranox__)

# Verify __linux__ is NOT defined
echo '__linux__' | clang --target=x86_64-pc-terranox -E -x c - 2>/dev/null
# Should output: __linux__ (unexpanded, meaning undefined)

# Compile and link a test program
cat > /tmp/test.c << 'EOF'
#ifdef __terranox__
const char *os = "TerranoxOS";
#else
#error "Not targeting Terranox"
#endif
int main(void) { return 0; }
EOF
clang --target=x86_64-pc-terranox --sysroot=/path/to/sysroot \
    -o /tmp/test /tmp/test.c
```

### Patch maintenance

As LLVM versions change, these patches may need rebasing.
Strategy:
- Keep patches as `patches/llvm-terranox-triple.patch`
- Dagger pipeline applies patches in `FetchLLVM()` before building
- CI tests the patches against every LLVM version in the matrix
- Long-term goal: upstream the triple to LLVM (requires a stable
  ABI and a community around TerranoxOS)

---

## 11. Stage-by-Stage Bootstrap — CMake Flag Reference

Every CMake flag in the bootstrap exists for a reason.
This section explains each one.

### Stage 0: Cross-Compiler (Wolfi Clang → musl-targeting Clang)

```bash
cmake -G Ninja \
    # ── Build type ──
    -DCMAKE_BUILD_TYPE=Release              # Optimized, no debug info (faster)
    # Use MinSizeRel for smaller binaries, Debug for debugging the compiler itself

    # ── Install location ──
    -DCMAKE_INSTALL_PREFIX=${CLFS_CROSS}    # /opt/terranox/cross-tools
    # NOT /usr — this is a temporary cross-compiler

    # ── What to build ──
    -DLLVM_ENABLE_PROJECTS="clang;lld"      # Compiler + linker only
    # NOT "all" — building everything takes hours longer
    # clang: the C/C++ frontend
    # lld: our linker (replaces GNU ld)

    -DLLVM_ENABLE_RUNTIMES=""               # NO runtimes yet
    # compiler-rt, libc++ etc. need musl headers, which we don't
    # have in the sysroot yet. Runtimes come in Stage 05.

    -DLLVM_TARGETS_TO_BUILD="X86"           # Only x86_64
    # Each target adds ~5-10 min to build time
    # Add "AArch64" when you need ARM64 cross-compilation

    # ── Defaults baked into the built compiler ──
    -DLLVM_DEFAULT_TARGET_TRIPLE=x86_64-linux-musl
    # Every compile without --target uses this triple
    # Using x86_64-linux-musl (not x86_64-pc-terranox) because
    # the terranox triple isn't patched in yet at Stage 0

    -DCLANG_DEFAULT_LINKER=lld              # Use lld, not ld
    -DCLANG_DEFAULT_CXX_STDLIB=libc++       # Use libc++, not libstdc++
    -DCLANG_DEFAULT_RTLIB=compiler-rt       # Use compiler-rt, not libgcc
    -DCLANG_DEFAULT_UNWINDLIB=libunwind     # Use libunwind, not libgcc_s

    # ── Build options ──
    -DLLVM_INSTALL_TOOLCHAIN_ONLY=ON        # Skip headers/libs we don't need
    -DLLVM_INCLUDE_TESTS=OFF                # Don't build LLVM test suite
    -DLLVM_INCLUDE_EXAMPLES=OFF
    -DLLVM_INCLUDE_BENCHMARKS=OFF
    -DLLVM_INCLUDE_DOCS=OFF
    # Each of these saves 5-15 min of build time

    /path/to/llvm-project/llvm              # Source directory (llvm/ subdir)
```

**Build time:** 30-90 minutes depending on CPU.
**Output:** `${CLFS_CROSS}/bin/clang` that defaults to musl target.

### Stage 04: Full musl (built by Stage 0 Clang)

```bash
# musl's configure is NOT autoconf — it's a custom script
CC="${CLFS_CROSS}/bin/clang"                # Use our Stage 0 Clang
AR="${CLFS_CROSS}/bin/llvm-ar"              # Use LLVM ar, not GNU ar
RANLIB="${CLFS_CROSS}/bin/llvm-ranlib"

# --target tells musl's configure what we're building FOR
./configure \
    --prefix=/usr \                         # Install paths in final sysroot
    --target=x86_64-linux-musl \
    --disable-wrapper                       # Don't create musl-gcc wrapper
    # musl-gcc is for GCC compatibility — we don't need it

make -j$(nproc)
make DESTDIR=${CLFS_SYSROOT} install
# DESTDIR prepends to all install paths
# Result: ${CLFS_SYSROOT}/usr/lib/libc.a, crt1.o, etc.

# Critical symlink: the dynamic linker
ln -sf /usr/lib/libc.so ${CLFS_SYSROOT}/lib/ld-musl-x86_64.so.1
```

### Stage 05: LLVM Runtimes (against full musl sysroot)

This is the most complex stage. We build 4 libraries in
dependency order within a single CMake invocation:

```bash
cmake -G Ninja \
    -DCMAKE_BUILD_TYPE=Release
    -DCMAKE_INSTALL_PREFIX=${CLFS_SYSROOT}/usr

    # ── Use Stage 0 Clang as the compiler ──
    -DCMAKE_C_COMPILER=${CLFS_CROSS}/bin/clang
    -DCMAKE_CXX_COMPILER=${CLFS_CROSS}/bin/clang++

    # ── Cross-compile for musl ──
    -DCMAKE_C_COMPILER_TARGET=x86_64-linux-musl
    -DCMAKE_CXX_COMPILER_TARGET=x86_64-linux-musl
    -DCMAKE_ASM_COMPILER_TARGET=x86_64-linux-musl
    -DCMAKE_SYSROOT=${CLFS_SYSROOT}
    # SYSROOT tells the compiler where to find headers and libraries
    # This is why we need full musl BEFORE building runtimes

    # ── What runtimes to build ──
    -DLLVM_ENABLE_RUNTIMES="compiler-rt;libunwind;libcxxabi;libcxx"
    # ORDER MATTERS in the source, but CMake handles the build
    # order. The dependency chain is:
    #   compiler-rt (no deps, replaces libgcc builtins)
    #   └── libunwind (needs compiler-rt for builtins)
    #       └── libc++abi (needs libunwind for exception unwinding)
    #           └── libc++ (needs libc++abi for ABI layer)

    # ── compiler-rt configuration ──
    -DCOMPILER_RT_BUILD_BUILTINS=ON         # The essential part
    -DCOMPILER_RT_BUILD_SANITIZERS=OFF      # ASan, UBSan — later
    -DCOMPILER_RT_BUILD_XRAY=OFF            # Function tracing — later
    -DCOMPILER_RT_BUILD_LIBFUZZER=OFF       # Fuzzing — later
    -DCOMPILER_RT_BUILD_PROFILE=OFF         # PGO — later
    -DCOMPILER_RT_BUILD_MEMPROF=OFF
    -DCOMPILER_RT_BUILD_ORC=OFF             # JIT — never
    -DCOMPILER_RT_DEFAULT_TARGET_ONLY=ON    # Only build for our target
    # Builtins-only keeps the build small and fast

    # ── libunwind configuration ──
    -DLIBUNWIND_USE_COMPILER_RT=ON          # Link against compiler-rt, not libgcc
    -DLIBUNWIND_ENABLE_STATIC=ON
    -DLIBUNWIND_ENABLE_SHARED=ON

    # ── libc++abi configuration ──
    -DLIBCXXABI_USE_COMPILER_RT=ON
    -DLIBCXXABI_USE_LLVM_UNWINDER=ON        # Use libunwind, not GCC unwinder
    -DLIBCXXABI_ENABLE_STATIC=ON

    # ── libc++ configuration ──
    -DLIBCXX_USE_COMPILER_RT=ON
    -DLIBCXX_HAS_MUSL_LIBC=ON              # Tell libc++ about musl quirks
    -DLIBCXX_ENABLE_STATIC=ON
    -DLIBCXX_CXX_ABI=libcxxabi              # Use libc++abi, not libsupc++

    /path/to/llvm-project/runtimes          # Note: /runtimes, NOT /llvm
```

**After this stage, create the libgcc compatibility shim:**

```bash
# Many packages look for -lgcc_s. This tells the linker to use
# compiler-rt builtins + libunwind instead.
echo 'INPUT(-lclang_rt.builtins -lunwind)' > ${CLFS_SYSROOT}/usr/lib/libgcc_s.so
```

### Stage 06: Stage 1 Clang (self-hosted, zero GNU deps)

```bash
cmake -G Ninja \
    -DCMAKE_BUILD_TYPE=Release
    -DCMAKE_INSTALL_PREFIX=${CLFS_TOOLS}    # /opt/terranox/tools (final output)

    # ── Use Stage 0 as the compiler ──
    -DCMAKE_C_COMPILER=${CLFS_CROSS}/bin/clang
    -DCMAKE_CXX_COMPILER=${CLFS_CROSS}/bin/clang++

    # ── Cross-compile flags ──
    -DCMAKE_C_FLAGS="--target=x86_64-linux-musl --sysroot=${CLFS_SYSROOT}"
    -DCMAKE_CXX_FLAGS="--target=x86_64-linux-musl --sysroot=${CLFS_SYSROOT} -stdlib=libc++"
    # -stdlib=libc++ is critical — without it, CMake's CXX check
    # tries to link with libstdc++ which doesn't exist in our sysroot

    # ── Host triple = target triple (self-hosted) ──
    -DLLVM_HOST_TRIPLE=x86_64-linux-musl
    # This tells LLVM "the machine running this compiler has
    # the same architecture as the machine we're building for."
    # This is what makes Stage 1 self-hosted.

    -DDEFAULT_SYSROOT=${CLFS_SYSROOT}
    # Bake the sysroot path into the compiler so users don't
    # need --sysroot on every invocation

    # Everything else same as Stage 0...
```

**Verification after Stage 1:**

```bash
# The binary should link against musl, not glibc
ldd ${CLFS_TOOLS}/bin/clang
# Expected: ld-musl-x86_64.so.1 (NOT ld-linux-x86-64.so.2)

# If you see libstdc++ here, something went wrong in Stage 05
# If you see libgcc_s, the compiler-rt linkage failed
```

---

## 12. musl-Specific Issues

### Headers that differ from glibc

musl is intentionally minimal. Some headers exist but with
fewer declarations than glibc.

```
Header              Issue                           Fix
------              -----                           ---
<execinfo.h>        DOES NOT EXIST                  Use libunwind for backtraces
<bits/types.h>      DOES NOT EXIST                  Don't include directly
<sys/cdefs.h>       Missing __THROW, __nonnull      Add -include stdc-predef.h or patch
<sys/queue.h>       DOES NOT EXIST                  Copy from BSD (permissive license)
<error.h>           DOES NOT EXIST                  Use err.h or write wrapper
<printf.h>          DOES NOT EXIST                  No custom printf specifiers
<malloc.h>          Exists but minimal              No mallinfo(), no malloc_trim()
<features.h>        No _GNU_SOURCE effect on most   Some GNU extensions just don't exist
```

### Common build failures

```bash
# "undefined reference to __dso_handle"
# Cause: libc++abi expects this symbol for atexit in shared libs
# Fix: ensure libc++abi is linked, or add:
echo 'void *__dso_handle = &__dso_handle;' > dso_handle.c

# "undefined reference to __cxa_thread_atexit_impl"
# Cause: musl doesn't implement __cxa_thread_atexit_impl
# Fix: build libc++abi with -DLIBCXXABI_HAS_CXA_THREAD_ATEXIT_IMPL=OFF
# or link against musl >= 1.1.20 (which added it)

# "error: use of undeclared identifier 'SYS_xxx'"
# Cause: musl doesn't define all Linux syscall numbers in headers
# Fix: #include <sys/syscall.h> directly

# "warning: unrecognized command-line option '-rdynamic'"
# Cause: lld doesn't need -rdynamic but some build systems pass it
# Fix: add -Wno-unused-command-line-argument

# "cannot find -lgcc_s"
# Cause: many packages hardcode -lgcc_s in their build system
# Fix: the libgcc_s.so shim from Stage 05 (INPUT(-lclang_rt.builtins -lunwind))

# "cannot find crtbeginS.o" / "cannot find crtendS.o"
# Cause: GCC provides these, Clang uses its own from compiler-rt
# Fix: ensure COMPILER_RT_BUILD_CRT=ON when building compiler-rt
# or create empty stubs: ar rcs crtbeginS.o && ar rcs crtendS.o
```

### Packages that need musl patches

```
Package         Issue                               Source for patches
-------         -----                               ------------------
glibc itself    Can't build (obviously)             Use musl
binutils        Hardcoded glibc paths               Use LLVM tools instead
Python          Missing __GNU_SOURCE functions       Alpine Linux patches
Node.js         Assumes glibc in configure          Alpine / Void patches
gdb             Uses ptrace extensions               musl-specific ptrace.h
systemd         Deeply tied to glibc                 Don't use (s6 or dinit)
util-linux      Some tools assume glibc internals   Void Linux patches
```

**Best patch sources:**
- Alpine Linux: https://gitlab.alpinelinux.org/alpine/aports
- Void Linux (musl variant): https://github.com/void-linux/void-packages
- Chimera Linux: https://github.com/chimera-linux (Clang + musl, closest to Terranox)

---

## 13. Known Failure Modes and Debugging

### Stage 0 failures

**Symptom:** CMake configuration fails immediately
```
CMake Error: The C compiler is not able to compile a simple test program.
```
**Cause:** Wolfi's Clang symlinks are wrong or missing.
**Debug:**
```bash
clang --version          # Should work
which clang              # Should point to clang-19 or unversioned
clang -v -xc /dev/null -o /dev/null  # Full verbose output
```
**Fix:** Check the symlink creation in the Dockerfile.

**Symptom:** Ninja build fails linking clang binary
```
ld.lld: error: undefined symbol: __cxa_atexit
```
**Cause:** Stage 0 is trying to link against libc++ but we're
building on the host (glibc), not the target (musl). Stage 0
should use the HOST C++ library for linking its own binary.
**Fix:** Don't set `-DCMAKE_CXX_FLAGS=-stdlib=libc++` for Stage 0.
That flag is only for Stage 1.

### Stage 04 (musl) failures

**Symptom:** musl configure fails
```
./configure: error: could not determine target architecture
```
**Cause:** Clang's default target doesn't match what musl expects.
**Debug:**
```bash
${CLFS_CROSS}/bin/clang -dumpmachine
# Should output: x86_64-linux-musl
```
**Fix:** Ensure Stage 0 was built with `-DLLVM_DEFAULT_TARGET_TRIPLE=x86_64-linux-musl`.

**Symptom:** musl builds but libc.a is empty or tiny
**Cause:** Clang is optimizing out everything because of wrong flags.
**Debug:**
```bash
llvm-ar t ${CLFS_SYSROOT}/usr/lib/libc.a | wc -l
# Should be ~600-800 object files
llvm-nm ${CLFS_SYSROOT}/usr/lib/libc.a | grep " T " | wc -l
# Should be ~1500+ exported symbols
```

### Stage 05 (runtimes) failures

**Symptom:** compiler-rt build fails finding headers
```
fatal error: 'stddef.h' file not found
```
**Cause:** The compiler can't find its own resource directory headers.
**Debug:**
```bash
${CLFS_CROSS}/bin/clang --print-resource-dir
# Should output a path like: /opt/terranox/cross-tools/lib/clang/21
ls $(${CLFS_CROSS}/bin/clang --print-resource-dir)/include/stddef.h
# This file MUST exist
```
**Fix:** Ensure Stage 0 was installed completely (`ninja install` ran to completion).

**Symptom:** libc++ build fails with libunwind errors
```
undefined reference to `_Unwind_Resume`
```
**Cause:** Build order wrong — libc++ needs libunwind + libc++abi.
**Fix:** Build all runtimes in ONE cmake invocation using
`-DLLVM_ENABLE_RUNTIMES="compiler-rt;libunwind;libcxxabi;libcxx"`.
CMake handles the internal dependency order. Don't build them separately.

### Stage 06 (Stage 1) failures

**Symptom:** Stage 1 Clang binary links against glibc
```
$ ldd ${CLFS_TOOLS}/bin/clang
  ld-linux-x86-64.so.2 => /lib64/ld-linux-x86-64.so.2
  libstdc++.so.6 => /usr/lib/x86_64-linux-gnu/libstdc++.so.6
```
**Cause:** CMAKE_CXX_FLAGS didn't include `-stdlib=libc++` or
the sysroot wasn't set correctly.
**Debug:**
```bash
# Check what the Stage 0 compiler thinks:
${CLFS_CROSS}/bin/clang++ --target=x86_64-linux-musl \
    --sysroot=${CLFS_SYSROOT} -stdlib=libc++ -v -xc++ /dev/null -o /dev/null 2>&1 \
    | grep "Selected GCC\|include\|library"
```
**Fix:** Ensure both `CMAKE_C_FLAGS` and `CMAKE_CXX_FLAGS` in
Stage 06 include `--target=x86_64-linux-musl --sysroot=...`.

### General debugging tools

```bash
# What triple is the compiler targeting?
clang -dumpmachine

# What search paths is it using?
clang -v -xc /dev/null -o /dev/null 2>&1

# What files does it find?
clang -v --target=x86_64-linux-musl --sysroot=... -xc /dev/null -o /dev/null 2>&1

# What's in a binary?
file ./binary                    # ELF class, architecture, linker
readelf -d ./binary              # Dynamic dependencies
ldd ./binary                     # Shared library resolution
llvm-nm ./binary | grep " U "   # Undefined symbols
llvm-objdump -d ./binary | head  # Disassembly sanity check
```

---

## Reference

- CMLFS: https://github.com/dslm4515/CMLFS
- Chimera Linux: https://chimera-linux.org (production Clang+musl distro)
- LLVM Cross-Compile docs: https://llvm.org/docs/HowToCrossCompileLLVM.html
- Clang Cross-Compile docs: https://clang.llvm.org/docs/CrossCompilation.html
- musl wiki — Building LLVM: https://wiki.musl-libc.org/building-llvm
- OSDev LLVM Cross-Compiler: https://wiki.osdev.org/LLVM_Cross-Compiler
- LFS Book (for package build order): https://www.linuxfromscratch.org/lfs/