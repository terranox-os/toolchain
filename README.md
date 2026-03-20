# TerranoxOS Toolchain

Pure LLVM/Clang toolchain for TerranoxOS with the custom `x86_64-pc-terranox` target triple.

## Variants

| Variant | Target Triple | C Library | Use Case |
|---------|--------------|-----------|----------|
| **musl** | `x86_64-pc-terranox` | musl 1.2.5 | Kernel, embedded, security |
| **glibc** | `x86_64-pc-terranox-gnu` | glibc 2.39 | Desktop, gaming, proprietary |

## Build Pipeline (Dagger)

Each variant follows a 3-stage bootstrap:

```
Stage 0  — Cross-compiler (built with seed Clang from Wolfi)
Sysroot  — C library + LLVM runtimes (compiler-rt, libunwind, libc++abi, libc++)
Stage 1  — Self-hosted compiler (built with Stage 0, linked against sysroot)
```

### Quick Start

```bash
# Install Dagger
curl -fsSL https://dl.dagger.io/dagger/install.sh | sh

# Validate seed toolchain (8 tests)
dagger call validate

# Build musl variant
dagger call bootstrap --llvm-version=21.1.8

# Build glibc variant
dagger call glibc-bootstrap --llvm-version=21.1.8 --glibc-version=2.39

# Export to host
dagger call bootstrap --llvm-version=21.1.8 export --path=./out/musl
dagger call glibc-stage-1 --llvm-version=21.1.8 --glibc-version=2.39 --source=. export --path=./out/glibc
```

### Individual Stages

```bash
# musl
dagger call stage-0 --llvm-version=21.1.8 --source=.
dagger call sysroot --llvm-version=21.1.8 --source=.
dagger call stage-1 --llvm-version=21.1.8 --source=.

# glibc
dagger call glibc-stage-0 --llvm-version=21.1.8 --source=.
dagger call glibc-sysroot --llvm-version=21.1.8 --glibc-version=2.39 --source=.
dagger call glibc-stage-1 --llvm-version=21.1.8 --glibc-version=2.39 --source=.
```

## Configuration

Edit `bootstrap.toml` to change versions:

```toml
[llvm]
default_version = "21.1.8"

[musl]
version = "1.2.5"

[glibc]
version = "2.39"
```

## Patches

`patches/llvm/` contains the TerranoxOS triple patches:
- `0001-Add-Terranox-OS-triple-to-LLVM-TargetParser.patch`
- `0002-Add-TerranoxOSTargetInfo-to-Clang.patch`

These add `x86_64-pc-terranox` as a recognized OS target with `__terranox__` defined.

## License

Apache-2.0
