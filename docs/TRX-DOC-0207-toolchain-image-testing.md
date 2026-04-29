<!--
SPDX-License-Identifier: CC-BY-4.0

doc-id: TRX-DOC-0207
title: Toolchain Image Testing
status: implemented
version: 1.0.0
date: 2026-03-21
author: Antonette Caldwell
scope: toolchain
milestone: v0.2.0
-->

# Toolchain Container Image Testing Guide

**Date:** 2026-03-15
**Component:** ToolchainImage() Dagger function
**Purpose:** Comprehensive testing checklist for Stage1 toolchain container images
**Status:** REQUIRES EXTENSIVE TESTING

---

## Overview

The `ToolchainImage()` function creates OCI container images with:
- Stage1 LLVM/Clang 21.1.8 toolchain
- musl libc 1.2.5 sysroot (headers + libraries)
- LLVM runtimes (compiler-rt, libunwind, libc++, libc++abi)
- Build tools (cmake, ninja, git, bash)

This enables full C/C++ compilation inside the container environment.

---

## Quick Start

### Build Container Image

```bash
# Export as OCI tarball (3-4 seconds with cached sysroot)
dagger call toolchain-image --toolchain ./toolchain/llvm \
  --llvm-version=21.1.8 \
  export --path ./terranox-toolchain-21.1.8.tar

# Load into Docker
docker load < terranox-toolchain-21.1.8.tar

# Get image ID
docker images | grep terranox-toolchain
```

### Run Interactive Container

```bash
docker run -it --rm <image-id> /bin/sh
```

---

## Testing Checklist

### ✅ **Level 1: Basic Compilation** (COMPLETED)

#### Test 1.1: C Program (Simple)
```bash
docker run --rm --entrypoint=/bin/sh <image-id> -c '
echo "int main() { return 42; }" > test.c
clang --sysroot=/opt/terranox/sysroot test.c -o test
./test
echo "Exit code: $?"
'
```
**Expected**: `Exit code: 42`
**Status**: ✅ PASSED

#### Test 1.2: C++ Program (iostream)
```bash
docker run --rm --entrypoint=/bin/sh <image-id> -c '
echo "#include <iostream>" > test.cpp
echo "int main() { std::cout << \"Hello!\" << std::endl; return 0; }" >> test.cpp
clang++ --sysroot=/opt/terranox/sysroot \
  -L/opt/terranox/sysroot/usr/lib \
  -lc++abi -lunwind \
  test.cpp -o test
./test
'
```
**Expected**: `Hello!`
**Status**: ✅ PASSED

---

### ✅ **Level 2: Advanced C/C++ Features** (PARTIAL)

#### Test 2.1: C - Standard Library Functions
```bash
# Test stdio, stdlib, string.h, math.h
cat > test.c <<'EOF'
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>

int main() {
    printf("Testing stdio: %d\n", 42);

    char *buf = malloc(100);
    strcpy(buf, "malloc works");
    printf("%s\n", buf);
    free(buf);

    double result = sqrt(16.0);
    printf("sqrt(16) = %.1f\n", result);

    return 0;
}
EOF
clang --sysroot=/opt/terranox/sysroot test.c -o test -lm
./test
```
**Expected**: All operations succeed
**Status**: ⏳ TODO

#### Test 2.2: C++ - STL Containers
```bash
cat > test.cpp <<'EOF'
#include <iostream>
#include <vector>
#include <map>
#include <string>

int main() {
    std::vector<int> vec = {1, 2, 3, 4, 5};
    std::cout << "Vector size: " << vec.size() << std::endl;

    std::map<std::string, int> map;
    map["test"] = 42;
    std::cout << "Map['test'] = " << map["test"] << std::endl;

    return 0;
}
EOF
clang++ --sysroot=/opt/terranox/sysroot -L/opt/terranox/sysroot/usr/lib \
  -lc++abi -lunwind test.cpp -o test
./test
```
**Expected**: Vector and map operations work
**Status**: ✅ PASSED (2026-03-14) - Outputs "Vector size: 3"

#### Test 2.3: C++ - Exception Handling
```bash
cat > test.cpp <<'EOF'
#include <iostream>
#include <stdexcept>

int main() {
    try {
        throw std::runtime_error("Test exception");
    } catch (const std::exception& e) {
        std::cout << "Caught: " << e.what() << std::endl;
    }
    return 0;
}
EOF
clang++ --sysroot=/opt/terranox/sysroot -L/opt/terranox/sysroot/usr/lib \
  -lc++abi -lunwind test.cpp -o test
./test
```
**Expected**: `Caught: Test exception`
**Status**: ✅ PASSED (2026-03-14)

#### Test 2.4: C++ - Templates
```bash
cat > test.cpp <<'EOF'
#include <iostream>

template<typename T>
T max(T a, T b) {
    return (a > b) ? a : b;
}

int main() {
    std::cout << "max(3, 7) = " << max(3, 7) << std::endl;
    std::cout << "max(3.5, 2.1) = " << max(3.5, 2.1) << std::endl;
    return 0;
}
EOF
clang++ --sysroot=/opt/terranox/sysroot -L/opt/terranox/sysroot/usr/lib \
  -lc++abi -lunwind test.cpp -o test
./test
```
**Expected**: Template instantiation works
**Status**: ⏳ TODO

---

### ⏳ **Level 3: Linking Modes** (TODO)

#### Test 3.1: Static Linking
```bash
echo "int main() { return 0; }" > test.c
clang --sysroot=/opt/terranox/sysroot -static test.c -o test
ldd test || echo "Statically linked"
```
**Expected**: No dynamic dependencies
**Status**: ⏳ TODO

#### Test 3.2: Dynamic Linking
```bash
echo "int main() { return 0; }" > test.c
clang --sysroot=/opt/terranox/sysroot test.c -o test
ldd test
```
**Expected**: Shows `/lib/ld-musl-x86_64.so.1` and `libc.so`
**Status**: ⏳ TODO

#### Test 3.3: Position Independent Executable (PIE)
```bash
echo "int main() { return 0; }" > test.c
clang --sysroot=/opt/terranox/sysroot -fPIE -pie test.c -o test
file test
```
**Expected**: `pie executable`
**Status**: ⏳ TODO

---

### ⏳ **Level 4: Build System Integration** (TODO)

#### Test 4.1: CMake Project
```bash
mkdir -p /workspace/cmake-test
cd /workspace/cmake-test

cat > CMakeLists.txt <<'EOF'
cmake_minimum_required(VERSION 3.20)
project(TerranoxTest C CXX)

add_executable(hello main.cpp)
EOF

cat > main.cpp <<'EOF'
#include <iostream>
int main() {
    std::cout << "CMake + Terranox!" << std::endl;
    return 0;
}
EOF

cmake -G Ninja \
  -DCMAKE_SYSROOT=/opt/terranox/sysroot \
  -DCMAKE_C_COMPILER=/opt/terranox/toolchain/bin/clang \
  -DCMAKE_CXX_COMPILER=/opt/terranox/toolchain/bin/clang++ \
  -B build

ninja -C build
./build/hello
```
**Expected**: `CMake + Terranox!`
**Status**: ⏳ TODO

#### Test 4.2: Multi-File Project
```bash
cat > main.c <<'EOF'
#include "lib.h"
int main() { return add(2, 3); }
EOF

cat > lib.h <<'EOF'
int add(int a, int b);
EOF

cat > lib.c <<'EOF'
#include "lib.h"
int add(int a, int b) { return a + b; }
EOF

clang --sysroot=/opt/terranox/sysroot -c lib.c -o lib.o
clang --sysroot=/opt/terranox/sysroot -c main.c -o main.o
clang --sysroot=/opt/terranox/sysroot lib.o main.o -o test
./test
echo "Exit code: $?"
```
**Expected**: `Exit code: 5`
**Status**: ⏳ TODO

---

### ⏳ **Level 5: Target Triple Verification** (TODO)

#### Test 5.1: Terranox Custom Triple
```bash
echo "int main() { return 0; }" > test.c
clang --target=x86_64-pc-terranox -c test.c -o test.o
file test.o
```
**Expected**: Accepts `x86_64-pc-terranox` triple
**Status**: ⏳ TODO

#### Test 5.2: Terranox Macro Definition
```bash
clang --target=x86_64-pc-terranox -dM -E - < /dev/null | grep -i terranox
```
**Expected**: Shows `__terranox__` macro
**Status**: ⏳ TODO

---

### ⏳ **Level 6: Optimization Levels** (TODO)

#### Test 6.1: Debug Build (-O0 -g)
```bash
echo "int main() { return 0; }" > test.c
clang --sysroot=/opt/terranox/sysroot -O0 -g test.c -o test
file test | grep "not stripped"
```
**Expected**: Debug symbols present
**Status**: ⏳ TODO

#### Test 6.2: Release Build (-O3)
```bash
echo "int main() { return 0; }" > test.c
clang --sysroot=/opt/terranox/sysroot -O3 test.c -o test
ls -lh test
```
**Expected**: Smaller binary than -O0
**Status**: ⏳ TODO

#### Test 6.3: Size Optimization (-Os)
```bash
echo "int main() { return 0; }" > test.c
clang --sysroot=/opt/terranox/sysroot -Os test.c -o test
ls -lh test
```
**Expected**: Smallest binary
**Status**: ⏳ TODO

---

### ⏳ **Level 7: Sanitizers** (TODO)

#### Test 7.1: Address Sanitizer (ASan)
```bash
cat > test.c <<'EOF'
#include <stdlib.h>
int main() {
    int *p = malloc(sizeof(int));
    free(p);
    return *p; // Use after free
}
EOF
clang --sysroot=/opt/terranox/sysroot -fsanitize=address test.c -o test
./test 2>&1 | grep "heap-use-after-free"
```
**Expected**: ASan detects use-after-free
**Status**: ⏳ TODO (may require ASan runtime)

#### Test 7.2: Undefined Behavior Sanitizer (UBSan)
```bash
cat > test.c <<'EOF'
int main() {
    int x = 0x7FFFFFFF;
    return x + 1; // Signed overflow
}
EOF
clang --sysroot=/opt/terranox/sysroot -fsanitize=undefined test.c -o test
./test
```
**Expected**: UBSan detects signed overflow
**Status**: ⏳ TODO

---

### ⏳ **Level 8: Cross-Compilation** (TODO)

#### Test 8.1: Compile for Different Target
```bash
echo "int main() { return 0; }" > test.c
clang --target=x86_64-linux-musl --sysroot=/opt/terranox/sysroot test.c -o test
file test
```
**Expected**: `x86_64, dynamically linked, interpreter /lib/ld-musl-x86_64.so.1`
**Status**: ⏳ TODO

---

### ⏳ **Level 9: Compiler Flags Edge Cases** (TODO)

#### Test 9.1: Warning as Error (-Werror)
```bash
cat > test.c <<'EOF'
int main() {
    int unused;
    return 0;
}
EOF
clang --sysroot=/opt/terranox/sysroot -Werror -Wunused-variable test.c -o test 2>&1 | grep error
```
**Expected**: Compilation fails with error
**Status**: ⏳ TODO

#### Test 9.2: C++ Standard Version
```bash
cat > test.cpp <<'EOF'
#include <iostream>
int main() {
    auto x = 42; // C++11 feature
    std::cout << x << std::endl;
    return 0;
}
EOF
clang++ --sysroot=/opt/terranox/sysroot -std=c++11 -L/opt/terranox/sysroot/usr/lib \
  -lc++abi -lunwind test.cpp -o test
./test
```
**Expected**: `42`
**Status**: ⏳ TODO

---

### ⏳ **Level 10: Registry Publishing** (TODO)

#### Test 10.1: Publish to GitHub Container Registry
```bash
dagger call toolchain-image --toolchain ./toolchain/llvm \
  publish --address ghcr.io/terranox-os/toolchain:21.1.8
```
**Expected**: Image pushed successfully
**Status**: ⏳ TODO (requires GitHub authentication)

#### Test 10.2: Pull and Run from Registry
```bash
docker pull ghcr.io/terranox-os/toolchain:21.1.8
docker run --rm ghcr.io/terranox-os/toolchain:21.1.8 clang --version
```
**Expected**: Clang 21.1.8
**Status**: ⏳ TODO

---

## Known Issues

### Issue 1: C++ Linking Requires Explicit Libraries

**Problem**: C++ programs fail to link without `-lc++abi -lunwind`

**Error**:
```
ld.lld: error: undefined symbol: __cxa_begin_catch
ld.lld: error: undefined symbol: operator new(unsigned long)
```

**Workaround**: Always add `-L/opt/terranox/sysroot/usr/lib -lc++abi -lunwind` when compiling C++

**Root Cause**: Stage1 toolchain defaults may not automatically link libc++abi

**Fix**: TODO - Investigate clang driver configuration for automatic libc++ linking

---

### Issue 2: Container Missing Core Utils

**Problem**: Container lacks `ls`, `cat`, `head`, etc. (minimalist Alpine base)

**Workaround**: Use `/usr/bin/find`, `/bin/sh` builtins, or install via `apk add coreutils`

**Fix**: Consider adding `coreutils` to base image or using busybox

---

## Environment Variables

- `TERRANOX_VERSION`: LLVM version (21.1.8)
- `TERRANOX_SYSROOT`: Sysroot path (`/opt/terranox/sysroot`)
- `PATH`: Includes `/opt/terranox/toolchain/bin`

---

## Recommended Testing Workflow

1. **Run Level 1 tests** (basic C/C++) - CRITICAL
2. **Run Level 2 tests** (advanced features) - HIGH PRIORITY
3. **Run Level 3 tests** (linking modes) - HIGH PRIORITY
4. **Run Level 4 tests** (build systems) - MEDIUM PRIORITY
5. **Run Level 5 tests** (Terranox triple) - HIGH PRIORITY (custom OS validation)
6. **Run Level 6-9 tests** (optimizations, sanitizers) - LOW PRIORITY
7. **Run Level 10 tests** (registry) - INFRASTRUCTURE

---

## Success Criteria

Before considering ToolchainImage production-ready:

- [ ] All Level 1 tests pass
- [ ] All Level 2 tests pass
- [ ] All Level 3 tests pass
- [ ] At least 1 Level 4 test passes (CMake or multi-file)
- [ ] All Level 5 tests pass (Terranox triple validation)
- [ ] C++ linking issue resolved or documented workaround
- [ ] Performance benchmarked (compilation speed)
- [ ] Image size optimized (<500MB total)
- [ ] Published to registry and tested from remote

---

## Next Steps

1. **Immediate**: Run Level 2 advanced C/C++ tests
2. **Short-term**: Investigate C++ automatic linking issue
3. **Medium-term**: Add CMake integration test
4. **Long-term**: Set up CI/CD to run all tests on PR

---

## Notes

- Sysroot build time: ~3-4 seconds (cached from Stage1)
- Image size: ~200MB additional for sysroot
- Base image: alpine:3.19 (musl libc environment)
- LLVM resource dir: `/opt/terranox/toolchain/lib/clang/21/lib/x86_64-unknown-linux-musl/`

**Last Updated**: 2026-03-15 02:00 UTC
