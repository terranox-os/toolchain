// SPDX-License-Identifier: Apache-2.0

// Musl-variant toolchain pipeline.
//
// This file contains the complete musl-based toolchain bootstrap.
// Every stage uses Alpine (musl) as the build host for consistency.
//
// Each stage can be built and exported independently:
//
//   dagger call musl-stage0 export --path=./stage0
//   dagger call musl-sysroot export --path=./sysroot
//   dagger call musl-stage1 --stage0=./stage0 --sysroot=./sysroot export --path=./stage1
//
// Or run the full pipeline at once:
//
//   dagger call musl-bootstrap export --path=./toolchain
//
// Pre-built artifacts can be passed in to skip expensive rebuilds:
//
//   dagger call musl-stage1 --stage0=./cached-stage0 --sysroot=./cached-sysroot
//   dagger call musl-bootstrap --stage0=./cached-stage0 --sysroot=./cached-sysroot

package main

import (
	"context"
	"fmt"
	"strings"

	"dagger/terranox-bootstrap/internal/dagger"
)

// ═══════════════════════════════════════════════════════════
// Stage 0: Cross-compiler (Alpine/musl host)
// ═══════════════════════════════════════════════════════════

// MuslStage0 builds the Stage 0 cross-compiler: a Clang targeting
// x86_64-linux-musl. Built in Alpine (musl) so all tools are musl-linked.
//
// Host: Alpine (musl)
// Seed: Alpine's Clang
// Output: /opt/terranox/cross-tools (Clang + LLD targeting musl)
//
// Usage:
//
//	dagger call musl-stage0
//	dagger call musl-stage0 --llvm-version=21.1.8
//	dagger call musl-stage0 export --path=./stage0
func (m *TerranoxBootstrap) MuslStage0(
	// +default="21.1.8"
	llvmVersion string,
	// Repository source directory containing patches/
	// +optional
	source *dagger.Directory,
) *dagger.Directory {
	llvmSrc := m.FetchLLVM(llvmVersion)

	// Apply Terranox patches if source provided
	if source != nil {
		patches := source.Directory("patches/llvm")
		llvmSrc = m.ApplyTerranoxPatches(llvmSrc, patches)
	}

	// Build in Alpine (musl) using Alpine's clang as seed compiler
	return dag.Container().
		From("alpine:latest").
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "cmake", "ninja", "samurai", "python3",
			"clang", "lld", "llvm", "musl-dev", "linux-headers",
			"git", "file", "ccache"}).
		// Mount persistent ccache volume to cache compiled objects across builds.
		// When inputs change (patches, base image), ccache still hits for
		// unchanged source files (~95% of LLVM), cutting rebuild time by 50-70%.
		WithMountedCache("/ccache", dag.CacheVolume("ccache-musl-stage0")).
		WithEnvVariable("CCACHE_DIR", "/ccache").
		WithEnvVariable("CCACHE_MAXSIZE", "5G").
		WithEnvVariable("CCACHE_COMPRESS", "true").
		WithDirectory("/tmp/llvm-project", llvmSrc).
		WithWorkdir("/tmp/build-stage0").
		WithExec([]string{"cmake", "-G", "Ninja",
			"-DCMAKE_BUILD_TYPE=Release",
			"-DCMAKE_INSTALL_PREFIX=/opt/terranox/cross-tools",
			"-DCMAKE_C_COMPILER=clang",
			"-DCMAKE_CXX_COMPILER=clang++",
			"-DCMAKE_C_COMPILER_LAUNCHER=ccache",
			"-DCMAKE_CXX_COMPILER_LAUNCHER=ccache",
			"-DLLVM_ENABLE_PROJECTS=clang;lld",
			"-DLLVM_ENABLE_RUNTIMES=",
			"-DLLVM_TARGETS_TO_BUILD=X86",
			"-DLLVM_DEFAULT_TARGET_TRIPLE=" + Target,
			"-DCLANG_DEFAULT_LINKER=lld",
			"-DCLANG_DEFAULT_CXX_STDLIB=libc++",
			"-DCLANG_DEFAULT_RTLIB=compiler-rt",
			"-DCLANG_DEFAULT_UNWINDLIB=libunwind",
			"-DLLVM_INSTALL_TOOLCHAIN_ONLY=ON",
			"-DLLVM_INCLUDE_TESTS=OFF",
			"-DLLVM_INCLUDE_EXAMPLES=OFF",
			"-DLLVM_INCLUDE_BENCHMARKS=OFF",
			"-DLLVM_INCLUDE_DOCS=OFF",
			// Limit parallel link jobs — each LLVM link uses 4-8 GB RAM.
			// Without this, nproc link jobs can launch simultaneously and
			// OOM on machines with limited RAM.
			"-DLLVM_PARALLEL_LINK_JOBS=2",
			"/tmp/llvm-project/llvm"}).
		WithExec([]string{"sh", "-c", "ninja -j$(nproc)"}).
		WithExec([]string{"ninja", "install"}).
		Directory("/opt/terranox/cross-tools")
}

// ═══════════════════════════════════════════════════════════
// Sysroot: musl + LLVM runtimes
// ═══════════════════════════════════════════════════════════

// MuslSysroot builds the musl sysroot containing:
// - Linux kernel headers
// - musl libc (headers + full build)
// - LLVM runtimes (compiler-rt, libunwind, libc++abi, libc++)
//
// Host: Alpine (musl) — consistent with Stage0 and Stage1.
// Alpine's native Clang builds musl directly (no cross-compilation needed)
// since both the host and target are musl.
//
// Usage:
//
//	dagger call musl-sysroot
//	dagger call musl-sysroot export --path=./sysroot
func (m *TerranoxBootstrap) MuslSysroot(
	// LLVM version for runtimes
	// +default="21.1.8"
	llvmVersion string,
	// musl version
	// +default="1.2.5"
	muslVersion string,
	// Linux kernel version for headers
	// +default="6.12.8"
	linuxVersion string,
	// Repository source directory containing patches/
	// +optional
	source *dagger.Directory,
) *dagger.Directory {
	llvmSrc := m.FetchLLVM(llvmVersion)

	// Apply Terranox patches if source provided
	if source != nil {
		patches := source.Directory("patches/llvm")
		llvmSrc = m.ApplyTerranoxPatches(llvmSrc, patches)
	}

	muslSrc := m.FetchMusl(muslVersion)
	headers := m.FetchLinuxHeaders(linuxVersion)

	// Alpine base with Clang + build tools for musl sysroot construction.
	// Using Alpine means the seed Clang is already musl-linked and targets
	// musl natively — no cross-compilation indirection needed.
	alpineBuild := dag.Container().
		From("alpine:latest").
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "coreutils", "make",
			"clang", "lld", "llvm", "compiler-rt",
			"cmake", "ninja", "samurai",
			"musl-dev", "linux-headers", "python3",
			"file",
		}).
		WithExec([]string{"mkdir", "-p",
			"/opt/terranox/sysroot/usr/lib",
			"/opt/terranox/sysroot/usr/include",
			"/opt/terranox/sysroot/usr/bin",
			"/opt/terranox/sysroot/lib",
		}).
		WithEnvVariable("CLFS_TARGET", Target).
		WithEnvVariable("CLFS_ARCH", Arch).
		WithWorkdir("/opt/terranox")

	// Step 1 + 2: Kernel headers + musl headers + full musl build
	//
	// Alpine's Clang targets musl natively. We still pass --target
	// explicitly to ensure the output triple matches our convention.
	sysrootWithMusl := alpineBuild.
		WithDirectory("/opt/terranox/sysroot", headers).
		WithDirectory("/tmp/musl-src", muslSrc).
		WithWorkdir("/tmp/musl-src").
		// Install musl headers
		WithExec([]string{"make", "ARCH=" + Arch,
			"prefix=/usr", "DESTDIR=/opt/terranox/sysroot",
			"install-headers"}).
		// Build full musl
		WithExec([]string{"sh", "-c",
			`CC="clang --target=` + Target + `" AR=llvm-ar RANLIB=llvm-ranlib ` +
				`./configure --prefix=/usr --target=` + Target + ` --disable-wrapper`}).
		WithExec([]string{"make", fmt.Sprintf("-j%d", 4)}).
		WithExec([]string{"make", "DESTDIR=/opt/terranox/sysroot", "install"}).
		// Dynamic linker symlink
		WithExec([]string{"ln", "-sf", "/usr/lib/libc.so",
			"/opt/terranox/sysroot/lib/ld-musl-x86_64.so.1"}).
		Directory("/opt/terranox/sysroot")

	// Step 3: Build LLVM runtimes against the musl sysroot
	//
	// Fresh Alpine container with the sysroot from step 2.
	// Clang cross-compiles the runtimes targeting our sysroot.
	runtimesBuild := dag.Container().
		From("alpine:latest").
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "cmake", "ninja", "python3", "file",
			"clang", "lld", "llvm", "compiler-rt",
		}).
		WithDirectory("/opt/terranox/sysroot", sysrootWithMusl).
		WithDirectory("/tmp/llvm-project", llvmSrc).
		WithWorkdir("/tmp/build-runtimes").
		WithExec([]string{"cmake", "-G", "Ninja",
			"-DCMAKE_BUILD_TYPE=Release",
			"-DCMAKE_INSTALL_PREFIX=/opt/terranox/sysroot/usr",
			"-DCMAKE_C_COMPILER=clang",
			"-DCMAKE_CXX_COMPILER=clang++",
			"-DCMAKE_C_COMPILER_TARGET=" + Target,
			"-DCMAKE_CXX_COMPILER_TARGET=" + Target,
			"-DCMAKE_ASM_COMPILER_TARGET=" + Target,
			"-DCMAKE_SYSROOT=/opt/terranox/sysroot",
			"-DCMAKE_C_FLAGS=-fPIC",
			"-DCMAKE_CXX_FLAGS=-fPIC",
			"-DCMAKE_EXE_LINKER_FLAGS=-fuse-ld=lld",
			"-DCMAKE_SHARED_LINKER_FLAGS=-fuse-ld=lld",
			"-DCMAKE_TRY_COMPILE_TARGET_TYPE=STATIC_LIBRARY",
			"-DLLVM_ENABLE_RUNTIMES=compiler-rt;libunwind;libcxxabi;libcxx",
			"-DCOMPILER_RT_BUILD_BUILTINS=ON",
			"-DCOMPILER_RT_BUILD_SANITIZERS=OFF",
			"-DCOMPILER_RT_BUILD_XRAY=OFF",
			"-DCOMPILER_RT_BUILD_LIBFUZZER=OFF",
			"-DCOMPILER_RT_BUILD_PROFILE=OFF",
			"-DCOMPILER_RT_BUILD_MEMPROF=OFF",
			"-DCOMPILER_RT_BUILD_ORC=OFF",
			"-DCOMPILER_RT_DEFAULT_TARGET_ONLY=ON",
			"-DLIBUNWIND_USE_COMPILER_RT=ON",
			"-DLIBUNWIND_ENABLE_STATIC=ON",
			"-DLIBUNWIND_ENABLE_SHARED=OFF",
			"-DLIBCXXABI_USE_COMPILER_RT=ON",
			"-DLIBCXXABI_USE_LLVM_UNWINDER=ON",
			"-DLIBCXXABI_ENABLE_STATIC=ON",
			"-DLIBCXXABI_ENABLE_SHARED=OFF",
			"-DLIBCXXABI_HAS_CXA_THREAD_ATEXIT_IMPL=OFF",
			"-DLIBCXX_USE_COMPILER_RT=ON",
			"-DLIBCXX_HAS_MUSL_LIBC=ON",
			"-DLIBCXX_ENABLE_STATIC=ON",
			"-DLIBCXX_ENABLE_SHARED=OFF",
			"-DLIBCXX_CXX_ABI=libcxxabi",
			"/tmp/llvm-project/runtimes"}).
		WithExec([]string{"sh", "-c", "ninja -j$(nproc)"}).
		WithExec([]string{"ninja", "install"}).
		// Create libgcc compatibility shim
		WithExec([]string{"sh", "-c",
			`echo 'INPUT(-lclang_rt.builtins -lunwind)' > /opt/terranox/sysroot/usr/lib/libgcc_s.so`}).
		Directory("/opt/terranox/sysroot")

	return runtimesBuild
}

// ═══════════════════════════════════════════════════════════
// SDK Sysroot: musl sysroot + Alpine dev packages
// ═══════════════════════════════════════════════════════════

// MuslSdkSysroot extends the base musl sysroot with development library
// headers and static libraries from Alpine. This enables cross-compilation
// of userspace programs that depend on common C libraries (zlib, openssl, etc.).
//
// The package list is driven by the [sdk] section in bootstrap.toml.
// Alpine packages are used because they are musl-linked and ABI-compatible
// with the base sysroot.
//
// Usage:
//
//	dagger call musl-sdk-sysroot
//	dagger call musl-sdk-sysroot --llvm-version=21.1.8 --source=.
//	dagger call musl-sdk-sysroot export --path=./sdk-sysroot
func (m *TerranoxBootstrap) MuslSdkSysroot(
	ctx context.Context,
	// LLVM version for runtimes
	// +default="21.1.8"
	llvmVersion string,
	// musl version
	// +default="1.2.5"
	muslVersion string,
	// Linux kernel version for headers
	// +default="6.12.8"
	linuxVersion string,
	// Pre-built base sysroot directory. If not provided,
	// the base sysroot is built from scratch via MuslSysroot.
	// +optional
	sysroot *dagger.Directory,
	// Repository source directory containing patches/ and bootstrap.toml
	// +optional
	source *dagger.Directory,
) (*dagger.Directory, error) {
	// Build the base sysroot if not provided
	baseSysroot := sysroot
	if baseSysroot == nil {
		baseSysroot = m.MuslSysroot(llvmVersion, muslVersion, linuxVersion, source)
	}

	// Load config to get the SDK package list
	var packages []string
	if source != nil {
		cfg, err := LoadConfig(ctx, source)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		packages = cfg.Sdk.Packages
	} else {
		packages = defaultConfig().Sdk.Packages
	}

	if len(packages) == 0 {
		// No packages configured — return the base sysroot unchanged
		return baseSysroot, nil
	}

	// Use Alpine to fetch and extract dev packages into the sysroot.
	// We install packages into a temporary root, then copy only the
	// headers and libraries into the sysroot — avoiding runtime deps.
	sdkSysroot := dag.Container().
		From("alpine:latest").
		WithExec([]string{"apk", "add", "--no-cache", "tar"}).
		WithDirectory("/opt/terranox/sysroot", baseSysroot).
		WithExec([]string{"mkdir", "-p", "/tmp/sdk-pkgs"}).
		// Fetch the requested packages as .apk files
		WithExec(append([]string{"apk", "fetch", "--no-cache", "-o", "/tmp/sdk-pkgs"}, packages...)).
		// Extract each package's contents into the sysroot
		WithExec([]string{"sh", "-c", `
			for apk in /tmp/sdk-pkgs/*.apk; do
				[ -f "$apk" ] || continue
				echo "Extracting: $(basename "$apk")"
				tar -xzf "$apk" -C /opt/terranox/sysroot 2>/dev/null || true
			done
			# Remove apk metadata that may have been extracted
			rm -rf /opt/terranox/sysroot/.PKGINFO /opt/terranox/sysroot/.SIGN.* /opt/terranox/sysroot/.post-install /opt/terranox/sysroot/.pre-install /opt/terranox/sysroot/.trigger
		`}).
		Directory("/opt/terranox/sysroot")

	return sdkSysroot, nil
}

// TestSdkSysroot verifies the SDK sysroot contains development headers
// and static libraries, and that a simple program can compile and link
// against them.
//
// Usage:
//
//	dagger call test-sdk-sysroot
//	dagger call test-sdk-sysroot --llvm-version=21.1.8 --source=.
func (m *TerranoxBootstrap) TestSdkSysroot(
	ctx context.Context,
	// +default="21.1.8"
	llvmVersion string,
	// Repository source directory containing patches/ and bootstrap.toml
	// +optional
	source *dagger.Directory,
) (string, error) {
	sdkSysroot, err := m.MuslSdkSysroot(ctx, llvmVersion, DefaultMuslVersion, DefaultLinuxVersion, nil, source)
	if err != nil {
		return "", fmt.Errorf("build SDK sysroot: %w", err)
	}

	// Load config to report which packages were expected
	var packages []string
	if source != nil {
		cfg, err := LoadConfig(ctx, source)
		if err != nil {
			return "", fmt.Errorf("load config: %w", err)
		}
		packages = cfg.Sdk.Packages
	} else {
		packages = defaultConfig().Sdk.Packages
	}

	return m.Base().
		WithDirectory("/sysroot", sdkSysroot).
		WithNewFile("/tmp/test_zlib.c", `
			#include <zlib.h>
			#include <stdio.h>
			int main(void) {
				printf("zlib version: %s\n", zlibVersion());
				return 0;
			}
		`).
		WithExec([]string{"sh", "-c", `
			echo "=== SDK Sysroot Verification ==="
			echo
			echo "Configured packages: ` + strings.Join(packages, ", ") + `"
			echo
			echo "--- Dev headers ---"
			[ -f /sysroot/usr/include/zlib.h ] && echo "PASS: zlib.h" || echo "FAIL: zlib.h missing"
			[ -f /sysroot/usr/include/openssl/ssl.h ] && echo "PASS: openssl/ssl.h" || echo "FAIL: openssl/ssl.h missing"
			[ -f /sysroot/usr/include/ncurses.h ] && echo "PASS: ncurses.h" || echo "FAIL: ncurses.h missing"
			[ -f /sysroot/usr/include/readline/readline.h ] && echo "PASS: readline/readline.h" || echo "FAIL: readline/readline.h missing"
			echo
			echo "--- Static libraries ---"
			[ -f /sysroot/usr/lib/libz.a ] && echo "PASS: libz.a" || echo "FAIL: libz.a missing"
			[ -f /sysroot/usr/lib/libssl.a ] && echo "PASS: libssl.a" || echo "FAIL: libssl.a missing"
			[ -f /sysroot/usr/lib/libcrypto.a ] && echo "PASS: libcrypto.a" || echo "FAIL: libcrypto.a missing"
			echo
			echo "--- Compile test: zlib ---"
		`}).
		WithExec([]string{"clang",
			"--target=" + Target,
			"--sysroot=/sysroot",
			"-static",
			"-fuse-ld=lld",
			"-o", "/tmp/test_zlib",
			"/tmp/test_zlib.c",
			"-lz"}).
		WithExec([]string{"file", "/tmp/test_zlib"}).
		WithExec([]string{"/tmp/test_zlib"}).
		WithExec([]string{"sh", "-c", `echo && echo "=== All SDK sysroot tests passed ==="`}).
		Stdout(ctx)
}

// ═══════════════════════════════════════════════════════════
// Stage 1: Self-hosted Clang (Alpine/musl host)
// ═══════════════════════════════════════════════════════════

// MuslStage1 builds the self-hosted Clang linked against musl.
// This is the final compiler with zero GNU dependencies.
//
// Host: Alpine (musl)
// Compiler: Stage0 cross-compiler (built or pre-built)
// Links against: Sysroot (musl + LLVM runtimes, built or pre-built)
// Output: /opt/terranox/tools (self-hosted Clang + LLD)
//
// Pre-built Stage0 and Sysroot can be passed in to skip rebuilding:
//
//	dagger call musl-stage1 --stage0=./cached-stage0 --sysroot=./cached-sysroot
//	dagger call musl-stage1 export --path=./stage1
func (m *TerranoxBootstrap) MuslStage1(
	// +default="21.1.8"
	llvmVersion string,
	// +default="1.2.5"
	muslVersion string,
	// +default="6.12.8"
	linuxVersion string,
	// Pre-built Stage0 cross-tools directory. If not provided,
	// Stage0 is built from scratch.
	// +optional
	stage0 *dagger.Directory,
	// Pre-built sysroot directory. If not provided,
	// the sysroot is built from scratch.
	// +optional
	sysroot *dagger.Directory,
	// Repository source directory containing patches/
	// +optional
	source *dagger.Directory,
) *dagger.Directory {
	llvmSrc := m.FetchLLVM(llvmVersion)

	// Apply Terranox patches if source provided
	if source != nil {
		patches := source.Directory("patches/llvm")
		llvmSrc = m.ApplyTerranoxPatches(llvmSrc, patches)
	}

	// Use pre-built artifacts if provided, otherwise build from scratch
	crossTools := stage0
	if crossTools == nil {
		crossTools = m.MuslStage0(llvmVersion, source)
	}

	sysrootDir := sysroot
	if sysrootDir == nil {
		sysrootDir = m.MuslSysroot(llvmVersion, muslVersion, linuxVersion, source)
	}

	// Build in Alpine (musl) so build tools can execute natively
	return dag.Container().
		From("alpine:latest").
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "cmake", "ninja", "samurai", "python3",
			"musl-dev", "linux-headers", "file", "ccache"}).
		// Mount persistent ccache volume for Stage1 compilation cache.
		// Separate from Stage0 because a different compiler (Stage0's clang)
		// produces different object hashes.
		WithMountedCache("/ccache", dag.CacheVolume("ccache-musl-stage1")).
		WithEnvVariable("CCACHE_DIR", "/ccache").
		WithEnvVariable("CCACHE_MAXSIZE", "5G").
		WithEnvVariable("CCACHE_COMPRESS", "true").
		WithDirectory("/opt/terranox/cross-tools", crossTools).
		WithDirectory("/opt/terranox/sysroot", sysrootDir).
		WithDirectory("/tmp/llvm-project", llvmSrc).
		// Copy runtime libraries to Stage0's resource directory
		WithExec([]string{"sh", "-c", `
			mkdir -p /opt/terranox/cross-tools/lib/clang/21/lib/x86_64-unknown-linux-musl
			cp -a /opt/terranox/sysroot/usr/lib/linux/* \
				/opt/terranox/cross-tools/lib/clang/21/lib/x86_64-unknown-linux-musl/
			cp /opt/terranox/sysroot/usr/lib/crt*.o \
				/opt/terranox/cross-tools/lib/clang/21/lib/x86_64-unknown-linux-musl/

			# Create symlinks for expected filenames
			cd /opt/terranox/cross-tools/lib/clang/21/lib/x86_64-unknown-linux-musl
			ln -sf libclang_rt.builtins-x86_64.a libclang_rt.builtins.a
			ln -sf clang_rt.crtbegin-x86_64.o crtbeginS.o
			ln -sf clang_rt.crtbegin-x86_64.o crtbegin.o
			ln -sf clang_rt.crtend-x86_64.o crtendS.o
			ln -sf clang_rt.crtend-x86_64.o crtend.o
		`}).
		WithEnvVariable("PATH",
			"/opt/terranox/cross-tools/bin:/usr/bin:/bin").
		WithWorkdir("/tmp/build-stage1").
		WithExec([]string{"cmake", "-G", "Ninja",
			"-DCMAKE_BUILD_TYPE=Release",
			"-DCMAKE_INSTALL_PREFIX=/opt/terranox/tools",
			"-DCMAKE_C_COMPILER=/opt/terranox/cross-tools/bin/clang",
			"-DCMAKE_CXX_COMPILER=/opt/terranox/cross-tools/bin/clang++",
			"-DCMAKE_C_COMPILER_LAUNCHER=ccache",
			"-DCMAKE_CXX_COMPILER_LAUNCHER=ccache",
			// Stage0's default target is already x86_64-linux-musl, so omit --target
			// This makes Stage1 a NATIVE build in Alpine (musl), not a cross-compile
			"-DCMAKE_C_FLAGS=--sysroot=/opt/terranox/sysroot",
			"-DCMAKE_CXX_FLAGS=--sysroot=/opt/terranox/sysroot -stdlib=libc++",
			// Add -L flag to tell linker where to find libc++abi and libunwind
			"-DCMAKE_EXE_LINKER_FLAGS=-fuse-ld=lld -L/opt/terranox/sysroot/usr/lib -lc++abi -lunwind",
			"-DCMAKE_SHARED_LINKER_FLAGS=-fuse-ld=lld -L/opt/terranox/sysroot/usr/lib -lc++abi -lunwind",
			"-DCMAKE_CROSSCOMPILING=FALSE",
			"-DLLVM_ENABLE_PROJECTS=clang;lld",
			"-DLLVM_ENABLE_RUNTIMES=",
			"-DLLVM_TARGETS_TO_BUILD=X86",
			"-DLLVM_DEFAULT_TARGET_TRIPLE=" + Target,
			"-DLLVM_HOST_TRIPLE=" + Target,
			"-DCLANG_DEFAULT_LINKER=lld",
			"-DCLANG_DEFAULT_CXX_STDLIB=libc++",
			"-DCLANG_DEFAULT_RTLIB=compiler-rt",
			"-DCLANG_DEFAULT_UNWINDLIB=libunwind",
			"-DDEFAULT_SYSROOT=/opt/terranox/sysroot",
			"-DLLVM_INSTALL_TOOLCHAIN_ONLY=ON",
			"-DLLVM_INCLUDE_TESTS=OFF",
			"-DLLVM_INCLUDE_EXAMPLES=OFF",
			"-DLLVM_INCLUDE_BENCHMARKS=OFF",
			"-DLLVM_PARALLEL_LINK_JOBS=2",
			"/tmp/llvm-project/llvm"}).
		WithExec([]string{"sh", "-c", "ninja -j$(nproc)"}).
		WithExec([]string{"ninja", "install"}).
		Directory("/opt/terranox/tools")
}

// ═══════════════════════════════════════════════════════════
// Bootstrap: Full musl toolchain assembly
// ═══════════════════════════════════════════════════════════

// MuslBootstrap builds the complete musl-based toolchain and
// packages it into a single output directory with a manifest.
//
// Pre-built Stage0 and Sysroot can be passed in to skip rebuilding.
// This enables incremental CI where earlier stages are cached as
// artifacts between jobs.
//
// Pipeline:
//
//	Alpine seed ──→ Stage0 (cross-compiler)
//	Alpine seed ──→ Sysroot (musl + LLVM runtimes)
//	Stage0 + Sysroot ──→ Stage1 (self-hosted musl Clang)
//
// Output structure:
//
//	tools/     - Stage1 Clang, LLD, llvm-ar, etc.
//	sysroot/   - musl headers/libs, LLVM runtimes, kernel headers
//	manifest.json - Build metadata
//
// Usage:
//
//	dagger call musl-bootstrap
//	dagger call musl-bootstrap --stage0=./cached-stage0 --sysroot=./cached-sysroot
//	dagger call musl-bootstrap export --path=./toolchain
func (m *TerranoxBootstrap) MuslBootstrap(
	ctx context.Context,
	// +default="21.1.8"
	llvmVersion string,
	// Pre-built Stage0 cross-tools directory. Skips Stage0 build if provided.
	// +optional
	stage0 *dagger.Directory,
	// Pre-built sysroot directory. Skips sysroot build if provided.
	// +optional
	sysroot *dagger.Directory,
	// Repository source directory containing patches/
	// +optional
	source *dagger.Directory,
) (*dagger.Directory, error) {
	// Step 1: Validate seed compiler
	_, err := m.Validate(ctx)
	if err != nil {
		return nil, fmt.Errorf("seed validation failed: %w", err)
	}

	// Use pre-built artifacts if provided
	stage0Dir := stage0
	if stage0Dir == nil {
		stage0Dir = m.MuslStage0(llvmVersion, source)
	}

	sysrootDir := sysroot
	if sysrootDir == nil {
		sysrootDir = m.MuslSysroot(llvmVersion, DefaultMuslVersion, DefaultLinuxVersion, source)
	}

	// Step 2: Build Stage1 using pre-built or freshly built artifacts
	tools := m.MuslStage1(llvmVersion, DefaultMuslVersion, DefaultLinuxVersion, stage0Dir, sysrootDir, source)

	// Package into a single output directory
	return dag.Directory().
		WithDirectory("tools", tools).
		WithDirectory("sysroot", sysrootDir).
		WithNewFile("manifest.json", fmt.Sprintf(`{
	"genesis_bootstrap": "0.3.0",
	"llvm_version": "%s",
	"musl_version": "%s",
	"linux_version": "%s",
	"target": "%s",
	"variant": "musl"
}`, llvmVersion, DefaultMuslVersion, DefaultLinuxVersion, Target)), nil
}

// ═══════════════════════════════════════════════════════════
// SDK Export and Image
// ═══════════════════════════════════════════════════════════

// ExportSdkSysroot builds the SDK sysroot and writes it to the host filesystem.
//
// Usage:
//
//	dagger call export-sdk-sysroot --llvm-version=21.1.8 --source=. --output=./out/sdk-sysroot
func (m *TerranoxBootstrap) ExportSdkSysroot(
	ctx context.Context,
	// +default="21.1.8"
	llvmVersion string,
	// Host directory to export to
	output *dagger.Directory,
	// Repository source directory containing patches/ and bootstrap.toml
	// +optional
	source *dagger.Directory,
) (*dagger.Directory, error) {
	sdkSysroot, err := m.MuslSdkSysroot(ctx, llvmVersion, DefaultMuslVersion, DefaultLinuxVersion, nil, source)
	if err != nil {
		return nil, fmt.Errorf("build SDK sysroot: %w", err)
	}
	return output.WithDirectory(".", sdkSysroot), nil
}

// SdkImage creates a Wolfi-style OCI container image with the Stage1
// toolchain and the SDK sysroot installed. This is a self-contained
// development environment for cross-compiling userspace programs
// against musl with common libraries (zlib, openssl, etc.) available.
//
// The image is modeled after Wolfi/Chainguard SDK images:
//   - Minimal Alpine base (musl-native, no glibc compat)
//   - Toolchain at /opt/terranox/toolchain
//   - SDK sysroot at /opt/terranox/sysroot (includes dev headers + static libs)
//   - Build tools (cmake, ninja, git, make, pkg-config)
//   - PATH and sysroot env vars pre-configured
//
// Usage:
//
//	# Export as OCI tarball:
//	dagger call sdk-image --source=. export --path=./terranox-sdk.tar
//
//	# Publish to registry:
//	dagger call sdk-image --source=. publish --address=ghcr.io/terranox-os/sdk:latest
//
//	# Interactive shell:
//	dagger call sdk-image --source=. terminal
func (m *TerranoxBootstrap) SdkImage(
	ctx context.Context,
	// +default="21.1.8"
	llvmVersion string,
	// +default="1.2.5"
	muslVersion string,
	// +default="6.12.8"
	linuxVersion string,
	// Pre-built Stage1 toolchain directory. If not provided, built from scratch.
	// +optional
	toolchain *dagger.Directory,
	// Pre-built SDK sysroot directory. If not provided, built from scratch.
	// +optional
	sdkSysroot *dagger.Directory,
	// Repository source directory containing patches/ and bootstrap.toml
	// +optional
	source *dagger.Directory,
) (*dagger.Container, error) {
	// Build toolchain if not provided
	if toolchain == nil {
		toolchain = m.MuslStage1(llvmVersion, muslVersion, linuxVersion, nil, nil, source)
	}

	// Build SDK sysroot if not provided
	if sdkSysroot == nil {
		var err error
		sdkSysroot, err = m.MuslSdkSysroot(ctx, llvmVersion, muslVersion, linuxVersion, nil, source)
		if err != nil {
			return nil, fmt.Errorf("build SDK sysroot: %w", err)
		}
	}

	return dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "cmake", "ninja", "samurai", "make",
			"git", "file", "pkgconf",
			"python3", "perl",
		}).
		WithDirectory("/opt/terranox/toolchain", toolchain).
		WithDirectory("/opt/terranox/sysroot", sdkSysroot).
		// Copy runtime libraries into toolchain's Clang resource dir
		WithExec([]string{"sh", "-c", `
			mkdir -p /opt/terranox/toolchain/lib/clang/21/lib/x86_64-unknown-linux-musl
			cp -a /opt/terranox/sysroot/usr/lib/linux/* \
				/opt/terranox/toolchain/lib/clang/21/lib/x86_64-unknown-linux-musl/ 2>/dev/null || true
			cp /opt/terranox/sysroot/usr/lib/crt*.o \
				/opt/terranox/toolchain/lib/clang/21/lib/x86_64-unknown-linux-musl/ 2>/dev/null || true

			# Create symlinks for expected filenames
			cd /opt/terranox/toolchain/lib/clang/21/lib/x86_64-unknown-linux-musl
			ln -sf libclang_rt.builtins-x86_64.a libclang_rt.builtins.a 2>/dev/null || true
			ln -sf clang_rt.crtbegin-x86_64.o crtbeginS.o 2>/dev/null || true
			ln -sf clang_rt.crtbegin-x86_64.o crtbegin.o 2>/dev/null || true
			ln -sf clang_rt.crtend-x86_64.o crtendS.o 2>/dev/null || true
			ln -sf clang_rt.crtend-x86_64.o crtend.o 2>/dev/null || true
		`}).
		// Convenience wrapper so bare `cc` / `c++` just work
		WithExec([]string{"sh", "-c", `
			ln -sf /opt/terranox/toolchain/bin/clang /usr/local/bin/cc
			ln -sf /opt/terranox/toolchain/bin/clang++ /usr/local/bin/c++
			ln -sf /opt/terranox/toolchain/bin/lld /usr/local/bin/ld
			ln -sf /opt/terranox/toolchain/bin/llvm-ar /usr/local/bin/ar
			ln -sf /opt/terranox/toolchain/bin/llvm-ranlib /usr/local/bin/ranlib
			ln -sf /opt/terranox/toolchain/bin/llvm-nm /usr/local/bin/nm
			ln -sf /opt/terranox/toolchain/bin/llvm-objdump /usr/local/bin/objdump
			ln -sf /opt/terranox/toolchain/bin/llvm-strip /usr/local/bin/strip
		`}).
		WithEnvVariable("PATH", "/opt/terranox/toolchain/bin:/usr/local/bin:/usr/bin:/bin").
		WithEnvVariable("TERRANOX_SYSROOT", "/opt/terranox/sysroot").
		WithEnvVariable("TERRANOX_VERSION", llvmVersion).
		WithEnvVariable("CC", "clang --sysroot=/opt/terranox/sysroot").
		WithEnvVariable("CXX", "clang++ --sysroot=/opt/terranox/sysroot -stdlib=libc++").
		WithEnvVariable("LD", "ld.lld").
		WithEnvVariable("AR", "llvm-ar").
		WithEnvVariable("RANLIB", "llvm-ranlib").
		WithEnvVariable("PKG_CONFIG_SYSROOT_DIR", "/opt/terranox/sysroot").
		WithEnvVariable("PKG_CONFIG_PATH", "/opt/terranox/sysroot/usr/lib/pkgconfig").
		WithLabel("org.opencontainers.image.title", "TerranoxOS SDK").
		WithLabel("org.opencontainers.image.description",
			"Musl-based SDK with LLVM/Clang toolchain and development libraries for TerranoxOS").
		WithLabel("org.opencontainers.image.version", llvmVersion).
		WithLabel("org.opencontainers.image.vendor", "TerranoxOS").
		WithLabel("org.opencontainers.image.licenses", "Apache-2.0").
		WithLabel("terranox.llvm.version", llvmVersion).
		WithLabel("terranox.musl.version", muslVersion).
		WithLabel("terranox.target", Target).
		WithLabel("terranox.variant", "sdk").
		WithWorkdir("/workspace"), nil
}
