// SPDX-License-Identifier: Apache-2.0
//
// Test program to verify x86_64-pc-terranox triple support
//
// Compile with:
//   clang --target=x86_64-pc-terranox -c test_terranox_triple.c
//
// Should define __terranox__ and NOT __linux__

#include <stddef.h>

#ifdef __terranox__
#define OS_NAME "TerranoxOS"
#elif defined(__linux__)
#error "Should define __terranox__, not __linux__!"
#else
#error "Neither __terranox__ nor __linux__ defined!"
#endif

// Verify we have the version macros
#ifndef __TERRANOX_VERSION__
#error "__TERRANOX_VERSION__ not defined!"
#endif

#ifndef __TERRANOX_PATCHLEVEL__
#error "__TERRANOX_PATCHLEVEL__ not defined!"
#endif

// Simple function to ensure compiler works
const char* get_os_name(void) {
    return OS_NAME;
}

// Verify we're x86_64
#ifndef __x86_64__
#error "Expected __x86_64__ to be defined!"
#endif

// Verify ELF
#ifndef __ELF__
#error "Expected __ELF__ to be defined!"
#endif

// Test that we can use standard types
typedef struct {
    size_t size;
    void *data;
} buffer_t;

buffer_t create_buffer(size_t sz) {
    buffer_t buf = { .size = sz, .data = NULL };
    return buf;
}

// Compile-time assertions (C11)
_Static_assert(__TERRANOX_VERSION__ == 1, "Expected version 1");
_Static_assert(__TERRANOX_PATCHLEVEL__ == 0, "Expected patchlevel 0");
_Static_assert(sizeof(void*) == 8, "Expected 64-bit pointers");

// Success if this compiles without errors
int terranox_triple_test_passed = 1;
