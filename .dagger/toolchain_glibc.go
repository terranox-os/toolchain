// SPDX-License-Identifier: Apache-2.0

// Glibc-variant toolchain pipeline.
//
// This file contains the complete glibc-based toolchain bootstrap.
// Every stage uses Wolfi (glibc) as the build host for consistency.
//
// Each stage can be built and exported independently:
//
//   dagger call glibc-stage0 export --path=./stage0
//   dagger call glibc-sysroot export --path=./sysroot
//   dagger call glibc-stage1 --stage0=./stage0 --sysroot=./sysroot export --path=./stage1
//
// Or run the full pipeline at once:
//
//   dagger call glibc-bootstrap export --path=./toolchain
//
// Pre-built artifacts can be passed in to skip expensive rebuilds:
//
//   dagger call glibc-stage1 --stage0=./cached-stage0 --sysroot=./cached-sysroot
//   dagger call glibc-bootstrap --stage0=./cached-stage0 --sysroot=./cached-sysroot

package main

import (
	"context"
	"fmt"

	"dagger/terranox-bootstrap/internal/dagger"
)

// ═══════════════════════════════════════════════════════════
// Stage 0: Cross-compiler (Wolfi/glibc host)
// ═══════════════════════════════════════════════════════════

// GlibcStage0 builds the Stage 0 cross-compiler for the glibc variant:
// a Clang targeting x86_64-pc-terranox-gnu. Built in Wolfi (glibc-based)
// so all build tools are glibc-linked.
//
// Host: Wolfi (glibc)
// Seed: Wolfi's Clang
// Output: /opt/terranox-glibc/cross-tools (Clang + LLD targeting glibc)
//
// Usage:
//
//	dagger call glibc-stage0
//	dagger call glibc-stage0 --llvm-version=21.1.8
//	dagger call glibc-stage0 export --path=./stage0
func (m *TerranoxBootstrap) GlibcStage0(
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

	// Build in Wolfi (glibc) using Wolfi's clang as seed compiler
	return dag.Container().
		From("cgr.dev/chainguard/wolfi-base:latest").
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "cmake", "ninja", "samurai", "python3",
			"clang-" + WolfiClangVersion,
			"lld-" + WolfiClangVersion,
			"llvm-" + WolfiClangVersion,
			"glibc-dev", "linux-headers",
			"git", "file", "ccache"}).
		// Mount persistent ccache volume to cache compiled objects across builds.
		WithMountedCache("/ccache", dag.CacheVolume("ccache-glibc-stage0")).
		WithEnvVariable("CCACHE_DIR", "/ccache").
		WithEnvVariable("CCACHE_MAXSIZE", "5G").
		WithEnvVariable("CCACHE_COMPRESS", "true").
		WithDirectory("/tmp/llvm-project", llvmSrc).
		WithWorkdir("/tmp/build-stage0-glibc").
		WithExec([]string{"cmake", "-G", "Ninja",
			"-DCMAKE_BUILD_TYPE=Release",
			"-DCMAKE_INSTALL_PREFIX=/opt/terranox-glibc/cross-tools",
			"-DCMAKE_C_COMPILER=clang",
			"-DCMAKE_CXX_COMPILER=clang++",
			"-DCMAKE_C_COMPILER_LAUNCHER=ccache",
			"-DCMAKE_CXX_COMPILER_LAUNCHER=ccache",
			"-DLLVM_ENABLE_PROJECTS=clang;lld",
			"-DLLVM_ENABLE_RUNTIMES=",
			"-DLLVM_TARGETS_TO_BUILD=X86",
			"-DLLVM_DEFAULT_TARGET_TRIPLE=" + TargetGlibc,
			"-DCLANG_DEFAULT_LINKER=lld",
			"-DCLANG_DEFAULT_CXX_STDLIB=libc++",
			"-DCLANG_DEFAULT_RTLIB=compiler-rt",
			"-DCLANG_DEFAULT_UNWINDLIB=libunwind",
			"-DLLVM_INSTALL_TOOLCHAIN_ONLY=ON",
			"-DLLVM_INCLUDE_TESTS=OFF",
			"-DLLVM_INCLUDE_EXAMPLES=OFF",
			"-DLLVM_INCLUDE_BENCHMARKS=OFF",
			"-DLLVM_INCLUDE_DOCS=OFF",
			"-DLLVM_PARALLEL_LINK_JOBS=2",
			"/tmp/llvm-project/llvm"}).
		WithExec([]string{"sh", "-c", "ninja -j$(nproc)"}).
		WithExec([]string{"ninja", "install"}).
		Directory("/opt/terranox-glibc/cross-tools")
}

// ═══════════════════════════════════════════════════════════
// Sysroot: glibc + LLVM runtimes
// ═══════════════════════════════════════════════════════════

// GlibcSysroot builds the glibc variant sysroot containing:
// - glibc (C standard library)
// - Linux kernel headers
// - LLVM runtimes (compiler-rt, libunwind, libc++abi, libc++)
//
// Host: Wolfi (glibc) — consistent with GlibcStage0 and GlibcStage1.
// Wolfi's native Clang builds glibc directly since both the host
// and target are glibc.
//
// Usage:
//
//	dagger call glibc-sysroot
//	dagger call glibc-sysroot export --path=./sysroot
func (m *TerranoxBootstrap) GlibcSysroot(
	// LLVM version to build runtimes from
	// +default="21.1.8"
	llvmVersion string,
	// glibc version
	// +default="2.41"
	glibcVersion string,
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

	glibcSrc := m.FetchGlibc(glibcVersion)
	headers := m.FetchLinuxHeaders(linuxVersion)

	// glibc build host triple — must use a standard triple that glibc's
	// config.sub recognizes. The custom x86_64-pc-terranox-gnu triple is
	// rejected by config.sub ("Kernel 'terranox' not known to work with
	// OS 'gnu'"). The ABI is identical to x86_64-pc-linux-gnu.
	glibcHostTriple := "x86_64-pc-linux-gnu"

	// Step 1 + 2: Kernel headers + glibc headers + full glibc build
	// glibc cannot be compiled with Clang due to asm label restrictions
	// ("cannot apply asm label to function after its first use").
	// Use Wolfi's GCC to build glibc — this is standard practice even
	// in Clang-first systems (ChromeOS, Android, etc.).
	sysrootWithGlibc := dag.Container().
		From("cgr.dev/chainguard/wolfi-base:latest").
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "make", "bison", "gawk", "python3", "perl", "file",
			"gcc", "binutils",
			"glibc-dev", "linux-headers"}).
		WithDirectory("/opt/terranox-glibc/sysroot", headers).
		WithDirectory("/tmp/glibc-src", glibcSrc).
		WithWorkdir("/tmp/glibc-build").
		// Build glibc with GCC (glibc's native compiler)
		WithExec([]string{"sh", "-c",
			`CC=gcc CXX=g++ ` +
				`AR=ar RANLIB=ranlib ` +
				`CFLAGS="-O2" ` +
				`../glibc-src/configure ` +
				`--prefix=/usr ` +
				`--host=` + glibcHostTriple + ` ` +
				`--build=x86_64-linux-gnu ` +
				`--with-headers=/opt/terranox-glibc/sysroot/usr/include ` +
				`--disable-werror ` +
				`--disable-multilib ` +
				`--enable-kernel=6.1`}).
		WithExec([]string{"make", fmt.Sprintf("-j%d", 4)}).
		WithExec([]string{"make", "install",
			"DESTDIR=/opt/terranox-glibc/sysroot"}).
		// Consolidate glibc library layout. glibc on x86_64 installs to:
		//   lib64/          - shared libraries (libc.so.6, ld-linux-x86-64.so.2)
		//   usr/lib64/      - static libs (.a), CRT objects (.o), linker scripts
		// But Clang with --sysroot looks in usr/lib/. Merge everything into
		// usr/lib/ so the toolchain can find all libraries and CRT objects.
		WithExec([]string{"sh", "-c", `
			cd /opt/terranox-glibc/sysroot
			mkdir -p usr/lib

			# Copy shared libs from lib64/ into usr/lib/
			for f in lib64/*.so* lib64/ld-*; do
				[ -e "$f" ] && cp -a "$f" usr/lib/ 2>/dev/null || true
			done

			# Copy static libs, CRT objects, and linker scripts from usr/lib64/
			for f in usr/lib64/*; do
				[ -e "$f" ] && cp -a "$f" usr/lib/ 2>/dev/null || true
			done

			# Fix libc.so linker script to use sysroot-relative paths
			if [ -f usr/lib/libc.so ]; then
				sed -i 's|/usr/lib64/|/usr/lib/|g; s|/lib64/|/usr/lib/|g' usr/lib/libc.so
			fi

			# Ensure dynamic linker is in standard location
			mkdir -p lib64
			cp -a usr/lib/ld-linux-x86-64.so.2 lib64/ 2>/dev/null || true
		`}).
		Directory("/opt/terranox-glibc/sysroot")

	// Step 3: Build LLVM runtimes against the glibc sysroot
	runtimesBuild := dag.Container().
		From("cgr.dev/chainguard/wolfi-base:latest").
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "cmake", "ninja", "python3", "file",
			"clang-" + WolfiClangVersion,
			"lld-" + WolfiClangVersion,
			"llvm-" + WolfiClangVersion}).
		WithDirectory("/opt/terranox-glibc/sysroot", sysrootWithGlibc).
		WithDirectory("/tmp/llvm-project", llvmSrc).
		WithWorkdir("/tmp/build-runtimes-glibc").
		// Build runtimes targeting standard linux-gnu triple (matches glibc sysroot)
		WithExec([]string{"cmake", "-G", "Ninja",
			"-DCMAKE_BUILD_TYPE=Release",
			"-DCMAKE_INSTALL_PREFIX=/opt/terranox-glibc/sysroot/usr",
			"-DCMAKE_C_COMPILER=clang",
			"-DCMAKE_CXX_COMPILER=clang++",
			"-DCMAKE_C_COMPILER_TARGET=" + glibcHostTriple,
			"-DCMAKE_CXX_COMPILER_TARGET=" + glibcHostTriple,
			"-DCMAKE_ASM_COMPILER_TARGET=" + glibcHostTriple,
			"-DCMAKE_SYSROOT=/opt/terranox-glibc/sysroot",
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
			"-DLIBCXX_ENABLE_STATIC=ON",
			"-DLIBCXX_ENABLE_SHARED=OFF",
			"-DLIBCXX_CXX_ABI=libcxxabi",
			"/tmp/llvm-project/runtimes"}).
		WithExec([]string{"sh", "-c", "ninja -j$(nproc)"}).
		WithExec([]string{"ninja", "install"}).
		// Create libgcc compatibility shim
		WithExec([]string{"sh", "-c",
			`echo 'INPUT(-lclang_rt.builtins -lunwind)' > /opt/terranox-glibc/sysroot/usr/lib/libgcc_s.so`}).
		Directory("/opt/terranox-glibc/sysroot")

	return runtimesBuild
}

// ═══════════════════════════════════════════════════════════
// Stage 1: Self-hosted Clang (Wolfi/glibc host)
// ═══════════════════════════════════════════════════════════

// GlibcStage1 builds the self-hosted Clang linked against glibc.
// This is the final compiler for the glibc variant with zero GNU dependencies.
//
// Host: Wolfi (glibc)
// Compiler: Stage0 cross-compiler (built or pre-built)
// Links against: Sysroot (glibc + LLVM runtimes, built or pre-built)
// Output: /opt/terranox-glibc/tools (self-hosted Clang + LLD)
//
// Pre-built Stage0 and Sysroot can be passed in to skip rebuilding:
//
//	dagger call glibc-stage1 --stage0=./cached-stage0 --sysroot=./cached-sysroot
//	dagger call glibc-stage1 export --path=./stage1
func (m *TerranoxBootstrap) GlibcStage1(
	// +default="21.1.8"
	llvmVersion string,
	// +default="2.41"
	glibcVersion string,
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
		crossTools = m.GlibcStage0(llvmVersion, source)
	}

	sysrootDir := sysroot
	if sysrootDir == nil {
		sysrootDir = m.GlibcSysroot(llvmVersion, glibcVersion, linuxVersion, source)
	}

	// Build in Wolfi (glibc) so build tools can execute natively
	return dag.Container().
		From("cgr.dev/chainguard/wolfi-base:latest").
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "cmake", "ninja", "samurai", "python3",
			"glibc-dev", "linux-headers", "file", "ccache"}).
		// Mount persistent ccache volume for glibc Stage1 compilation cache.
		WithMountedCache("/ccache", dag.CacheVolume("ccache-glibc-stage1")).
		WithEnvVariable("CCACHE_DIR", "/ccache").
		WithEnvVariable("CCACHE_MAXSIZE", "5G").
		WithEnvVariable("CCACHE_COMPRESS", "true").
		WithDirectory("/opt/terranox-glibc/cross-tools", crossTools).
		WithDirectory("/opt/terranox-glibc/sysroot", sysrootDir).
		WithDirectory("/tmp/llvm-project", llvmSrc).
		// Copy runtime libraries to Stage0's resource directory.
		// Stage0's default target is x86_64-pc-terranox-gnu, so Clang looks
		// for runtimes under that triple name. The sysroot runtimes were built
		// with x86_64-pc-linux-gnu target, so we copy them to both paths.
		WithExec([]string{"sh", "-c", `
			RTDIR=/opt/terranox-glibc/cross-tools/lib/clang/21/lib

			# Copy to linux-gnu path (matches sysroot runtime target)
			mkdir -p $RTDIR/x86_64-pc-linux-gnu
			cp -a /opt/terranox-glibc/sysroot/usr/lib/linux/* \
				$RTDIR/x86_64-pc-linux-gnu/ 2>/dev/null || true
			cp /opt/terranox-glibc/sysroot/usr/lib/crt*.o \
				$RTDIR/x86_64-pc-linux-gnu/ 2>/dev/null || true

			# Create symlinks for expected filenames
			cd $RTDIR/x86_64-pc-linux-gnu
			ln -sf libclang_rt.builtins-x86_64.a libclang_rt.builtins.a 2>/dev/null || true
			ln -sf clang_rt.crtbegin-x86_64.o crtbeginS.o 2>/dev/null || true
			ln -sf clang_rt.crtbegin-x86_64.o crtbegin.o 2>/dev/null || true
			ln -sf clang_rt.crtend-x86_64.o crtendS.o 2>/dev/null || true
			ln -sf clang_rt.crtend-x86_64.o crtend.o 2>/dev/null || true

			# Symlink terranox-gnu → linux-gnu so Stage0 finds runtimes
			# under its default target triple
			ln -sf x86_64-pc-linux-gnu $RTDIR/x86_64-pc-terranox-gnu
		`}).
		WithEnvVariable("PATH",
			"/opt/terranox-glibc/cross-tools/bin:/usr/bin:/bin").
		WithWorkdir("/tmp/build-stage1-glibc").
		WithExec([]string{"cmake", "-G", "Ninja",
			"-DCMAKE_BUILD_TYPE=Release",
			"-DCMAKE_INSTALL_PREFIX=/opt/terranox-glibc/tools",
			"-DCMAKE_C_COMPILER=/opt/terranox-glibc/cross-tools/bin/clang",
			"-DCMAKE_CXX_COMPILER=/opt/terranox-glibc/cross-tools/bin/clang++",
			"-DCMAKE_C_COMPILER_LAUNCHER=ccache",
			"-DCMAKE_CXX_COMPILER_LAUNCHER=ccache",
			// Stage0's default target is x86_64-pc-terranox-gnu but we're building
			// on a Wolfi (linux-gnu) host. Override target to match sysroot ABI.
			"-DCMAKE_C_FLAGS=--target=x86_64-pc-linux-gnu --sysroot=/opt/terranox-glibc/sysroot",
			"-DCMAKE_CXX_FLAGS=--target=x86_64-pc-linux-gnu --sysroot=/opt/terranox-glibc/sysroot -stdlib=libc++",
			// Add -L flag to tell linker where to find libc++abi and libunwind
			"-DCMAKE_EXE_LINKER_FLAGS=-fuse-ld=lld -L/opt/terranox-glibc/sysroot/usr/lib -lc++abi -lunwind",
			"-DCMAKE_SHARED_LINKER_FLAGS=-fuse-ld=lld -L/opt/terranox-glibc/sysroot/usr/lib -lc++abi -lunwind",
			"-DCMAKE_CROSSCOMPILING=FALSE",
			"-DLLVM_ENABLE_PROJECTS=clang;lld",
			"-DLLVM_ENABLE_RUNTIMES=",
			"-DLLVM_TARGETS_TO_BUILD=X86",
			"-DLLVM_DEFAULT_TARGET_TRIPLE=" + TargetGlibc,
			"-DLLVM_HOST_TRIPLE=" + TargetGlibc,
			"-DCLANG_DEFAULT_LINKER=lld",
			"-DCLANG_DEFAULT_CXX_STDLIB=libc++",
			"-DCLANG_DEFAULT_RTLIB=compiler-rt",
			"-DCLANG_DEFAULT_UNWINDLIB=libunwind",
			"-DDEFAULT_SYSROOT=/opt/terranox-glibc/sysroot",
			"-DLLVM_INSTALL_TOOLCHAIN_ONLY=ON",
			"-DLLVM_INCLUDE_TESTS=OFF",
			"-DLLVM_INCLUDE_EXAMPLES=OFF",
			"-DLLVM_INCLUDE_BENCHMARKS=OFF",
			"-DLLVM_PARALLEL_LINK_JOBS=2",
			"/tmp/llvm-project/llvm"}).
		WithExec([]string{"sh", "-c", "ninja -j$(nproc)"}).
		WithExec([]string{"ninja", "install"}).
		Directory("/opt/terranox-glibc/tools")
}

// ═══════════════════════════════════════════════════════════
// Bootstrap: Full glibc toolchain assembly
// ═══════════════════════════════════════════════════════════

// GlibcBootstrap builds the complete glibc-based toolchain and
// packages it into a single output directory with a manifest.
//
// Pre-built Stage0 and Sysroot can be passed in to skip rebuilding.
// This enables incremental CI where earlier stages are cached as
// artifacts between jobs.
//
// Pipeline:
//
//	Wolfi seed ──→ Stage0 (cross-compiler)
//	Wolfi seed ──→ Sysroot (glibc + LLVM runtimes)
//	Stage0 + Sysroot ──→ Stage1 (self-hosted glibc Clang)
//
// Output structure:
//
//	tools/     - Stage1 Clang, LLD, llvm-ar, etc.
//	sysroot/   - glibc headers/libs, LLVM runtimes, kernel headers
//	manifest.json - Build metadata
//
// Usage:
//
//	dagger call glibc-bootstrap
//	dagger call glibc-bootstrap --stage0=./cached-stage0 --sysroot=./cached-sysroot
//	dagger call glibc-bootstrap export --path=./toolchain
func (m *TerranoxBootstrap) GlibcBootstrap(
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
		stage0Dir = m.GlibcStage0(llvmVersion, source)
	}

	sysrootDir := sysroot
	if sysrootDir == nil {
		sysrootDir = m.GlibcSysroot(llvmVersion, DefaultGlibcVersion, DefaultLinuxVersion, source)
	}

	// Step 2: Build Stage1 using pre-built or freshly built artifacts
	tools := m.GlibcStage1(llvmVersion, DefaultGlibcVersion, DefaultLinuxVersion, stage0Dir, sysrootDir, source)

	// Package into a single output directory
	return dag.Directory().
		WithDirectory("tools", tools).
		WithDirectory("sysroot", sysrootDir).
		WithNewFile("manifest.json", fmt.Sprintf(`{
	"genesis_bootstrap": "0.3.0",
	"llvm_version": "%s",
	"glibc_version": "%s",
	"linux_version": "%s",
	"target": "%s",
	"variant": "glibc"
}`, llvmVersion, DefaultGlibcVersion, DefaultLinuxVersion, TargetGlibc)), nil
}
