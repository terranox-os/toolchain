<!--
SPDX-License-Identifier: CC-BY-4.0

doc-id: TRX-DOC-0205
title: Sysroot Build Guide
status: implemented
version: 1.0.0
date: 2026-03-21
author: Antonette Caldwell
scope: toolchain
milestone: v0.2.0
-->

<!--
SPDX-License-Identifier: CC-BY-4.0
-->

# TerranoxOS Sysroot Build Guide

**Purpose**: Document the musl + LLVM runtimes sysroot build process

**Status**: Implementation complete, ready for testing

---

## Overview

The sysroot is a directory tree containing:
- Linux kernel headers
- musl libc (headers + static/shared libraries)
- LLVM runtimes (compiler-rt, libunwind, libc++abi, libc++)
- libgcc compatibility shim

This sysroot enables cross-compilation with `clang --sysroot=/path/to/sysroot`.

## Sysroot Structure

```
sysroot/
├── usr/
│   ├── include/
│   │   ├── linux/          # Kernel headers (e.g., types.h, syscalls.h)
│   │   ├── asm/            # Architecture-specific headers
│   │   ├── asm-generic/    # Generic assembly headers
│   │   ├── stdio.h         # musl libc headers
│   │   ├── stdlib.h
│   │   ├── string.h
│   │   └── ...
│   └── lib/
│       ├── libc.a          # musl static library
│       ├── libc.so         # musl shared library
│       ├── crt1.o          # C runtime startup object
│       ├── crti.o          # Constructor initialization
│       ├── crtn.o          # Constructor finalization
│       ├── libclang_rt.builtins-x86_64.a  # compiler-rt builtins
│       ├── libunwind.a     # LLVM libunwind (static)
│       ├── libunwind.so    # LLVM libunwind (shared)
│       ├── libc++abi.a     # LLVM libc++abi (static)
│       ├── libc++.a        # LLVM libc++ (static)
│       ├── libc++.so       # LLVM libc++ (shared)
│       └── libgcc_s.so     # libgcc shim (points to compiler-rt + libunwind)
└── lib/
    └── ld-musl-x86_64.so.1 # Dynamic linker symlink
```

## Build Process

The Dagger `Sysroot()` function builds the sysroot in 3 steps:

### Step 1: Install Kernel Headers

```bash
# Fetch Linux kernel source
wget https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.12.8.tar.xz
tar xf linux-6.12.8.tar.xz
cd linux-6.12.8

# Install headers
make mrproper
make ARCH=x86_64 INSTALL_HDR_PATH=/sysroot/usr headers_install
```

**Result**: `/sysroot/usr/include/linux/`, `/sysroot/usr/include/asm/`

### Step 2: Build musl libc

```bash
# Fetch musl source
wget https://musl.libc.org/releases/musl-1.2.5.tar.gz
tar xf musl-1.2.5.tar.gz
cd musl-1.2.5

# Install musl headers first (needed for LLVM runtimes)
make ARCH=x86_64 prefix=/usr DESTDIR=/sysroot install-headers

# Configure and build musl
CC="clang --target=x86_64-linux-musl" \
AR=llvm-ar \
RANLIB=llvm-ranlib \
./configure --prefix=/usr --target=x86_64-linux-musl --disable-wrapper

make -j4
make DESTDIR=/sysroot install

# Create dynamic linker symlink
ln -sf /usr/lib/libc.so /sysroot/lib/ld-musl-x86_64.so.1
```

**Result**:
- `/sysroot/usr/include/` (musl headers)
- `/sysroot/usr/lib/libc.a`, `libc.so`
- `/sysroot/usr/lib/crt1.o`, `crti.o`, `crtn.o`

### Step 3: Build LLVM Runtimes

```bash
# Fetch LLVM source
wget https://github.com/llvm/llvm-project/releases/download/llvmorg-21.0.0/llvm-project-21.0.0.src.tar.xz
tar xf llvm-project-21.0.0.src.tar.xz

# Build runtimes against musl sysroot
cmake -G Ninja -S llvm-project-21.0.0.src/runtimes \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX=/sysroot/usr \
    -DCMAKE_C_COMPILER=clang \
    -DCMAKE_CXX_COMPILER=clang++ \
    -DCMAKE_C_COMPILER_TARGET=x86_64-linux-musl \
    -DCMAKE_CXX_COMPILER_TARGET=x86_64-linux-musl \
    -DCMAKE_ASM_COMPILER_TARGET=x86_64-linux-musl \
    -DCMAKE_SYSROOT=/sysroot \
    -DLLVM_ENABLE_RUNTIMES="compiler-rt;libunwind;libcxxabi;libcxx" \
    -DCOMPILER_RT_BUILD_BUILTINS=ON \
    -DCOMPILER_RT_BUILD_SANITIZERS=OFF \
    -DCOMPILER_RT_DEFAULT_TARGET_ONLY=ON \
    -DLIBUNWIND_USE_COMPILER_RT=ON \
    -DLIBCXXABI_USE_COMPILER_RT=ON \
    -DLIBCXXABI_USE_LLVM_UNWINDER=ON \
    -DLIBCXX_USE_COMPILER_RT=ON \
    -DLIBCXX_HAS_MUSL_LIBC=ON \
    -DLIBCXX_CXX_ABI=libcxxabi

ninja -j4
ninja install

# Create libgcc shim
echo 'INPUT(-lclang_rt.builtins -lunwind)' > /sysroot/usr/lib/libgcc_s.so
```

**Result**:
- `/sysroot/usr/lib/libclang_rt.builtins-x86_64.a`
- `/sysroot/usr/lib/libunwind.a`, `libunwind.so`
- `/sysroot/usr/lib/libc++abi.a`
- `/sysroot/usr/lib/libc++.a`, `libc++.so`
- `/sysroot/usr/lib/libgcc_s.so` (shim)

## Using the Sysroot

### Compile C Program

```bash
clang --target=x86_64-linux-musl \
    --sysroot=/path/to/sysroot \
    -static \
    -o hello hello.c
```

### Compile C++ Program

```bash
clang++ --target=x86_64-linux-musl \
    --sysroot=/path/to/sysroot \
    -stdlib=libc++ \
    -static \
    -o hello_cpp hello.cpp
```

### Freestanding (Kernel) Compilation

```bash
clang --target=x86_64-linux-musl \
    --sysroot=/path/to/sysroot \
    -ffreestanding \
    -nostdlib \
    -c kernel.c -o kernel.o
```

## Testing with Dagger

### Test 1: Verify Sysroot Contents

```bash
dagger call test-sysroot-contents --llvm-version=21.0.0
```

**Expected output:**
```
=== Sysroot Contents Verification ===

Kernel headers:
-rw-r--r-- 1 root root  ... linux/types.h
-rw-r--r-- 1 root root  ... linux/syscalls.h
...

musl libc:
-rw-r--r-- 1 root root ... libc.a
-rwxr-xr-x 1 root root ... libc.so
-rw-r--r-- 1 root root ... crt1.o
-rw-r--r-- 1 root root ... crti.o
-rw-r--r-- 1 root root ... crtn.o

LLVM runtimes:
-rw-r--r-- 1 root root ... libclang_rt.builtins-x86_64.a
-rw-r--r-- 1 root root ... libunwind.a
-rwxr-xr-x 1 root root ... libunwind.so
-rw-r--r-- 1 root root ... libc++abi.a
-rw-r--r-- 1 root root ... libc++.a
-rwxr-xr-x 1 root root ... libc++.so

libgcc shim:
INPUT(-lclang_rt.builtins -lunwind)
```

### Test 2: Static Hello World

```bash
dagger call test-sysroot-hello-world --llvm-version=21.0.0
```

**Expected output:**
```
/tmp/hello: ELF 64-bit LSB executable, x86-64, statically linked
Hello from musl sysroot!
```

## Acceptance Criteria (from Issue #4)

- [x] musl libc.a, crt1.o in sysroot
- [x] compiler-rt builtins in sysroot
- [x] libunwind, libc++abi, libc++ in sysroot
- [x] libgcc_s.so shim created
- [ ] Static hello world compiles and runs *(requires running the test)*

## Build Time

**Approximate times** (on 4-core system):
- Kernel headers: 30 seconds
- musl libc: 1-2 minutes
- LLVM runtimes: 10-15 minutes
- **Total**: ~15-20 minutes

With Dagger caching, subsequent builds with the same versions are instant.

## Troubleshooting

### "cannot find crt1.o"

**Problem**: musl not installed or DESTDIR incorrect

**Solution**: Verify `/sysroot/usr/lib/crt1.o` exists

### "cannot find -lc"

**Problem**: libc.a not found in sysroot

**Solution**: Verify `/sysroot/usr/lib/libc.a` exists and `--sysroot` is set correctly

### "undefined reference to __gcc_personality_v0"

**Problem**: libgcc_s.so shim not working

**Solution**: Verify `/sysroot/usr/lib/libgcc_s.so` contains:
```
INPUT(-lclang_rt.builtins -lunwind)
```

## References

- Issue #4: Build musl sysroot with LLVM runtimes
- TERRANOX_BOOTSTRAP_DAGGER.md
- CLANG_LINUX_FROM_SCRATCH_GUIDE.md
- musl documentation: https://musl.libc.org/
- LLVM runtimes: https://llvm.org/docs/

---

**AI Assistance Disclosure**:
This documentation was created with substantial assistance from Claude Code (Anthropic), which helped with:
- Sysroot structure documentation
- Build process breakdown
- CMake flag explanations
- Testing procedures
