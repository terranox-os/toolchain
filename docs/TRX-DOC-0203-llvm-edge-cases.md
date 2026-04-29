<!--
SPDX-License-Identifier: CC-BY-4.0

doc-id: TRX-DOC-0203
title: LLVM Clang Edge Cases
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

# LLVM/Clang Edge Cases Reference
## A Comprehensive Guide for OS Development and LFS-Style System Building

**Version:** 1.0.0
**Target Audience:** Developers building operating systems or Linux From Scratch systems
with a pure LLVM/Clang + musl toolchain (zero GCC dependencies)
**Author:** Antonette — TerranoxOS Project
**Date:** March 2026

---

## Table of Contents

1. [Undefined Behavior Divergences](#1-undefined-behavior-divergences)
2. [Inline Assembly Differences](#2-inline-assembly-differences)
3. [ABI and Calling Convention Edge Cases](#3-abi-and-calling-convention-edge-cases)
4. [compiler-rt vs libgcc](#4-compiler-rt-vs-libgcc)
5. [Preprocessor and Language Extension Differences](#5-preprocessor-and-language-extension-differences)
6. [Linker (lld vs GNU ld) Edge Cases](#6-linker-lld-vs-gnu-ld-edge-cases)
7. [Optimization Divergences](#7-optimization-divergences)
8. [musl-Specific Edge Cases](#8-musl-specific-edge-cases)
9. [Build System Edge Cases](#9-build-system-edge-cases)
10. [Detection and Diagnostic Tools](#10-detection-and-diagnostic-tools)
11. [Recommended Compiler Flags](#11-recommended-compiler-flags)
12. [Package-Specific Fixes](#12-package-specific-fixes)

---

## 1. Undefined Behavior Divergences

Both GCC and Clang optimize based on undefined behavior (UB) as permitted by the
C/C++ standards. However, they exploit different UB categories at different
aggressiveness levels, meaning code that appears to "work" under GCC can silently
produce incorrect results under Clang — and vice versa.

### 1.1 Pointer Overflow

**Severity: CRITICAL for kernel/OS code**

Clang 20+ more aggressively optimizes pointer arithmetic that overflows.

**The problem:**

```c
// Common overflow check pattern in kernel memory management
bool check_overflow(char *ptr, size_t offset) {
    return ptr + offset < ptr;  // Intended: detect wraparound
}
```

Under GCC, this often compiles to an actual comparison. Under Clang 20+,
this optimizes to `return false` because the standard says pointer overflow
is undefined behavior — Clang assumes it cannot happen.

**The fix:**

```c
// CORRECT: Use integer arithmetic for overflow checks
bool check_overflow(char *ptr, size_t offset) {
    return (uintptr_t)ptr + offset < (uintptr_t)ptr;
}

// ALTERNATIVE: Compare offset against available space
bool check_overflow(char *ptr, size_t offset, char *end) {
    return offset < (size_t)(end - ptr);
}
```

**Compiler flags:**

```bash
-fno-strict-overflow    # Makes both signed integer and pointer overflow defined
-fwrapv-pointer         # Only makes pointer overflow defined (Clang 20+)
-fwrapv                 # Only makes signed integer overflow defined (Clang 20+)
```

**Detection:**

```bash
clang -fsanitize=pointer-overflow test.c -o test
./test  # Will report any pointer overflow at runtime
```

**Affected code patterns:**
- Kernel memory allocators checking for address space wraparound
- Ring buffer pointer advancement
- Page table entry arithmetic
- DMA buffer boundary checks
- Any pointer comparison where `ptr + offset` might wrap

### 1.2 Infinite Loop Removal

**Severity: CRITICAL for kernel code**

Clang may remove loops that it determines have no observable side effects,
even when the loop contains atomic operations or is intentionally infinite.

**The problem:**

```c
// Kernel halt function — MUST NOT be optimized away
void halt(void) {
    while (1) {
        __asm__("hlt");  // Clang might remove this loop
    }
}

// Spinlock wait — Clang might remove if it thinks
// the atomic load has no side effect
void spin_wait(atomic_int *lock) {
    while (atomic_load_explicit(lock, memory_order_acquire) != 0) {
        // spin
    }
}
```

**The fix:**

```c
// CORRECT: Use asm volatile with memory clobber
void halt(void) {
    while (1) {
        __asm__ volatile ("hlt" ::: "memory");
    }
}

// CORRECT: Ensure loop body has a volatile side effect
void spin_wait(atomic_int *lock) {
    while (atomic_load_explicit(lock, memory_order_acquire) != 0) {
        __asm__ volatile ("pause" ::: "memory");
    }
}

// CORRECT: Mark the loop variable as volatile
void delay_loop(void) {
    volatile int i;
    for (i = 0; i < 1000000; i++) {
        // Loop body intentionally empty
    }
}
```

**Key rule:** Every infinite loop or busy-wait loop MUST contain either:
1. `asm volatile ("..." ::: "memory")`, or
2. A volatile memory access, or
3. An I/O operation that Clang cannot prove has no side effects

### 1.3 Signed Integer Overflow

**Severity: HIGH**

Both compilers assume signed overflow cannot happen, but Clang tends to
be more aggressive in exploiting this for optimization at higher -O levels.

**The problem:**

```c
// Clang may optimize this to always return true
bool will_overflow(int x) {
    return x + 1 > x;  // UB if x == INT_MAX
}

// Clang may remove the overflow check entirely
int safe_add(int a, int b) {
    int result = a + b;
    if (result < a)  // UB-based check: Clang optimizes to false
        return INT_MAX;
    return result;
}
```

**The fix:**

```c
// CORRECT: Use unsigned arithmetic or __builtin_add_overflow
bool safe_add(int a, int b, int *result) {
    return __builtin_add_overflow(a, b, result);
}

// CORRECT: Cast to unsigned for the check
int safe_add_v2(int a, int b) {
    unsigned ua = (unsigned)a;
    unsigned ub = (unsigned)b;
    unsigned result = ua + ub;
    // Check for signed overflow using unsigned arithmetic
    if ((int)result < 0 && a > 0 && b > 0)
        return INT_MAX;
    return (int)result;
}
```

**Compiler flag:** `-fwrapv` makes signed integer overflow wrap around
(two's complement), disabling UB-based optimizations on signed arithmetic.

### 1.4 Strict Aliasing

**Severity: HIGH for kernel code**

Both compilers optimize based on strict aliasing rules (Type-Based Alias
Analysis — TBAA), but produce different code when aliasing violations occur.

**The problem:**

```c
// STRICT ALIASING VIOLATION: Common in kernel code
uint32_t read_register(void *mmio_base, int offset) {
    // Casting void* → uint32_t* then dereferencing
    // violates strict aliasing if the memory was written
    // through a different pointer type
    return *(uint32_t *)((char *)mmio_base + offset);
}

// STRICT ALIASING VIOLATION: Type punning
float int_bits_to_float(uint32_t bits) {
    return *(float *)&bits;  // UB under strict aliasing
}
```

**The fix:**

```c
// CORRECT: Use memcpy for type punning
float int_bits_to_float(uint32_t bits) {
    float result;
    memcpy(&result, &bits, sizeof(result));
    return result;  // Compiler optimizes memcpy away
}

// CORRECT: Use volatile for MMIO
uint32_t read_register(volatile void *mmio_base, int offset) {
    return *(volatile uint32_t *)((volatile char *)mmio_base + offset);
}

// CORRECT: Use union for type punning (C only, not C++)
float int_bits_to_float_v2(uint32_t bits) {
    union { uint32_t i; float f; } u;
    u.i = bits;
    return u.f;
}
```

**Compiler flag:** `-fno-strict-aliasing` disables TBAA. The Linux kernel
uses this flag globally. **Recommended for all kernel code.**

### 1.5 Null Pointer Dereference Optimization

**Severity: HIGH**

Clang may remove null checks if it can prove the pointer was already
dereferenced earlier (since dereferencing null is UB, the pointer must
not be null).

**The problem:**

```c
void process(struct device *dev) {
    int type = dev->type;      // Dereferences dev
    if (dev == NULL)           // Clang may remove this
        return;                // because dev was already
    do_something(dev, type);   // dereferenced above
}
```

**The fix:**

```c
// CORRECT: Check for null BEFORE dereferencing
void process(struct device *dev) {
    if (dev == NULL)
        return;
    int type = dev->type;
    do_something(dev, type);
}
```

**Compiler flag:** `-fno-delete-null-pointer-checks` prevents the
compiler from removing null checks based on prior dereferences.
**Recommended for kernel code.**

---

## 2. Inline Assembly Differences

### 2.1 Constraint Syntax Divergences

Clang supports most GCC inline asm constraints but has subtle differences.

**Input/Output constraint mismatches:**

```c
// GCC accepts this; Clang may reject or miscompile
uint64_t rdtsc(void) {
    uint32_t lo, hi;
    __asm__ volatile (
        "rdtsc"
        : "=a"(lo), "=d"(hi)  // OK in both
    );
    return ((uint64_t)hi << 32) | lo;
}

// ISSUE: Complex constraint alternatives
// GCC supports "rm" meaning "register or memory"
// Clang handles it but may choose differently
__asm__ volatile (
    "mov %1, %0"
    : "=rm"(output)   // register-or-memory output
    : "ri"(input)      // register-or-immediate input
);
```

**Named operands (recommended for clarity):**

```c
// BEST PRACTICE: Use named operands for portability
__asm__ volatile (
    "outb %[data], %[port]"
    : /* no outputs */
    : [data] "a"(data),
      [port] "Nd"(port)
    : "memory"
);
```

### 2.2 asm goto

**Status:** Supported by Clang since version 9, but with more edge cases
than GCC's implementation.

```c
// asm goto — jumps from assembly to a C label
// Works in both, but Clang has had bugs with:
//   - Exception handling interaction
//   - Optimization of code after the asm goto
//   - Register allocation near the jump target

__asm__ goto (
    "cmpb $0, %0\n\t"
    "je %l[is_zero]"
    : /* no outputs */
    : "m"(value)
    : "cc"
    : is_zero          // Label operand
);
// Fall through: value is non-zero
do_nonzero();
return;

is_zero:
    do_zero();
    return;
```

**Recommendation for TerranoxOS:** Avoid `asm goto` unless necessary.
Use regular inline asm with condition codes returned as output operands
instead. This is more portable and less prone to compiler bugs.

### 2.3 Memory Clobber Behavior

**Key difference:** Clang may be more aggressive about reordering memory
accesses around inline asm blocks that don't have the `"memory"` clobber.

```c
// WRONG: Missing memory clobber
// Clang may reorder the store past the asm
*mmio_reg = command;
__asm__ volatile ("" : : : /* no clobbers */);
while (*mmio_reg & BUSY_BIT) { }

// CORRECT: Memory clobber prevents reordering
*mmio_reg = command;
__asm__ volatile ("" : : : "memory");
while (*mmio_reg & BUSY_BIT) {
    __asm__ volatile ("pause" : : : "memory");
}
```

**Rule:** When in doubt, add `"memory"` to the clobber list. The cost
is that the compiler must reload memory values after the asm block, but
for kernel code correctness is more important than avoiding a few reloads.

### 2.4 Intel vs AT&T Syntax

Clang supports both AT&T (default) and Intel syntax for inline asm.

```c
// AT&T syntax (default for both GCC and Clang)
__asm__ volatile ("movl $0x80000000, %%eax" : : : "eax");

// Intel syntax (supported by both, but less tested in Clang)
__asm__ volatile (".intel_syntax noprefix\n\t"
                  "mov eax, 0x80000000\n\t"
                  ".att_syntax prefix"
                  : : : "eax");
```

**Recommendation:** Use AT&T syntax consistently. It's the default for
both compilers and better tested. If you prefer Intel syntax, use
`-masm=intel` globally rather than mixing syntax in inline asm blocks.

---

## 3. ABI and Calling Convention Edge Cases

### 3.1 Small Struct Passing (x86_64)

**Severity: HIGH at FFI boundaries**

```c
struct small_8bytes {
    uint32_t a;
    uint32_t b;
};

// GCC and Clang may pass this struct differently:
// - In registers vs on the stack
// - As one 64-bit register vs two 32-bit halves
void func(struct small_8bytes s);
```

**When this matters:**
- C ↔ Ada FFI boundary (TerranoxOS Ada modules)
- C ↔ Rust FFI boundary
- C ↔ handwritten assembly boundary
- Objects compiled by different compiler versions linked together

**The fix:**

```c
// At FFI boundaries, pass by pointer instead of by value
void func(const struct small_8bytes *s);

// Or use explicit attribute for calling convention
void func(struct small_8bytes s) __attribute__((sysv_abi));
```

**Testing:**

```c
// Compile with both compilers and compare:
// clang -S -O2 test.c -o test_clang.s
// gcc -S -O2 test.c -o test_gcc.s
// diff test_clang.s test_gcc.s
// Look for differences in how structs are passed to functions
```

### 3.2 Stack Alignment

**x86_64 ABI requires 16-byte stack alignment on function entry.**

Clang enforces this more strictly. Interrupt handlers are the main
problem because the CPU pushes an odd number of values before calling
the handler, potentially misaligning the stack.

```c
// PROBLEM: ISR has misaligned stack
__attribute__((interrupt))
void isr_handler(struct interrupt_frame *frame) {
    // Stack may be 8-byte aligned, not 16-byte
    // Clang may generate SSE instructions that fault on misaligned stack
    some_function();  // May crash with #GP if stack is misaligned
}
```

**The fix:**

```nasm
; Assembly ISR stub — manually align stack before calling C
isr_stub_common:
    push rax            ; Save registers
    push rcx
    push rdx
    ; ... save all caller-saved registers ...

    mov  rdi, rsp       ; Pass frame pointer as argument
    and  rsp, ~0xF      ; Align stack to 16 bytes
    sub  rsp, 8         ; Maintain alignment after call pushes return address

    call isr_handler_c  ; Call C handler

    ; ... restore registers ...
    iretq
```

**Compiler flags:**
```bash
-mno-sse              # Prevents SSE instructions that require alignment
-mno-sse2             # (always use for kernel code)
-mstackrealign        # Realign stack on function entry (expensive)
```

### 3.3 __int128 Handling

```c
// __int128 is supported by both GCC and Clang on x86_64
// but there are edge cases in how it's returned from functions
// and passed as arguments

__int128 multiply(uint64_t a, uint64_t b) {
    return (__int128)a * b;
}

// Clang and GCC may use different registers (rdx:rax vs memory)
// for returning this value depending on the calling context
```

**Recommendation:** Use `__int128` freely in self-contained code, but
at ABI boundaries (FFI, syscall interface) pass the halves as two
separate `uint64_t` values.

### 3.4 Alignment of Types

GCC and Clang may disagree on the alignment of certain types,
particularly long double and large structs.

```c
// Check alignment assumptions explicitly
_Static_assert(_Alignof(long double) == 16,
               "long double alignment mismatch");
_Static_assert(sizeof(long double) == 16,
               "long double size mismatch");

// For structs at ABI boundaries, always use explicit alignment
struct __attribute__((aligned(16))) sse_state {
    uint8_t data[512];
};
```

---

## 4. compiler-rt vs libgcc

### 4.1 Missing Builtins

compiler-rt provides most libgcc builtins, but there are gaps.

**Known missing or different builtins on x86_64:**

| Builtin | libgcc | compiler-rt | Notes |
|---------|--------|-------------|-------|
| `__divti3` | Yes | Yes | 128-bit division |
| `__modti3` | Yes | Yes | 128-bit modulo |
| `__fixunsdfti` | Yes | Yes | double → uint128 |
| `__gcc_personality_v0` | Yes | **No** | C++ EH personality |
| `__register_frame` | Yes | Partial | DWARF frame registration |
| `_Unwind_Resume` | In libgcc_s | In libunwind | Different library |
| `__cxa_*` | In libstdc++ | In libc++abi | Different library |

**Testing for missing builtins:**

```bash
# Compile with compiler-rt and check for undefined symbols
clang --rtlib=compiler-rt -c test.c -o test.o
llvm-nm -u test.o  # List undefined symbols

# Link and check
clang --rtlib=compiler-rt --unwindlib=libunwind test.o -o test
# Any "undefined reference" errors indicate missing builtins
```

### 4.2 libgcc_s Compatibility Shim

Many packages and libraries explicitly link against `-lgcc_s`.
Create a linker script shim in your sysroot:

```bash
# Create shim: ${SYSROOT}/usr/lib/libgcc_s.so
echo 'INPUT(-lunwind -lclang_rt.builtins)' > ${SYSROOT}/usr/lib/libgcc_s.so

# Create static shim too
echo 'INPUT(-lunwind -lclang_rt.builtins)' > ${SYSROOT}/usr/lib/libgcc_s.a

# Some packages look for libgcc.a
echo 'INPUT(-lclang_rt.builtins)' > ${SYSROOT}/usr/lib/libgcc.a

# Some packages look for libgcc_eh.a (exception handling)
echo 'INPUT(-lunwind)' > ${SYSROOT}/usr/lib/libgcc_eh.a
```

### 4.3 CRT Files (crtbegin.o, crtend.o)

GCC provides `crtbegin.o`, `crtbeginS.o`, `crtbeginT.o`, `crtend.o`,
`crtendS.o`. These handle constructor/destructor sections (.ctors/.dtors
and .init_array/.fini_array).

**For a pure LLVM toolchain:**

```bash
# Option A: Build compiler-rt with CRT support
cmake ... \
    -DCOMPILER_RT_BUILD_CRT=ON \
    ...

# Verify CRT files exist after build:
ls $(clang --print-resource-dir)/lib/linux/clang_rt.crtbegin-x86_64.o
ls $(clang --print-resource-dir)/lib/linux/clang_rt.crtend-x86_64.o
```

```bash
# Option B: Symlink to make them discoverable
RTLIB=$(clang --print-resource-dir)/lib/linux
ln -sf ${RTLIB}/clang_rt.crtbegin-x86_64.o ${SYSROOT}/usr/lib/crtbegin.o
ln -sf ${RTLIB}/clang_rt.crtbegin-x86_64.o ${SYSROOT}/usr/lib/crtbeginS.o
ln -sf ${RTLIB}/clang_rt.crtbegin-x86_64.o ${SYSROOT}/usr/lib/crtbeginT.o
ln -sf ${RTLIB}/clang_rt.crtend-x86_64.o   ${SYSROOT}/usr/lib/crtend.o
ln -sf ${RTLIB}/clang_rt.crtend-x86_64.o   ${SYSROOT}/usr/lib/crtendS.o
```

**musl provides these CRT files:**

| File | Provider | Purpose |
|------|----------|---------|
| `crt1.o` | musl | Entry point (`_start`) |
| `Scrt1.o` | musl | PIC entry point |
| `rcrt1.o` | musl | Static PIE entry point |
| `crti.o` | musl | `.init` section prologue |
| `crtn.o` | musl | `.init` section epilogue |
| `crtbegin.o` | compiler-rt | `.ctors`/`.init_array` handling |
| `crtend.o` | compiler-rt | `.dtors`/`.fini_array` handling |

### 4.4 Exception Handling ABI Mismatch

```c
// If you encounter "undefined reference to __gcc_personality_v0":
// A library was compiled expecting GCC's exception handling.
// Options:

// 1. Rebuild the library with Clang (preferred)
// 2. Provide a shim:
void *__gcc_personality_v0 = 0;  // Stub — disables C++ exceptions

// 3. For C-only code, compile with -fno-exceptions
```

---

## 5. Preprocessor and Language Extension Differences

### 5.1 inline Keyword Semantics

```c
// In C (not C++), the meaning of "inline" differs between
// GNU C89 mode (old GCC default) and C99/C17 (Clang default)

inline int add(int a, int b) {
    return a + b;
}
// C99/C17 (Clang default): This is ONLY an inline hint.
//   There must be a non-inline definition elsewhere.
//   Without it: "undefined reference to add"
//
// GNU C89 (old GCC default): This provides the definition.

// FIX: Use "static inline" for header-file functions
static inline int add(int a, int b) {
    return a + b;
}
// This works identically in all modes and both compilers.
```

**Compiler flag:** `-fgnu89-inline` reverts to GNU C89 inline behavior.

### 5.2 __GNUC__ Macro and Feature Detection

```c
// Clang defines __GNUC__ for compatibility, but it does NOT
// mean all GCC extensions are available

#if defined(__GNUC__) && !defined(__clang__)
    // GCC-only code path
    #define HAVE_NESTED_FUNCTIONS 1
#elif defined(__clang__)
    // Clang code path
    #define HAVE_NESTED_FUNCTIONS 0
#endif

// BETTER: Use feature-checking macros
#if __has_attribute(noinline)
    #define NOINLINE __attribute__((noinline))
#else
    #define NOINLINE
#endif

#if __has_builtin(__builtin_expect)
    #define likely(x)   __builtin_expect(!!(x), 1)
    #define unlikely(x) __builtin_expect(!!(x), 0)
#else
    #define likely(x)   (x)
    #define unlikely(x) (x)
#endif
```

### 5.3 Unsupported GCC Extensions

**Extensions supported by GCC but NOT by Clang:**

| Extension | GCC | Clang | Workaround |
|-----------|-----|-------|------------|
| Nested functions | Yes | **No** | Refactor to separate functions |
| `__builtin_ia32_*` | Yes | **No** | Use `*mmintrin.h` headers |
| `#pragma GCC optimize` | Yes | Ignored | Use `__attribute__((optimize))` or per-file flags |
| Labels as values (`&&label`) | Yes | Yes | Both support this |
| Statement expressions `({...})` | Yes | Yes | Both support this |
| `typeof` | Yes | Yes | Both support this |
| Zero-length arrays | Yes | Yes | Both support this |
| `__attribute__((cleanup))` | Yes | Yes | Both support this |
| `__attribute__((section))` | Yes | Yes | Both support this |
| VLAs in C++ | Yes (ext) | Warning | Use `std::vector` or `-Wno-vla` |
| `case` ranges `0 ... 255:` | Yes | Yes | Both support this |
| `__builtin_expect_with_probability` | Yes | Yes (14+) | Use `__builtin_expect` as fallback |

### 5.4 Diagnostic Pragma Differences

```c
// GCC allows "unpaired" diagnostic pop that resets to command-line state
// Clang does NOT support this — pop without push is an error

// WRONG (works in GCC, undefined in Clang):
#pragma GCC diagnostic ignored "-Wunused-variable"
// ... code ...
#pragma GCC diagnostic pop  // No matching push!

// CORRECT:
#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wunused-variable"
// ... code ...
#pragma GCC diagnostic pop
```

---

## 6. Linker (lld vs GNU ld) Edge Cases

### 6.1 Linker Script Compatibility

```ld
/* lld handles most GNU ld linker script features, but with differences: */

SECTIONS {
    /* DIFFERENCE 1: Orphan section placement
     * lld places orphan sections (not in the script) differently than GNU ld.
     * FIX: Explicitly list ALL sections and discard unwanted ones. */

    .text : { *(.text .text.*) }
    .rodata : { *(.rodata .rodata.*) }
    .data : { *(.data .data.*) }
    .bss : { *(.bss .bss.* COMMON) }

    /* DIFFERENCE 2: SORT behavior
     * lld's SORT_BY_NAME may produce different ordering than GNU ld
     * for sections with complex names. Test by comparing section layouts. */
    .init_array : {
        PROVIDE_HIDDEN(__init_array_start = .);
        KEEP(*(SORT_BY_INIT_PRIORITY(.init_array.*)))
        KEEP(*(.init_array))
        PROVIDE_HIDDEN(__init_array_end = .);
    }

    /* DIFFERENCE 3: MEMORY regions
     * lld supports MEMORY but has had bugs with complex overlapping regions.
     * For kernel code, explicit address assignment is safer. */

    /* CRITICAL: Discard everything not explicitly listed */
    /DISCARD/ : {
        *(.gcc_except_table)
        *(.eh_frame)
        *(.note.GNU-stack)
        *(.note.gnu.build-id)
        *(.comment)
    }
}
```

### 6.2 --gc-sections Behavior

```bash
# lld is more aggressive about garbage collecting unused sections.
# This can remove kernel init code that's only referenced through
# tables generated at link time.

# FIX: Use KEEP() in linker scripts for sections that must not be GC'd
# KEEP(*(.init_array))
# KEEP(*(.text.startup))

# Or mark individual functions/data:
__attribute__((used))
void init_function(void) { ... }

__attribute__((retain))  # Clang 13+
static int important_data = 42;
```

### 6.3 Symbol Handling Differences

```bash
# lld treats weak symbols differently in some edge cases
# GCC's linker prefers the strong definition when both exist;
# lld may not always do this in the same way with archives (.a files)

# FIX: If you have weak/strong symbol pairs, make sure the
# strong definition is in an object file linked BEFORE the archive
# containing the weak definition.

# Example link order:
ld.lld -o kernel \
    start.o \          # Contains strong _start
    kernel_main.o \    # Contains strong kmain
    -lkernel_utils \   # Archive with weak fallback symbols
    # Order matters more with lld than with GNU ld
```

### 6.4 Relocation Differences

```bash
# lld and GNU ld handle some relocation types differently.
# Most common issue: R_X86_64_PLT32 vs R_X86_64_PC32

# For kernel code (no PLT), you may see:
# "relocation R_X86_64_PLT32 cannot be used; recompile with -fPIC"

# FIX: For kernel code, use:
-fno-pic -fno-pie     # Disable PIC/PIE
-mcmodel=kernel        # Use kernel memory model

# For static executables:
-static -no-pie
```

---

## 7. Optimization Divergences

### 7.1 Loop Optimization

```c
// Clang and GCC use different heuristics for:
// - Loop unrolling depth
// - Vectorization decisions
// - Loop rotation
// - LICM (Loop-Invariant Code Motion)

// If loop behavior changes between compilers, use:
#pragma clang loop unroll_count(4)        // Clang-specific
// or
__attribute__((optimize("no-unroll-loops")))  // GCC-specific

// Portable: Use volatile or memory barriers to prevent
// unwanted loop optimizations
```

### 7.2 Constant Folding Aggressiveness

```c
// Clang folds constants more aggressively, which can eliminate
// code paths you expected to exist

// PROBLEM: Debug check gets optimized away
void safe_divide(int a, int b) {
    assert(b != 0);       // Clang may prove b != 0 and remove this
    return a / b;
}

// FIX: Use volatile for debug-only checks that must execute
void safe_divide_v2(int a, int b) {
    volatile int check = b;
    assert(check != 0);
    return a / b;
}
```

### 7.3 LTO (Link-Time Optimization) Differences

```bash
# Clang LTO
clang -flto=thin  ...   # ThinLTO: faster, parallel, less memory
clang -flto=full  ...   # Full LTO: better optimization, more memory

# Key differences from GCC LTO:
# 1. Clang ThinLTO makes different inlining decisions
# 2. Symbol visibility may change (use -fvisibility=hidden explicitly)
# 3. Whole-program devirtualization behavior differs
# 4. LTO can expose UB that was hidden by separate compilation

# For kernel builds, LTO may reveal bugs where code relies on
# separate compilation preventing certain optimizations.
# Always test with LTO off first, then enable incrementally.
```

### 7.4 -O2 vs -Os vs -Oz

```bash
# Clang has -Oz (optimize for size, more aggressive than -Os)
# GCC does not have -Oz

# Clang -Os tends to produce smaller code than GCC -Os
# Clang -O2 tends to produce faster code than GCC -O2 for some workloads
# but GCC -O2 is sometimes faster for others (particularly integer-heavy
# loops and some auto-vectorization patterns)

# For kernel code, recommended:
-O2                     # Best balance of speed/size
-Os                     # If code size is critical
# Avoid -O3 for kernel code — too aggressive, increases code size,
# and may trigger optimizer bugs in edge cases
```

---

## 8. musl-Specific Edge Cases

### 8.1 Dynamic Linker Path

```bash
# glibc: /lib64/ld-linux-x86-64.so.2
# musl:  /lib/ld-musl-x86_64.so.1

# Clang must know which to use.
# Check what Clang uses:
clang --target=x86_64-linux-musl -### test.c 2>&1 | grep dynamic-linker

# If it shows the glibc path, your target triple is wrong.
# FIX: Ensure --target=x86_64-linux-musl (not x86_64-linux-gnu)
```

### 8.2 Header Compatibility

```c
// musl headers differ from glibc headers in several ways:

// 1. No __GLIBC__ macro
#ifdef __GLIBC__
    // This code path is NEVER taken with musl
    #include <sys/cdefs.h>
#endif

// 2. Missing glibc extensions
// musl does not provide:
//   - <sys/cdefs.h> (some features)
//   - <error.h> (use err.h instead)
//   - <execinfo.h> (no backtrace())
//   - features_t in <features.h>

// 3. Different POSIX compliance level
// musl strictly follows POSIX; glibc adds many non-standard extensions.
// Code using _GNU_SOURCE features may need alternatives.
```

### 8.3 Stack Protector Symbols

```bash
# musl provides __stack_chk_fail (needed for -fstack-protector)
# but the implementation may differ from glibc

# For kernel code: you typically provide your own __stack_chk_fail
# that calls your panic handler

# Verify:
nm ${SYSROOT}/usr/lib/libc.a | grep stack_chk
```

### 8.4 Thread-Local Storage (TLS)

```c
// musl uses a different TLS model than glibc.
// For kernel code, TLS is typically not available.
// For userspace: musl supports __thread and _Thread_local

// Edge case: musl's TLS initialization order differs from glibc.
// Static TLS (initial-exec model) has a smaller limit in musl.
// Large TLS usage may fail at runtime with musl.
```

---

## 9. Build System Edge Cases

### 9.1 Autoconf / configure Scripts

```bash
# Many configure scripts assume GCC and break with Clang.
# Common issues and fixes:

# ISSUE: "checking whether the C compiler works... no"
# CAUSE: Cross-compilation check tries to run the binary
# FIX: Always pass --host and --build:
./configure \
    --host=x86_64-linux-musl \
    --build=$(cc -dumpmachine) \
    CC="clang --target=x86_64-linux-musl --sysroot=${SYSROOT}" \
    CXX="clang++ --target=x86_64-linux-musl --sysroot=${SYSROOT}" \
    LD="ld.lld" \
    AR="llvm-ar" \
    NM="llvm-nm" \
    RANLIB="llvm-ranlib" \
    STRIP="llvm-strip" \
    OBJCOPY="llvm-objcopy" \
    READELF="llvm-readelf"

# ISSUE: Script checks for gcc-specific flags
# CAUSE: configure tests "does the compiler accept -Wfoo" by compiling
#        with -Werror, and Clang warns about unknown GCC flags
# FIX: Set CFLAGS to include -Wno-unknown-warning-option:
CFLAGS="-Wno-unknown-warning-option" ./configure ...

# ISSUE: Script uses `$CC -v` output to detect compiler type
# CAUSE: Clang's -v output differs from GCC's
# FIX: Usually harmless, but if configure branches on "gcc" in output,
#      you may need to patch the configure script
```

### 9.2 CMake Projects

```cmake
# CMake detects Clang as "Clang", not "GNU"
# Projects that gate features on compiler ID may skip needed flags

# ISSUE: Project does this:
if(CMAKE_C_COMPILER_ID STREQUAL "GNU")
    add_compile_options(-Wall -Wextra)
endif()
# Clang will not get these flags

# FIX: When building packages, override:
cmake ... \
    -DCMAKE_C_FLAGS="-Wall -Wextra" \
    -DCMAKE_C_COMPILER_WORKS=YES  # Skip compiler check for cross-compile
```

### 9.3 Makefile Defaults

```bash
# GNU make uses CC=cc by default
# If cc → gcc on your host, cross-compiled packages may use wrong compiler

# ALWAYS set these explicitly before building ANY package:
export CC="clang --target=${TARGET} --sysroot=${SYSROOT}"
export CXX="clang++ --target=${TARGET} --sysroot=${SYSROOT}"
export LD="ld.lld"
export AR="llvm-ar"
export NM="llvm-nm"
export RANLIB="llvm-ranlib"
export STRIP="llvm-strip"
export OBJCOPY="llvm-objcopy"
export OBJDUMP="llvm-objdump"
export READELF="llvm-readelf"
export SIZE="llvm-size"
export STRINGS="llvm-strings"

# Some Makefiles also check:
export AS="clang"     # Clang includes an integrated assembler
export CPP="clang -E" # Preprocessor
```

### 9.4 pkg-config Contamination

```bash
# After building a package, check its .pc file for GCC references

# PROBLEM: /usr/lib/pkgconfig/foo.pc contains:
# Libs: -lfoo -lgcc_s -lgcc

# FIX: Sed out GCC references:
sed -i 's/-lgcc_s//g; s/-lgcc //g' ${SYSROOT}/usr/lib/pkgconfig/*.pc

# Or better: rebuild the package correctly so it doesn't reference GCC
```

---

## 10. Detection and Diagnostic Tools

### 10.1 Sanitizers

```bash
# UndefinedBehaviorSanitizer — catches UB at runtime
clang -fsanitize=undefined \
      -fno-sanitize-recover=all \  # Abort on first UB
      test.c -o test
./test

# Specific UB checks:
-fsanitize=signed-integer-overflow    # Signed int overflow
-fsanitize=pointer-overflow           # Pointer overflow
-fsanitize=shift                      # Invalid shifts
-fsanitize=null                       # Null dereference
-fsanitize=alignment                  # Misaligned access
-fsanitize=bounds                     # Array bounds

# AddressSanitizer — catches memory errors
clang -fsanitize=address test.c -o test

# MemorySanitizer — catches uninitialized reads
clang -fsanitize=memory test.c -o test

# ThreadSanitizer — catches data races
clang -fsanitize=thread test.c -o test

# NOTE: Sanitizers require runtime support from compiler-rt.
# They work for userspace code but NOT for kernel code
# (kernel has no runtime support for sanitizer callbacks).
```

### 10.2 Codegen Comparison

```bash
# Compare assembly output between optimization levels
clang -O0 -S test.c -o test_O0.s
clang -O2 -S test.c -o test_O2.s
diff test_O0.s test_O2.s
# Large unexpected differences may indicate UB exploitation

# Compare between Clang and GCC (if GCC is available)
clang -O2 -S test.c -o test_clang.s
gcc   -O2 -S test.c -o test_gcc.s
diff test_clang.s test_gcc.s
# Behavioral differences indicate code relying on implementation-defined
# or undefined behavior
```

### 10.3 Dependency Auditing

```bash
# Verify no GCC runtime dependencies in your binaries
ldd ./binary 2>/dev/null | grep -E "libgcc|libstdc\+\+"
# Should return NOTHING for a pure LLVM toolchain

# Check which compiler produced an object
llvm-readelf -p .comment ./binary
# Should show "clang" not "GCC"

# Check for unresolved symbols
llvm-nm -u ./binary
# Should be empty for static binaries

# Verify dynamic linker is musl, not glibc
readelf -l ./binary | grep interpreter
# Should show: /lib/ld-musl-x86_64.so.1
# NOT: /lib64/ld-linux-x86-64.so.2

# Check all shared library dependencies recursively
LD_LIBRARY_PATH=${SYSROOT}/usr/lib ldd ./binary
```

### 10.4 LLVM IR Analysis

```bash
# Emit LLVM IR to inspect what the compiler is actually doing
clang -O2 -emit-llvm -S test.c -o test.ll

# Look for:
# - "unreachable" instructions (compiler determined UB path)
# - Missing function calls (optimized away)
# - "nsw" / "nuw" flags on arithmetic (no-signed-wrap / no-unsigned-wrap)
#   These indicate the optimizer is assuming no overflow

# Example: search for UB-exploiting optimizations
grep -n "unreachable" test.ll
grep -n "nsw" test.ll
```

---

## 11. Recommended Compiler Flags

### 11.1 Kernel Code

```bash
KERNEL_CFLAGS="\
    -ffreestanding \
    -fno-builtin \
    -nostdlib \
    -nostdinc \
    -fno-strict-aliasing \
    -fno-delete-null-pointer-checks \
    -fno-strict-overflow \
    -fno-stack-protector \
    -mno-red-zone \
    -mno-sse \
    -mno-sse2 \
    -mno-mmx \
    -mno-80387 \
    -mcmodel=kernel \
    -fno-pic \
    -fno-pie \
    -fno-common \
    -ffunction-sections \
    -fdata-sections \
    -Wall \
    -Wextra \
    -Werror \
    -std=c17 \
    -O2 \
"

KERNEL_LDFLAGS="\
    -nostdlib \
    -static \
    --gc-sections \
    -z max-page-size=4096 \
"
```

### 11.2 Userspace Code (Cross-Compilation)

```bash
USER_CFLAGS="\
    --target=x86_64-linux-musl \
    --sysroot=${SYSROOT} \
    --rtlib=compiler-rt \
    --unwindlib=libunwind \
    -stdlib=libc++ \
    -fuse-ld=lld \
    -Wall \
    -Wextra \
    -O2 \
"
```

### 11.3 Debug Build

```bash
DEBUG_CFLAGS="\
    -O0 \
    -g3 \
    -ggdb \
    -fno-omit-frame-pointer \
    -fno-optimize-sibling-calls \
    -fsanitize=undefined \
    -fsanitize=address \
"
```

---

## 12. Package-Specific Fixes

### Packages Known to Require Patches for Clang + musl

| Package | Issue | Fix |
|---------|-------|-----|
| coreutils | GNU-specific extensions | Chimera Linux patches |
| gawk | Nested functions | Refactor or use mawk |
| grub | Assumes GCC for `-Wno-*` flags | `-Wno-unknown-warning-option` |
| iproute2 | GCC-specific `__attribute__` | Chimera Linux patches |
| util-linux | Glibc-specific headers | musl compatibility patches |
| procps-ng | `/proc` parsing assumes glibc | Minor patches needed |
| shadow (passwd) | Glibc-specific `<shadow.h>` | musl provides subset |
| e2fsprogs | Assumes GNU ld for linker scripts | Usually works with lld |
| kmod | Glibc-specific features | Minor patches |
| eudev | Glibc-specific headers | Use upstream musl patches |
| systemd | Heavy glibc dependency | Use alternative init (s6, dinit, OpenRC) |
| bash | Minor Clang warnings | `-Wno-*` flags usually sufficient |
| toybox | Clean Clang support | Works out of the box |
| busybox | Mostly Clang compatible | Minor flag adjustments |
| zlib-ng | Clean CMake build | Works out of the box |
| Linux kernel | `make LLVM=1` | Well supported upstream |

### Primary Patch Sources

1. **Chimera Linux** (https://github.com/chimera-linux) — Production
   Clang+musl distro; patches for hundreds of packages
2. **Alpine Linux** (https://gitlab.alpinelinux.org) — musl patches
   (still uses GCC, but musl compatibility patches apply)
3. **Void Linux musl** (https://github.com/void-linux) — musl variant
   patches and workarounds
4. **CMLFS** (https://github.com/dslm4515/CMLFS) — Clang+musl LFS
   build notes and patches

---

## Quick Reference Card

```
┌─────────────────────────────────────────────────────────────┐
│  LLVM/Clang Edge Case Quick Reference                       │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  CRITICAL FLAGS FOR KERNEL CODE:                            │
│    -fno-strict-aliasing                                     │
│    -fno-delete-null-pointer-checks                          │
│    -fno-strict-overflow  (or -fwrapv -fwrapv-pointer)       │
│    -mno-red-zone                                            │
│    -mno-sse -mno-sse2                                       │
│    -mcmodel=kernel                                          │
│                                                             │
│  COMPATIBILITY SHIMS IN SYSROOT:                            │
│    libgcc_s.so → INPUT(-lunwind -lclang_rt.builtins)        │
│    libgcc.a   → INPUT(-lclang_rt.builtins)                  │
│    libgcc_eh.a → INPUT(-lunwind)                            │
│                                                             │
│  EVERY INLINE ASM BLOCK MUST HAVE:                          │
│    volatile keyword                                         │
│    "memory" clobber (when touching shared state)            │
│                                                             │
│  AT FFI BOUNDARIES:                                         │
│    Pass structs by pointer, not value                       │
│    Align stack to 16 bytes in ISR stubs                     │
│    Use explicit calling convention attributes                │
│                                                             │
│  BEFORE BUILDING ANY PACKAGE:                               │
│    Export CC, CXX, LD, AR, NM, RANLIB, STRIP, OBJCOPY      │
│    Check .pc files for -lgcc references after install       │
│    Verify: ldd binary | grep gcc  (should return nothing)   │
│                                                             │
│  DETECTING PROBLEMS:                                        │
│    -fsanitize=undefined         (UB detection)              │
│    -fsanitize=pointer-overflow  (pointer wrap)              │
│    clang -emit-llvm -S          (inspect IR for UB)         │
│    llvm-readelf -p .comment     (verify compiler identity)  │
│    ldd binary                   (verify no gcc deps)        │
│                                                             │
│  PATCH SOURCES:                                             │
│    Chimera Linux — Clang+musl production patches            │
│    Alpine Linux — musl compatibility patches                │
│    CMLFS — Clang LFS build notes                            │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```