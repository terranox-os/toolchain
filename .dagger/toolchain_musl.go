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
			// Without this, -j4 can launch 4 link jobs simultaneously and
			// OOM on CI runners (16 GB), falling back to swap.
			"-DLLVM_PARALLEL_LINK_JOBS=2",
			"/tmp/llvm-project/llvm"}).
		WithExec([]string{"ninja", "-j4"}).
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
		WithExec([]string{"ninja", "-j4"}).
		WithExec([]string{"ninja", "install"}).
		// Create libgcc compatibility shim
		WithExec([]string{"sh", "-c",
			`echo 'INPUT(-lclang_rt.builtins -lunwind)' > /opt/terranox/sysroot/usr/lib/libgcc_s.so`}).
		Directory("/opt/terranox/sysroot")

	return runtimesBuild
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
		WithExec([]string{"ninja", "-j4"}).
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
