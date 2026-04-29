<!--
SPDX-License-Identifier: CC-BY-4.0

doc-id: TRX-DOC-0200
title: Bootstrap Toolchain Specification
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

# TerranoxOS — Bootstrap and Toolchain Specification

**Version:** 1.0.0
**Author:** Antonette — TerranoxOS Project
**Date:** March 2026
**Reconciles:** Copilot bootstrap spec with existing CLFS_GUIDE,
BOOTSTRAP_DAGGER, BOOTSTRAP_CONFIG, and LLVM_CLANG_EDGE_CASES

---

## Purpose

This specification defines how TerranoxOS builds itself from
nothing, how trust is established, and how the multi-stage
compiler pipeline produces deterministic, verifiable toolchains.

Forge, Core, Strata, and Sentinel all depend on a correct,
deterministic toolchain. This is the most critical specification
in the architecture.

---

## Bootstrap Pipeline Overview

TerranoxOS uses a four-stage bootstrap, each stage reducing
reliance on external binaries and increasing determinism:

```
Stage 0     Stage 1        Stage 2         Stage 3
(trust      (self-hosted)  (reproducible)  (optimized)
 root)

Wolfi seed  →  Cross-      →  Self-hosted  →  Reproduced   →  PGO/LTO
Clang 19       compiler       Clang           independently    production
(prebuilt)     for musl       (links musl,    verified         compiler
               target         libc++,         bit-for-bit
                              compiler-rt)
```

### Mapping to existing Dagger functions

```
Copilot stage   Dagger function             Status
─────────────   ───────────────             ──────
Stage 0 seed    dag.Container().From(wolfi) Exists
Stage 0 build   dagger call stage0          Exists
Sysroot         dagger call sysroot         Exists
Stage 1         dagger call stage1          Exists
Stage 2         dagger call stage2-verify   NEW (v0.2.0)
Stage 3         dagger call stage3-pgo      NEW (v0.3.0)
```

v0.1.0 builds through Stage 1 (self-hosted Clang, zero GNU deps).
Stage 2 (reproducibility verification) comes in v0.2.0.
Stage 3 (PGO/LTO optimization) comes in v0.3.0.

---

## Stage 0: The Trust Root

### What it is

Stage 0 is the minimal set of prebuilt binaries used to
bootstrap the first self-built compiler.

**For musl variant (v0.1.0):** Alpine Linux's Clang package
**For glibc variant (v0.2.0+):** Wolfi Clang package

### Requirements

- **Prebuilt, signed, and auditable.** Alpine packages are
  signed with apk-tools, Wolfi packages include SBOMs and
  Sigstore signatures.
- **Content-addressed and pinned.** The exact package version
  is recorded in the Dagger pipeline:

  ```go
  // musl variant (current):
  From("alpine:latest")
  WithExec([]string{"apk", "add", "--no-cache", "clang", "lld", "llvm"})

  // glibc variant (future):
  From("cgr.dev/chainguard/wolfi-base:latest")
  WithExec([]string{"apk", "add", "clang-19", "lld-19"})
  ```

- **Isolated from the host.** Runs inside a Dagger container
  with no access to host libraries or toolchains.
- **Never modified.** Updates require a new trust root version
  with a new digest.

### Verification

```bash
# Verify the seed compiler works
dagger call validate

# This runs 8 validation tests:
#   1. Basic compilation (hello world)
#   2. Target triple verification (x86_64-pc-terranox)
#   3. Linker test (lld)
#   4. LLVM tools test
#   5. Freestanding mode (kernel compilation)
#   6. Static linking test
#   7. Musl libc compilation
#   8. Kernel-mode compilation (no SSE, red-zone, PIC)
```

### Trust root versioning

The trust root is versioned independently of the bootstrap:

```
musl variant trust roots:
  trust-root-musl-v1: Alpine Clang (current)
  trust-root-musl-v2: Alpine Clang (updated version)

glibc variant trust roots (v0.2.0+):
  trust-root-glibc-v1: Wolfi Clang 19
  trust-root-glibc-v2: Wolfi Clang 21
```

Each trust root version is tracked in git history via
`.dagger/main.go` changes. Changing the trust root is a
security-critical operation that must be documented and announced.

---

## Stage 1: Self-Hosted Compiler

### Purpose

Stage 1 is built using Stage 0 and is the first compiler
produced entirely from TerranoxOS-controlled source.

### Pipeline

This maps directly to the existing Dagger functions:

```
dagger call stage0 --llvm-version=21.1.8
  → Alpine seed Clang (musl) builds a cross-compiler targeting musl
  → Builds in Alpine container with musl-dev, linux-headers
  → Output: /opt/terranox/cross-tools/bin/clang

dagger call sysroot --llvm-version=21.1.8
  → Stage 0 cross-compiler builds:
    1. Linux headers (install only)
    2. Full musl libc
    3. LLVM runtimes (compiler-rt, libunwind, libc++abi, libc++)
    4. libgcc_s.so compatibility shim
  → Output: /opt/terranox/sysroot/

dagger call stage1 --llvm-version=21.1.8
  → Stage 0 compiler builds Stage 1 Clang in Alpine (musl) environment:
    - Links against musl + libc++ + compiler-rt + libunwind
    - ldd shows: ld-musl-x86_64.so.1 only (zero GNU deps)
    - Defaults: --target=x86_64-pc-terranox, lld, compiler-rt
    - Build tools can execute natively (musl environment)
  → Output: /opt/terranox/tools/bin/clang
```

### Properties

- Not yet verified for reproducibility (that's Stage 2).
- Used to build all v0.1.0 packages and the kernel.
- Includes provenance metadata linking back to Stage 0.

### Outputs

```
Stage 1 Clang (with terranox triple)
Stage 1 lld
Stage 1 llvm-ar, llvm-nm, llvm-objdump, etc.
Stage 1 compiler-rt builtins
Stage 1 libunwind
Stage 1 libc++abi + libc++
musl libc (in sysroot)
```

### Provenance record

Each Stage 1 build records:

```json
{
  "stage": 1,
  "trust_root": "trust-root-v1",
  "seed_digest": "sha256:a1b2c3d4...",
  "llvm_version": "21.0.0",
  "llvm_commit": "abc123def456...",
  "musl_version": "1.2.5",
  "build_flags": "see bootstrap.toml",
  "terranox_patches": "patches/llvm-terranox-triple.patch",
  "output_digest": "BLAKE3:x9y8z7w6...",
  "timestamp": "2026-03-20T14:00:00Z",
  "dagger_version": "0.19.x",
  "builder": "dagger call bootstrap"
}
```

---

## Stage 2: Reproducible Compiler (v0.2.0)

### Purpose

Stage 2 rebuilds the compiler using Stage 1 and verifies
that the output is bit-for-bit identical to Stage 1's output.
This is the reproducibility gate.

### Process

```
1. Build Stage 1 on Builder A → digest-A
2. Build Stage 1 on Builder B → digest-B
3. Compare: digest-A == digest-B?
   → YES: Stage 2 passes. Compiler is reproducible.
   → NO:  Quarantine. Investigate. Fix. Rebuild.
```

### Dagger function (v0.2.0)

```go
// Stage2Verify rebuilds Stage 1 independently and compares digests.
func (m *TerranoxBootstrap) Stage2Verify(
    ctx context.Context,
    llvmVersion string,
) (string, error) {
    // Build Stage 1 normally
    stage1a := m.Stage1(llvmVersion)

    // Build Stage 1 again with a different container ID
    // (different build, same inputs)
    stage1b := m.Stage1(llvmVersion)

    // Hash both outputs
    hashA := dag.Container().
        From("cgr.dev/chainguard/wolfi-base:latest").
        WithDirectory("/a", stage1a).
        WithExec([]string{"find", "/a", "-type", "f",
            "-exec", "b3sum", "{}", "+"}).
        Stdout(ctx)

    hashB := dag.Container().
        From("cgr.dev/chainguard/wolfi-base:latest").
        WithDirectory("/b", stage1b).
        WithExec([]string{"find", "/b", "-type", "f",
            "-exec", "b3sum", "{}", "+"}).
        Stdout(ctx)

    // Compare
    if hashA != hashB {
        return "", fmt.Errorf(
            "REPRODUCIBILITY FAILURE:\n  Build A: %s\n  Build B: %s",
            hashA[:64], hashB[:64])
    }

    return fmt.Sprintf("Stage 2 VERIFIED: builds are identical\n  Digest: %s",
        hashA[:64]), nil
}
```

### Requirements for reproducibility

All of these must be normalized:

```
SOURCE_DATE_EPOCH=0              Fixed timestamp
TZ=UTC                           Fixed timezone
LC_ALL=C                         Fixed locale
PATH=/usr/bin:/bin                Minimal, fixed PATH
HOME=/nonexistent                No home directory influence
HOSTNAME=terranox-build          Fixed hostname
UID=1000 GID=1000                Fixed user/group
```

These are already set in the Dagger container environment.

### Non-deterministic stage quarantine

If Stage 2 fails reproducibility:

```
1. Artifact is quarantined (not promoted to store)
2. Build logs from both builders are diff'd:
   - Look for timestamp leaks
   - Look for ordering differences (filesystem, hash map)
   - Look for environment variable leaks
3. Root cause is documented in an issue
4. Patch is applied to build scripts or LLVM source
5. Stage is rebuilt and re-verified
```

---

## Stage 3: Optimized Production Compiler (v0.3.0)

### Purpose

Stage 3 is the production compiler used to build TerranoxOS
Core, Strata, Sentinel, and user packages. It adds PGO and
LTO for optimal performance while maintaining reproducibility.

### PGO (Profile-Guided Optimization)

```
1. Build Stage 2 Clang (unoptimized but reproducible)
2. Use Stage 2 Clang to compile a training workload:
   - Build musl, toybox, bash, zlib-ng, vim
   - Collect runtime profiles (.profdata)
3. Rebuild Clang with -fprofile-use=merged.profdata
4. Result: Stage 3 Clang optimized for TerranoxOS workloads
```

### LTO (Link-Time Optimization)

```
-DLLVM_ENABLE_LTO=Thin           Thin LTO (faster than Full LTO)
-DCMAKE_C_FLAGS="-flto=thin"
-DCMAKE_CXX_FLAGS="-flto=thin"
```

### Hardened build options

Stage 3 includes security hardening by default:

```
-fstack-protector-strong          Stack buffer overflow detection
-fcf-protection=full              Control-flow integrity (Intel CET)
-fno-delete-null-pointer-checks   Preserve null checks
-fno-strict-aliasing              Safer memory access
-D_FORTIFY_SOURCE=3               Buffer overflow detection
-pie                              Position-independent executables
-Wl,-z,relro,-z,now               Full RELRO (GOT protection)
```

### Requirements

- Must remain reproducible (PGO profiles are pinned, not
  regenerated per build).
- Must be verifiable: rebuild Stage 3 from Stage 2 + pinned
  profiles → identical output.
- Must be content-addressed and signed.

---

## Toolchain Variants

### v0.1.0: Primary variant

```
clang-musl    Clang + musl + compiler-rt + libc++ + libunwind
              Default variant for v0.1.0
              All initial packages and kernel built with this
```

### v0.2.0: Add glibc variant

```
clang-musl    Default (security, hardened, forensic, developer strata)
clang-glibc   For desktop and server strata (application compatibility)
              Separate bootstrap chain: Stage 0 → Stage 1 targeting glibc
              Produces separate store entries (different hashes)
```

Both variants go through the full bootstrap pipeline. The
Dagger version matrix gains a second dimension:

```
dagger call bootstrap --llvm-version=21.0.0 --libc=musl
dagger call bootstrap --llvm-version=21.0.0 --libc=glibc
```

### v0.3.0+: Optional GCC fallback

```
gcc-musl      Fallback for packages that require GCC extensions
gcc-glibc     Fallback for maximum compatibility
              Built as separate bootstrap chains
              Installed alongside Clang, not replacing it
              Used only when explicitly specified in a manifest
```

GCC is NOT a first-class citizen. It exists only as a
fallback for the rare package that truly cannot build with
Clang.

### Library resolution: Nix-style RPATH

All dynamically-linked binaries have their RPATH baked in at
build time, pointing directly to the store paths of their
dependencies. This eliminates any possibility of a musl binary
loading a glibc library or vice versa.

```
musl binary:  RPATH → /forge/store/<hash>-musl-1.2.5/usr/lib:...
glibc binary: RPATH → /forge/store/<hash>-glibc-2.39/usr/lib:...
```

No `ld.so.conf`. No `LD_LIBRARY_PATH`. No ambiguity.
See TERRANOX_FILESYSTEM_SPEC.md for the full store model.

### /core: Static and libc-agnostic

/core binaries (toybox, bash, terranox-strata, terranox-init)
are statically linked. They have zero libc dependency, making
/core work regardless of which libc the active strata use.

---

## Build Environment Requirements

### Hermeticity (enforced by Dagger)

```
✓ No network access during compilation
✓ No access to host libraries or toolchains
✓ Fixed locale (LC_ALL=C) and timezone (TZ=UTC)
✓ Canonical environment variables (SOURCE_DATE_EPOCH=0)
✓ Normalized filesystem ordering
✓ Container-isolated (Dagger + Wolfi base)
```

Dagger provides hermeticity by default. Each `WithExec()`
runs in an isolated container with only explicitly mounted
inputs.

### Sandbox enforcement

```
✓ Namespaced build (Dagger containers use Linux namespaces)
✓ Read-only root except for build workspace
✓ No /tmp leakage between stages (each function gets fresh container)
✓ No PID or network leakage
```

---

## Provenance and Verification

### Per-stage provenance

Every stage records:

```
Source:        LLVM commit hash, musl version, patch list
Build flags:   Full CMake configuration (from bootstrap.toml)
Environment:   Container image digest, Dagger version
Toolchain:     Previous stage's output digest
Outputs:       BLAKE3 digest of all output files
Build log:     Full build output (stored in Dagger cache)
SBOM:          CycloneDX for all components
Signature:     Cosign signature on output digest
```

### Verification pipeline

```
Build Stage N using Stage N-1           (primary build)
Rebuild Stage N independently           (verification build)
Compare digests:
  Match    → promote to /forge/store/, sign, publish
  Mismatch → quarantine, investigate, fix, rebuild
```

This is the same build-twice-and-compare model used by
Reproducible Builds, bootstrappable.org, and the SLSA
framework.

---

## Trust Root Compromise Recovery

If the Stage 0 trust root is compromised:

```
1. SECURITY ADVISORY: announce compromise
2. Generate new trust root:
   - New Wolfi seed with patched Clang
   - New digest pinned in bootstrap.toml
   - New signature from project maintainer
3. Rebuild entire bootstrap chain:
   Stage 0 (new seed) → Stage 1 → Stage 2 → Stage 3
4. Rebuild all packages from the new toolchain
5. New profile digests for all strata
6. Users: terranox-strata activate → rebuilds symlinks from
   new packages (previous generations still available for
   forensic comparison)
7. Document: incident report with timeline and root cause
```

Because TerranoxOS uses content-addressed profiles (see
TERRANOX_PROFILE_MODEL.md), the compromised and clean
systems have different profile digests. Sentinel's verify
command detects the mismatch.

---

## Integration Points

```
This spec is used by:           Reference doc:
──────────────────              ──────────────
Dagger pipeline functions       TERRANOX_BOOTSTRAP_DAGGER.md
CMake flag reference            CLFS_GUIDE.md §11
LLVM edge cases                 LLVM_CLANG_EDGE_CASES.md
Version matrix                  TERRANOX_BOOTSTRAP_CONFIG.md
GNAT LLVM (Ada compiler)       TERRANOX_ADA_SPARK_BUILD_GUIDE.md
Kernel build                    TERRANOX_KERNEL_ARCHITECTURE.md
Package builds (melange)        TERRANOX_WOLFI_GUIDE.md
Security/supply chain           TERRANOX_SECURITY_ARCHITECTURE.md
```

---

## Milestone Schedule

```
v0.1.0 "Nebulous":
  ✓ Stage 0: Wolfi seed verified (dagger call verify-seed)
  ✓ Stage 1: Self-hosted Clang (dagger call bootstrap)
  ✓ 8 validation tests pass (dagger call validate)
  ✓ Custom x86_64-pc-terranox triple
  ✓ GNAT LLVM built against Stage 1

v0.2.0 "Cirrus":
  ☐ Stage 2: Reproducibility verification (dagger call stage2-verify)
  ☐ Build-twice-compare pipeline operational
  ☐ Quarantine process for non-deterministic builds
  ☐ Per-stage provenance records in Forge store

v0.3.0 "Pulsar":
  ☐ Stage 3: PGO/LTO optimized compiler
  ☐ Hardened build flags in production toolchain
  ☐ GCC-musl variant (if needed for specific packages)
  ☐ Trust root rotation procedure tested
```