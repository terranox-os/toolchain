# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

TerranoxOS Toolchain — the repository for **all** TerranoxOS toolchains, including bleeding-edge ones like Ada/GNAT LLVM. Builds pure LLVM/Clang toolchains targeting the custom `x86_64-pc-terranox` triple. The entire pipeline is written in Go using Dagger, which requires a container runtime (Docker or similar) — there is no GitHub Actions CI yet. Toolchains are built, tested, and released locally via Dagger.

## Build Commands

Requires [Dagger](https://dagger.io/) CLI installed.

```bash
# Validate seed toolchain (runs all 8 tests)
dagger call validate

# Full musl bootstrap
dagger call bootstrap --llvm-version=21.1.8

# Full glibc bootstrap
dagger call glibc-bootstrap --llvm-version=21.1.8 --glibc-version=2.39

# Individual stages (musl)
dagger call stage-0 --llvm-version=21.1.8 --source=.
dagger call sysroot --llvm-version=21.1.8 --source=.
dagger call stage-1 --llvm-version=21.1.8 --source=.

# Individual stages (glibc)
dagger call glibc-stage-0 --llvm-version=21.1.8 --source=.
dagger call glibc-sysroot --llvm-version=21.1.8 --glibc-version=2.39 --source=.
dagger call glibc-stage-1 --llvm-version=21.1.8 --glibc-version=2.39 --source=.

# Export toolchain to host
dagger call export --llvm-version=21.1.8 --output=./out/musl

# Run single test
dagger call test-basic
dagger call test-target
dagger call test-linker
dagger call test-tools
dagger call test-compiler-rt
dagger call test-libcxx
dagger call test-freestanding
dagger call test-musl

# Matrix test (all supported LLVM versions)
dagger call matrix

# List all available functions
dagger functions
```

There is no Makefile, no `go test`, no linter, and no GitHub Actions CI. All build and test logic runs through Dagger functions, which require a container runtime (Docker, Podman, etc.) on the host.

## Architecture

All source lives in `.dagger/` as a single Go Dagger module (`dagger/terranox-bootstrap`). The module struct is `TerranoxBootstrap` — every public method becomes a `dagger call` function.

### 3-Stage Bootstrap Pipeline

Both musl and glibc variants follow the same pattern:

1. **Stage 0** — Cross-compiler built with the Wolfi seed Clang (v19). Produces a Clang that targets `x86_64-linux-musl` or `x86_64-pc-terranox-gnu`.
2. **Sysroot** — C library (musl or glibc) + LLVM runtimes (compiler-rt, libunwind, libc++abi, libc++) built with Stage 0.
3. **Stage 1** — Self-hosted compiler built with Stage 0 and linked against the sysroot. This is the final toolchain.

### Source Files

| File | Purpose |
|------|---------|
| `main.go` | Core pipeline: base container, source fetching, stage 0/1, sysroot, patching, validation, matrix, export |
| `toolchain_musl.go` | Musl-variant specific stage and sysroot functions |
| `toolchain_glibc.go` | Glibc-variant specific stage and sysroot functions |
| `build_system.go` | Kernel builds (TerranoxOS + Linux), bootable images, melange package building, strata |
| `security.go` | SBOM generation (CycloneDX 1.5), cosign signing, SLSA provenance |
| `testing.go` | ISO building (Limine bootloader), QEMU boot tests, reproducibility tests |
| `rootfs.go` | Root filesystem assembly, initramfs, strata building |
| `gnat_verify.go` | Ada/GNAT LLVM compiler support and verification |
| `config.go` | TOML configuration parsing with type-safe Go structs |

### Configuration

`bootstrap.toml` is the central config file. CLI arguments override config values. Defaults are hardcoded as constants in `main.go` if the file is missing.

### LLVM Patches

`patches/llvm/` contains two patches that add `x86_64-pc-terranox` as a recognized OS target:
- Patch 0001: Adds the triple to LLVM's TargetParser
- Patch 0002: Adds `TerranoxOSTargetInfo` to Clang (defines `__terranox__` macro)

These are applied automatically by the Dagger pipeline via `ApplyTerranoxPatches()`.

### Two Variants

- **musl** (`x86_64-linux-musl` / `x86_64-pc-terranox`): Kernel, embedded, security workloads. Alpine 3.19 base for sysroot.
- **glibc** (`x86_64-pc-terranox-gnu`): Desktop, gaming, proprietary software. Wolfi base for sysroot.

### Caching

Dagger provides layer caching. Additionally, ccache volumes are used per stage (`ccache-musl-stage0`, `ccache-glibc-stage0`) with 5GB max and compression enabled.

## Code Conventions

- SPDX license header on every file: `// SPDX-License-Identifier: Apache-2.0`
- Receiver pattern: `func (m *TerranoxBootstrap) FunctionName(ctx context.Context, ...) (ReturnType, error)`
- Dagger parameter annotations: `// +default="..."` and `// +optional` comments above parameters
- Section dividers: `// ═══════════════════════════════════════════════════════════`
- All public functions return `(string, error)` or `(*dagger.Container, error)` / `*dagger.Container`
- Go 1.26, Dagger SDK v0.19.11, Engine v0.20.1
