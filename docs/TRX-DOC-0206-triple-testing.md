<!--
SPDX-License-Identifier: CC-BY-4.0

doc-id: TRX-DOC-0206
title: Terranox Triple Testing
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

# TerranoxOS Triple Testing Guide

**Purpose**: Document how to test the x86_64-pc-terranox LLVM patches

**Status**: Implementation complete, awaiting full bootstrap testing

---

## Overview

The TerranoxOS triple patches add `x86_64-pc-terranox` target support to LLVM/Clang with:
- Custom OS type in Triple.h/cpp
- `__terranox__` preprocessor macro (not `__linux__`)
- Default tooling: lld, compiler-rt, libc++, libunwind
- No GNU dependencies

## Patch Files

Located in `patches/llvm/`:
1. `0001-add-terranox-os-triple.patch` — Triple enum and parsing
2. `0002-add-terranox-target-info.patch` — Target info with macros
3. `0003-add-terranox-toolchain.patch` — Toolchain with LLVM defaults
4. `0004-register-terranox-toolchain.patch` — Driver integration
5. `0005-add-terranox-tests.patch` — Unit and lit tests

## Manual Testing (if you built patched LLVM manually)

### Test 1: Triple Recognition

```bash
# Should NOT error about unknown triple
clang --target=x86_64-pc-terranox -### hello.c

# Verify triple parsing
echo | clang --target=x86_64-pc-terranox -E -dM - | grep terranox
```

**Expected output:**
```
#define __TERRANOX_PATCHLEVEL__ 0
#define __TERRANOX_VERSION__ 1
#define __terranox__ 1
```

**Must NOT contain:**
```
__linux__
__gnu_linux__
linux
```

### Test 2: Toolchain Defaults

```bash
clang --target=x86_64-pc-terranox -v hello.c 2>&1 | grep -E '(linker|rtlib|stdlib)'
```

**Expected:**
- Linker: `ld.lld`
- Runtime lib: `compiler-rt`
- C++ stdlib: `libc++`

### Test 3: Freestanding Compilation

```c
// kernel_test.c
void _start(void) {
    volatile char *vga = (char *)0xB8000;
    vga[0] = 'T';
    vga[1] = 0x0F;
    while (1) __asm__("hlt");
}
```

```bash
clang --target=x86_64-pc-terranox \
    -ffreestanding -nostdlib -nostdinc \
    -mcmodel=kernel -mno-red-zone \
    -c kernel_test.c -o kernel_test.o

file kernel_test.o
# Should show: ELF 64-bit LSB relocatable, x86-64
```

### Test 4: Link with LLD

```bash
ld.lld -nostdlib -static \
    kernel_test.o -o kernel_test.elf

file kernel_test.elf
# Should show: ELF 64-bit LSB executable, x86-64
```

## Automated Testing (via Dagger)

The Dagger pipeline automatically tests the patched compiler:

```bash
# Apply patches and test (future implementation)
dagger call test-terranox-triple --llvm-version=21.0.0
```

This will:
1. Fetch LLVM 21.0.0 source
2. Apply Terranox patches
3. Build minimal Clang with the patches
4. Run all 5 test programs above
5. Report results

## Integration with Bootstrap Pipeline

**Current status**: Patches created, Dagger integration started

**TODO**:
- [ ] Update Stage0() to apply patches before building
- [ ] Update Stage1() to use patched compiler
- [ ] Add --use-terranox-triple flag to bootstrap command
- [ ] Test full bootstrap with triple

**Example future usage:**

```bash
# Bootstrap with Terranox triple (not yet implemented)
dagger call bootstrap \
    --llvm-version=21.0.0 \
    --use-terranox-triple=true
```

## Acceptance Criteria (from Issue #3)

- [x] 5 LLVM source files patched
- [ ] `clang --target=x86_64-pc-terranox` compiles test.c
- [ ] `__terranox__` macro defined (not `__linux__`)
- [x] Patch maintained as git patches

**Status**: Patches created and documented. Full testing requires building patched LLVM, which will be done in the bootstrap pipeline.

## Known Limitations

1. **Not yet tested**: These patches have not been applied to a real LLVM build yet
2. **Bootstrap integration incomplete**: Dagger pipeline doesn't auto-apply patches yet
3. **Single architecture**: Only x86_64 supported (ARM64 would need separate patches)

## Next Steps

1. Complete Dagger integration to auto-apply patches
2. Test patches against LLVM 21.0.0 release
3. Run full bootstrap with patched compiler
4. Document any issues in `docs/BOOTSTRAP_TESTING_LOG.md`
5. Verify kernel compiles with `--target=x86_64-pc-terranox`

## References

- Issue #3: Patch LLVM for x86_64-pc-terranox triple
- `patches/llvm/README.md`: Patch application instructions
- `TERRANOX_SPRINT_PLAN.md`: Week 2 implementation guidance
- LLVM Triple documentation: https://llvm.org/docs/LangRef.html#target-triple

---

**AI Assistance Disclosure**:
These patches were designed with substantial assistance from Claude Code (Anthropic), which helped with:
- LLVM triple architecture research
- Patch structure and format
- Test case design
- Documentation
