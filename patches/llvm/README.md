<!--
SPDX-License-Identifier: CC-BY-4.0
-->

# LLVM Patches for TerranoxOS

This directory contains patches to add `x86_64-pc-terranox` target triple support to LLVM/Clang.

## Overview

These patches add a custom OS triple to LLVM that:
- Defines `__terranox__` macro (not `__linux__`)
- Defaults to pure LLVM tooling: lld, compiler-rt, libc++, libunwind
- Enables compilation with `clang --target=x86_64-pc-terranox`

## Patch Series

1. **0001-add-terranox-os-triple.patch** — Add Terranox to Triple.h/cpp
2. **0002-add-terranox-target-info.patch** — Add TerranoxOSTargetInfo with __terranox__ macro
3. **0003-add-terranox-toolchain.patch** — Add Terranox toolchain with LLVM defaults
4. **0004-register-terranox-toolchain.patch** — Register toolchain in Driver
5. **0005-add-terranox-tests.patch** — Add unit tests and lit tests

## Applying the Patches

### Manual Application

```bash
# Clone LLVM project
git clone https://github.com/llvm/llvm-project.git
cd llvm-project
git checkout llvmorg-21.0.0

# Apply patches
for patch in /path/to/terranox-os/patches/llvm/*.patch; do
    git am < "$patch"
done
```

### Using the apply_patches.sh Script

```bash
cd /path/to/llvm-project
/path/to/terranox-os/patches/llvm/apply_patches.sh
```

## Building Patched LLVM

```bash
cd llvm-project
cmake -S llvm -B build -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DLLVM_ENABLE_PROJECTS="clang;lld" \
    -DLLVM_TARGETS_TO_BUILD="X86" \
    -DLLVM_INSTALL_TOOLCHAIN_ONLY=ON

ninja -C build
sudo ninja -C build install
```

## Testing the Patched Compiler

### Test 1: Triple Recognition

```bash
clang --target=x86_64-pc-terranox -### test.c
# Should NOT error with "unknown target triple"
```

### Test 2: Preprocessor Macros

```bash
echo | clang --target=x86_64-pc-terranox -dM -E - | grep -E '(terranox|linux)'
# Should show:
#   #define __terranox__ 1
#   #define __TERRANOX_VERSION__ 1
# Should NOT show:
#   __linux__
```

### Test 3: Toolchain Defaults

```bash
clang --target=x86_64-pc-terranox -v test.c 2>&1 | grep -E '(lld|compiler-rt|libc\+\+)'
# Should use:
#   - Linker: ld.lld
#   - Runtime: compiler-rt
#   - C++ stdlib: libc++
```

### Test 4: Freestanding Compilation

```bash
clang --target=x86_64-pc-terranox -ffreestanding -c test.c -o test.o
file test.o
# Should produce valid ELF x86-64 relocatable
```

## Integration with Dagger

The Dagger bootstrap pipeline will automatically:
1. Fetch LLVM source (version from bootstrap.toml)
2. Apply these patches
3. Build the patched compiler
4. Use it for all subsequent builds

## Maintenance

When updating LLVM versions:
1. Test patches against new version
2. Resolve any conflicts
3. Update this README with tested versions
4. Update bootstrap.toml default version

## Tested LLVM Versions

- ✅ LLVM 21.0.0 (primary target)
- ⚠️ LLVM 20.0.6 (not yet tested)
- ⚠️ LLVM 19.1.7 (not yet tested)
- ⚠️ LLVM main (may require adjustments)

## References

- Issue #3: Patch LLVM for x86_64-pc-terranox triple
- TERRANOX_BOOTSTRAP_DAGGER.md
- TERRANOX_SPRINT_PLAN.md (Week 2 guidance)

## License

These patches are released under the Apache-2.0 license to match LLVM's licensing.
The modifications are minimal and follow LLVM's coding standards.
