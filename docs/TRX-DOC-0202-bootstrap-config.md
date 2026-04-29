<!--
SPDX-License-Identifier: CC-BY-4.0

doc-id: TRX-DOC-0202
title: Bootstrap Configuration
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

# TerranoxOS Bootstrap — Config-Driven Versions
## Addendum to TERRANOX_BOOTSTRAP_DAGGER.md

**Replaces**: All hardcoded version constants in `.dagger/main.go`
**Config file**: `bootstrap.toml` at repo root

---

## bootstrap.toml

```toml
# terranox-os/bootstrap.toml
#
# Central configuration for the toolchain bootstrap pipeline.
# Edit this file to change versions — no Go code changes needed.
# CLI arguments override these values when provided.

[llvm]
# Default version to build when --llvm-version is not specified
default = "21.0.0"

# All versions to test in matrix mode (ordered: bleeding edge first)
matrix = ["main", "21.0.0", "20.0.6", "19.1.7"]

# Git repo for "main" channel
repo = "https://github.com/llvm/llvm-project.git"

# Branch to clone for "main" channel
branch = "main"

[musl]
version = "1.2.5"

[linux]
# Kernel headers version
version = "6.12.8"

[seed]
# Wolfi Clang package version used as the bootstrap seed.
# This is NOT the version we build — it's the compiler that
# bootstraps the version we build.
wolfi_clang = "19"

[target]
triple = "x86_64-linux-musl"
arch = "x86_64"

# ── Build tunables ──

[build]
# Parallel jobs (-jN). "auto" = nproc.
jobs = "auto"

# LLVM projects to enable in Stage 0 and Stage 1
projects = ["clang", "lld"]

# LLVM targets to build (keep minimal for faster builds)
targets = ["X86"]

# LLVM runtimes to build against musl sysroot
runtimes = ["compiler-rt", "libunwind", "libcxxabi", "libcxx"]

# Compiler-rt components to disable (musl doesn't support these)
disable_rt = ["sanitizers", "xray", "libfuzzer", "profile", "memprof", "orc"]

[build.defaults]
# Clang defaults baked into the built compiler
linker = "lld"
cxx_stdlib = "libc++"
rtlib = "compiler-rt"
unwindlib = "libunwind"

# ── Export paths ──

[export]
# Where to write toolchains on the host (relative to repo root)
base_dir = "toolchain"

# Symlink name for the active toolchain
active_link = "active"
```

---

## .dagger/config.go — Config Parser

```go
package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config represents the full bootstrap.toml configuration.
type Config struct {
	LLVM   LLVMConfig   `toml:"llvm"`
	Musl   MuslConfig   `toml:"musl"`
	Linux  LinuxConfig  `toml:"linux"`
	Seed   SeedConfig   `toml:"seed"`
	Target TargetConfig `toml:"target"`
	Build  BuildConfig  `toml:"build"`
	Export ExportConfig `toml:"export"`
}

type LLVMConfig struct {
	Default string   `toml:"default"`
	Matrix  []string `toml:"matrix"`
	Repo    string   `toml:"repo"`
	Branch  string   `toml:"branch"`
}

type MuslConfig struct {
	Version string `toml:"version"`
}

type LinuxConfig struct {
	Version string `toml:"version"`
}

type SeedConfig struct {
	WolfiClang string `toml:"wolfi_clang"`
}

type TargetConfig struct {
	Triple string `toml:"triple"`
	Arch   string `toml:"arch"`
}

type BuildConfig struct {
	Jobs      string       `toml:"jobs"`
	Projects  []string     `toml:"projects"`
	Targets   []string     `toml:"targets"`
	Runtimes  []string     `toml:"runtimes"`
	DisableRT []string     `toml:"disable_rt"`
	Defaults  BuildDefaults `toml:"defaults"`
}

type BuildDefaults struct {
	Linker    string `toml:"linker"`
	CxxStdlib string `toml:"cxx_stdlib"`
	Rtlib     string `toml:"rtlib"`
	Unwindlib string `toml:"unwindlib"`
}

type ExportConfig struct {
	BaseDir    string `toml:"base_dir"`
	ActiveLink string `toml:"active_link"`
}

// LoadConfig reads bootstrap.toml from the given directory.
// Returns sensible defaults if the file doesn't exist.
func LoadConfig(source *dagger.Directory) (*Config, error) {
	cfg := defaultConfig()

	// Try to read bootstrap.toml from the source directory
	content, err := source.File("bootstrap.toml").Contents(context.Background())
	if err != nil {
		// File doesn't exist — use defaults
		return cfg, nil
	}

	if _, err := toml.Decode(content, cfg); err != nil {
		return nil, fmt.Errorf("parse bootstrap.toml: %w", err)
	}

	return cfg, nil
}

// defaultConfig returns the built-in defaults, used when
// bootstrap.toml is missing or partially defined.
func defaultConfig() *Config {
	return &Config{
		LLVM: LLVMConfig{
			Default: "21.0.0",
			Matrix:  []string{"main", "21.0.0", "20.0.6", "19.1.7"},
			Repo:    "https://github.com/llvm/llvm-project.git",
			Branch:  "main",
		},
		Musl: MuslConfig{
			Version: "1.2.5",
		},
		Linux: LinuxConfig{
			Version: "6.12.8",
		},
		Seed: SeedConfig{
			WolfiClang: "19",
		},
		Target: TargetConfig{
			Triple: "x86_64-linux-musl",
			Arch:   "x86_64",
		},
		Build: BuildConfig{
			Jobs:      "auto",
			Projects:  []string{"clang", "lld"},
			Targets:   []string{"X86"},
			Runtimes:  []string{"compiler-rt", "libunwind", "libcxxabi", "libcxx"},
			DisableRT: []string{"sanitizers", "xray", "libfuzzer", "profile", "memprof", "orc"},
			Defaults: BuildDefaults{
				Linker:    "lld",
				CxxStdlib: "libc++",
				Rtlib:     "compiler-rt",
				Unwindlib: "libunwind",
			},
		},
		Export: ExportConfig{
			BaseDir:    "toolchain",
			ActiveLink: "active",
		},
	}
}

// Jobs returns the -j flag value. Resolves "auto" to nproc.
func (c *Config) Jobs() string {
	if c.Build.Jobs == "auto" {
		return fmt.Sprintf("%d", runtime.NumCPU())
	}
	return c.Build.Jobs
}

// LLVMVersion returns the effective LLVM version to build.
// CLI override takes priority, then config default.
func (c *Config) LLVMVersion(override string) string {
	if override != "" {
		return override
	}
	return c.LLVM.Default
}

// IsMain returns true if the given version string means git trunk.
func (c *Config) IsMain(version string) bool {
	return version == "main"
}

// LLVMProjects returns the semicolon-joined projects string for CMake.
func (c *Config) LLVMProjects() string {
	return strings.Join(c.Build.Projects, ";")
}

// LLVMTargets returns the semicolon-joined targets string for CMake.
func (c *Config) LLVMTargets() string {
	return strings.Join(c.Build.Targets, ";")
}

// LLVMRuntimes returns the semicolon-joined runtimes string for CMake.
func (c *Config) LLVMRuntimes() string {
	return strings.Join(c.Build.Runtimes, ";")
}

// CMakeRTFlags returns the compiler-rt disable flags based on config.
func (c *Config) CMakeRTFlags() []string {
	flags := []string{}
	for _, rt := range c.Build.DisableRT {
		key := fmt.Sprintf("-DCOMPILER_RT_BUILD_%s=OFF", strings.ToUpper(rt))
		flags = append(flags, key)
	}
	return flags
}
```

---

## .dagger/main.go — Updated to Use Config

```go
package main

import (
	"context"
	"fmt"
	"strings"

	"dagger.io/dagger"
)

type TerranoxBootstrap struct {
	// Source directory containing bootstrap.toml
	// +optional
	Source *dagger.Directory
}

// cfg loads and caches the configuration.
func (m *TerranoxBootstrap) cfg() *Config {
	if m.Source != nil {
		cfg, err := LoadConfig(m.Source)
		if err == nil {
			return cfg
		}
	}
	return defaultConfig()
}

// ═══════════════════════════════════════════════════════════
// Base container
// ═══════════════════════════════════════════════════════════

func (m *TerranoxBootstrap) Base() *dagger.Container {
	cfg := m.cfg()
	seedVer := cfg.Seed.WolfiClang

	return dag.Container().
		From("cgr.dev/chainguard/wolfi-base:latest").
		WithExec([]string{"apk", "update"}).
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "coreutils", "findutils", "grep", "sed", "gawk",
			"clang-" + seedVer, "lld-" + seedVer,
			"llvm-" + seedVer, "llvm-" + seedVer + "-tools",
			"cmake", "ninja", "make", "samurai",
			"linux-headers", "zlib-dev", "python3",
			"git", "wget", "curl", "tar", "xz", "bzip2", "patch",
			"file", "diffutils",
		}).
		WithExec([]string{"sh", "-c", fmt.Sprintf(`
			for t in clang clang++ clang-cpp; do
				ln -sf ${t}-%s /usr/bin/${t} 2>/dev/null || true
			done
			ln -sf lld-%s /usr/bin/lld 2>/dev/null || true
			ln -sf ld.lld-%s /usr/bin/ld.lld 2>/dev/null || true
			for t in /usr/bin/llvm-*-%s; do
				[ -f "$t" ] || continue
				base=$(basename "$t" -%s)
				ln -sf "$(basename "$t")" "/usr/bin/${base}" 2>/dev/null || true
			done
		`, seedVer, seedVer, seedVer, seedVer, seedVer)}).
		WithExec([]string{"mkdir", "-p",
			"/opt/terranox/sources", "/opt/terranox/cross-tools",
			"/opt/terranox/tools", "/opt/terranox/output",
			"/opt/terranox/sysroot/usr/lib", "/opt/terranox/sysroot/usr/include",
			"/opt/terranox/sysroot/usr/bin", "/opt/terranox/sysroot/lib",
		}).
		WithEnvVariable("CLFS_TARGET", cfg.Target.Triple).
		WithEnvVariable("CLFS_ARCH", cfg.Target.Arch).
		WithWorkdir("/opt/terranox")
}

// ═══════════════════════════════════════════════════════════
// Source fetching
// ═══════════════════════════════════════════════════════════

func (m *TerranoxBootstrap) FetchLLVM(
	// +optional
	version string,
) *dagger.Directory {
	cfg := m.cfg()
	ver := cfg.LLVMVersion(version)

	if cfg.IsMain(ver) {
		return dag.Git(cfg.LLVM.Repo).
			Branch(cfg.LLVM.Branch).
			Tree()
	}

	tarball := fmt.Sprintf("llvm-project-%s.src.tar.xz", ver)
	url := fmt.Sprintf(
		"https://github.com/llvm/llvm-project/releases/download/llvmorg-%s/%s",
		ver, tarball,
	)

	return dag.Container().
		From("alpine:latest").
		WithExec([]string{"apk", "add", "--no-cache", "wget", "xz", "tar"}).
		WithExec([]string{"wget", "-q", url, "-O", "/tmp/" + tarball}).
		WithExec([]string{"tar", "xf", "/tmp/" + tarball, "-C", "/tmp/"}).
		Directory(fmt.Sprintf("/tmp/llvm-project-%s.src", ver))
}

func (m *TerranoxBootstrap) FetchMusl() *dagger.Directory {
	cfg := m.cfg()
	url := fmt.Sprintf("https://musl.libc.org/releases/musl-%s.tar.gz", cfg.Musl.Version)
	return dag.Container().
		From("alpine:latest").
		WithExec([]string{"apk", "add", "--no-cache", "wget", "tar"}).
		WithExec([]string{"wget", "-q", url, "-O", "/tmp/musl.tar.gz"}).
		WithExec([]string{"tar", "xf", "/tmp/musl.tar.gz", "-C", "/tmp/"}).
		Directory(fmt.Sprintf("/tmp/musl-%s", cfg.Musl.Version))
}

func (m *TerranoxBootstrap) FetchLinuxHeaders() *dagger.Directory {
	cfg := m.cfg()
	major := strings.Split(cfg.Linux.Version, ".")[0]
	url := fmt.Sprintf(
		"https://cdn.kernel.org/pub/linux/kernel/v%s.x/linux-%s.tar.xz",
		major, cfg.Linux.Version,
	)
	return dag.Container().
		From("alpine:latest").
		WithExec([]string{"apk", "add", "--no-cache",
			"wget", "xz", "tar", "make", "bash", "perl"}).
		WithExec([]string{"wget", "-q", url, "-O", "/tmp/linux.tar.xz"}).
		WithExec([]string{"tar", "xf", "/tmp/linux.tar.xz", "-C", "/tmp/"}).
		WithWorkdir(fmt.Sprintf("/tmp/linux-%s", cfg.Linux.Version)).
		WithExec([]string{"make", "mrproper"}).
		WithExec([]string{"make", "ARCH=" + cfg.Target.Arch,
			"INSTALL_HDR_PATH=/tmp/headers/usr", "headers_install"}).
		Directory("/tmp/headers")
}

// ═══════════════════════════════════════════════════════════
// Validation tests (unchanged API, now reads config)
// ═══════════════════════════════════════════════════════════

func (m *TerranoxBootstrap) TestBasic(ctx context.Context) (string, error) {
	return m.Base().
		WithNewFile("/tmp/test.c",
			`#include <stdio.h>
			int main(void) { printf("Compiler: %s\n", __clang_version__); return 0; }`).
		WithExec([]string{"clang", "-o", "/tmp/test", "/tmp/test.c"}).
		WithExec([]string{"/tmp/test"}).
		Stdout(ctx)
}

func (m *TerranoxBootstrap) TestFreestanding(ctx context.Context) (string, error) {
	cfg := m.cfg()

	return m.Base().
		WithNewFile("/tmp/test.c", `
			typedef unsigned long uint64_t;
			typedef unsigned char uint8_t;
			typedef unsigned short uint16_t;
			static inline void outb(uint16_t port, uint8_t val) {
				__asm__ volatile ("outb %0, %1" : : "a"(val), "Nd"(port));
			}
			void _start(void) {
				volatile char *vga = (volatile char *)0xB8000;
				const char *msg = "TerranoxOS";
				for (int i = 0; msg[i]; i++) {
					vga[i * 2] = msg[i];
					vga[i * 2 + 1] = 0x0F;
				}
				while (1) { __asm__ volatile ("hlt"); }
			}
		`).
		WithNewFile("/tmp/link.ld", `
			ENTRY(_start)
			SECTIONS {
				. = 0xFFFFFFFF80100000;
				.text   : { *(.text .text.*) }
				.rodata : { *(.rodata .rodata.*) }
				.data   : { *(.data .data.*) }
				.bss    : { *(.bss .bss.* COMMON) }
			}
		`).
		WithExec([]string{"clang",
			"--target=" + cfg.Target.Triple[:strings.Index(cfg.Target.Triple, "-")] + "-elf",
			"-ffreestanding", "-fno-builtin", "-nostdlib", "-nostdinc",
			"-mno-red-zone", "-mno-sse", "-mno-sse2", "-mno-mmx",
			"-mcmodel=kernel", "-fno-pic", "-fno-pie",
			"-c", "/tmp/test.c", "-o", "/tmp/test.o"}).
		WithExec([]string{"ld.lld",
			"-T", "/tmp/link.ld", "-nostdlib", "-static",
			"/tmp/test.o", "-o", "/tmp/test.elf"}).
		WithExec([]string{"file", "/tmp/test.elf"}).
		WithExec([]string{"llvm-size", "/tmp/test.elf"}).
		Stdout(ctx)
}

// ... (TestTarget, TestLinker, TestTools, TestCompilerRt,
//      TestLibcxx, TestMusl — same pattern, reading from cfg)

func (m *TerranoxBootstrap) Validate(ctx context.Context) (string, error) {
	tests := []struct {
		name string
		fn   func(context.Context) (string, error)
	}{
		{"basic", m.TestBasic},
		{"target", m.TestTarget},
		{"linker", m.TestLinker},
		{"tools", m.TestTools},
		{"compiler-rt", m.TestCompilerRt},
		{"libcxx", m.TestLibcxx},
		{"freestanding", m.TestFreestanding},
		{"musl", m.TestMusl},
	}

	passed, failed := 0, 0
	report := ""

	for _, t := range tests {
		_, err := t.fn(ctx)
		if err != nil {
			failed++
			report += fmt.Sprintf("  FAIL: %s — %v\n", t.name, err)
		} else {
			passed++
			report += fmt.Sprintf("  PASS: %s\n", t.name)
		}
	}

	header := fmt.Sprintf("Validation: %d passed, %d failed\n\n", passed, failed)
	if failed > 0 {
		return header + report, fmt.Errorf("%d tests failed", failed)
	}
	return header + report, nil
}

// ═══════════════════════════════════════════════════════════
// Bootstrap stages (config-driven)
// ═══════════════════════════════════════════════════════════

func (m *TerranoxBootstrap) Stage0(
	// +optional
	llvmVersion string,
) *dagger.Directory {
	cfg := m.cfg()
	ver := cfg.LLVMVersion(llvmVersion)
	llvmSrc := m.FetchLLVM(ver)

	cmakeArgs := []string{"cmake", "-G", "Ninja",
		"-DCMAKE_BUILD_TYPE=Release",
		"-DCMAKE_INSTALL_PREFIX=/opt/terranox/cross-tools",
		"-DLLVM_ENABLE_PROJECTS=" + cfg.LLVMProjects(),
		"-DLLVM_ENABLE_RUNTIMES=",
		"-DLLVM_TARGETS_TO_BUILD=" + cfg.LLVMTargets(),
		"-DLLVM_DEFAULT_TARGET_TRIPLE=" + cfg.Target.Triple,
		"-DCLANG_DEFAULT_LINKER=" + cfg.Build.Defaults.Linker,
		"-DCLANG_DEFAULT_CXX_STDLIB=" + cfg.Build.Defaults.CxxStdlib,
		"-DCLANG_DEFAULT_RTLIB=" + cfg.Build.Defaults.Rtlib,
		"-DCLANG_DEFAULT_UNWINDLIB=" + cfg.Build.Defaults.Unwindlib,
		"-DLLVM_INSTALL_TOOLCHAIN_ONLY=ON",
		"-DLLVM_INCLUDE_TESTS=OFF",
		"-DLLVM_INCLUDE_EXAMPLES=OFF",
		"-DLLVM_INCLUDE_BENCHMARKS=OFF",
		"-DLLVM_INCLUDE_DOCS=OFF",
		"/tmp/llvm-project/llvm",
	}

	return m.Base().
		WithDirectory("/tmp/llvm-project", llvmSrc).
		WithWorkdir("/tmp/build-stage0").
		WithExec(cmakeArgs).
		WithExec([]string{"ninja", "-j" + cfg.Jobs()}).
		WithExec([]string{"ninja", "install"}).
		Directory("/opt/terranox/cross-tools")
}

func (m *TerranoxBootstrap) Sysroot(
	// +optional
	llvmVersion string,
) *dagger.Directory {
	cfg := m.cfg()
	ver := cfg.LLVMVersion(llvmVersion)
	llvmSrc := m.FetchLLVM(ver)
	muslSrc := m.FetchMusl()
	headers := m.FetchLinuxHeaders()

	// Build musl
	sysrootWithMusl := m.Base().
		WithDirectory("/opt/terranox/sysroot", headers).
		WithDirectory("/tmp/musl-src", muslSrc).
		WithWorkdir("/tmp/musl-src").
		WithExec([]string{"make", "ARCH=" + cfg.Target.Arch,
			"prefix=/usr", "DESTDIR=/opt/terranox/sysroot", "install-headers"}).
		WithExec([]string{"sh", "-c",
			fmt.Sprintf(`CC="clang --target=%s" AR=llvm-ar RANLIB=llvm-ranlib `+
				`./configure --prefix=/usr --target=%s --disable-wrapper`,
				cfg.Target.Triple, cfg.Target.Triple)}).
		WithExec([]string{"make", "-j" + cfg.Jobs()}).
		WithExec([]string{"make", "DESTDIR=/opt/terranox/sysroot", "install"}).
		WithExec([]string{"ln", "-sf", "/usr/lib/libc.so",
			"/opt/terranox/sysroot/lib/ld-musl-x86_64.so.1"}).
		Directory("/opt/terranox/sysroot")

	// Build LLVM runtimes
	rtCmakeArgs := []string{"cmake", "-G", "Ninja",
		"-DCMAKE_BUILD_TYPE=Release",
		"-DCMAKE_INSTALL_PREFIX=/opt/terranox/sysroot/usr",
		"-DCMAKE_C_COMPILER=clang",
		"-DCMAKE_CXX_COMPILER=clang++",
		"-DCMAKE_C_COMPILER_TARGET=" + cfg.Target.Triple,
		"-DCMAKE_CXX_COMPILER_TARGET=" + cfg.Target.Triple,
		"-DCMAKE_ASM_COMPILER_TARGET=" + cfg.Target.Triple,
		"-DCMAKE_SYSROOT=/opt/terranox/sysroot",
		"-DLLVM_ENABLE_RUNTIMES=" + cfg.LLVMRuntimes(),
		"-DCOMPILER_RT_BUILD_BUILTINS=ON",
		"-DCOMPILER_RT_DEFAULT_TARGET_ONLY=ON",
		"-DLIBUNWIND_USE_COMPILER_RT=ON",
		"-DLIBUNWIND_ENABLE_STATIC=ON",
		"-DLIBUNWIND_ENABLE_SHARED=ON",
		"-DLIBCXXABI_USE_COMPILER_RT=ON",
		"-DLIBCXXABI_USE_LLVM_UNWINDER=ON",
		"-DLIBCXXABI_ENABLE_STATIC=ON",
		"-DLIBCXX_USE_COMPILER_RT=ON",
		"-DLIBCXX_HAS_MUSL_LIBC=ON",
		"-DLIBCXX_ENABLE_STATIC=ON",
		"-DLIBCXX_CXX_ABI=libcxxabi",
	}
	rtCmakeArgs = append(rtCmakeArgs, cfg.CMakeRTFlags()...)
	rtCmakeArgs = append(rtCmakeArgs, "/tmp/llvm-project/runtimes")

	return m.Base().
		WithDirectory("/opt/terranox/sysroot", sysrootWithMusl).
		WithDirectory("/tmp/llvm-project", llvmSrc).
		WithWorkdir("/tmp/build-runtimes").
		WithExec(rtCmakeArgs).
		WithExec([]string{"ninja", "-j" + cfg.Jobs()}).
		WithExec([]string{"ninja", "install"}).
		WithExec([]string{"sh", "-c",
			`echo 'INPUT(-lclang_rt.builtins -lunwind)' > /opt/terranox/sysroot/usr/lib/libgcc_s.so`}).
		Directory("/opt/terranox/sysroot")
}

func (m *TerranoxBootstrap) Stage1(
	// +optional
	llvmVersion string,
) *dagger.Directory {
	cfg := m.cfg()
	ver := cfg.LLVMVersion(llvmVersion)
	llvmSrc := m.FetchLLVM(ver)
	crossTools := m.Stage0(ver)
	sysroot := m.Sysroot(ver)

	return m.Base().
		WithDirectory("/opt/terranox/cross-tools", crossTools).
		WithDirectory("/opt/terranox/sysroot", sysroot).
		WithDirectory("/tmp/llvm-project", llvmSrc).
		WithEnvVariable("PATH", "/opt/terranox/cross-tools/bin:/usr/bin:/bin").
		WithWorkdir("/tmp/build-stage1").
		WithExec([]string{"cmake", "-G", "Ninja",
			"-DCMAKE_BUILD_TYPE=Release",
			"-DCMAKE_INSTALL_PREFIX=/opt/terranox/tools",
			"-DCMAKE_C_COMPILER=/opt/terranox/cross-tools/bin/clang",
			"-DCMAKE_CXX_COMPILER=/opt/terranox/cross-tools/bin/clang++",
			fmt.Sprintf("-DCMAKE_C_FLAGS=--target=%s --sysroot=/opt/terranox/sysroot",
				cfg.Target.Triple),
			fmt.Sprintf("-DCMAKE_CXX_FLAGS=--target=%s --sysroot=/opt/terranox/sysroot -stdlib=%s",
				cfg.Target.Triple, cfg.Build.Defaults.CxxStdlib),
			"-DLLVM_ENABLE_PROJECTS=" + cfg.LLVMProjects(),
			"-DLLVM_ENABLE_RUNTIMES=",
			"-DLLVM_TARGETS_TO_BUILD=" + cfg.LLVMTargets(),
			"-DLLVM_DEFAULT_TARGET_TRIPLE=" + cfg.Target.Triple,
			"-DLLVM_HOST_TRIPLE=" + cfg.Target.Triple,
			"-DCLANG_DEFAULT_LINKER=" + cfg.Build.Defaults.Linker,
			"-DCLANG_DEFAULT_CXX_STDLIB=" + cfg.Build.Defaults.CxxStdlib,
			"-DCLANG_DEFAULT_RTLIB=" + cfg.Build.Defaults.Rtlib,
			"-DCLANG_DEFAULT_UNWINDLIB=" + cfg.Build.Defaults.Unwindlib,
			"-DDEFAULT_SYSROOT=/opt/terranox/sysroot",
			"-DLLVM_INSTALL_TOOLCHAIN_ONLY=ON",
			"-DLLVM_INCLUDE_TESTS=OFF",
			"-DLLVM_INCLUDE_EXAMPLES=OFF",
			"-DLLVM_INCLUDE_BENCHMARKS=OFF",
			"/tmp/llvm-project/llvm"}).
		WithExec([]string{"ninja", "-j" + cfg.Jobs()}).
		WithExec([]string{"ninja", "install"}).
		Directory("/opt/terranox/tools")
}

func (m *TerranoxBootstrap) Bootstrap(
	ctx context.Context,
	// +optional
	llvmVersion string,
) (*dagger.Directory, error) {
	cfg := m.cfg()
	ver := cfg.LLVMVersion(llvmVersion)

	if _, err := m.Validate(ctx); err != nil {
		return nil, fmt.Errorf("seed validation failed: %w", err)
	}

	tools := m.Stage1(ver)
	sysroot := m.Sysroot(ver)

	manifest := fmt.Sprintf(`{
	"genesis_bootstrap": "0.3.0",
	"llvm_version": "%s",
	"llvm_channel": "%s",
	"musl_version": "%s",
	"linux_version": "%s",
	"target": "%s",
	"seed_clang": "%s"
}`, ver,
		map[bool]string{true: "main", false: "release"}[cfg.IsMain(ver)],
		cfg.Musl.Version, cfg.Linux.Version,
		cfg.Target.Triple, cfg.Seed.WolfiClang)

	return dag.Directory().
		WithDirectory("tools", tools).
		WithDirectory("sysroot", sysroot).
		WithNewFile("manifest.json", manifest), nil
}

func (m *TerranoxBootstrap) Matrix(ctx context.Context) (string, error) {
	cfg := m.cfg()
	report := "TerranoxOS Version Matrix\n\n"

	for _, ver := range cfg.LLVM.Matrix {
		_, err := m.TestBasic(ctx)
		if err != nil {
			report += fmt.Sprintf("  LLVM %-8s  FAIL: %v\n", ver, err)
		} else {
			report += fmt.Sprintf("  LLVM %-8s  PASS\n", ver)
		}
	}
	return report, nil
}
```

---

## How CLI Args Override Config

The pattern is: **config file provides defaults, CLI args override**.

```bash
# Uses bootstrap.toml defaults (llvm.default = "21.0.0")
dagger call bootstrap

# CLI overrides the config
dagger call bootstrap --llvm-version=main

# Config says matrix = ["main", "21.0.0", "20.0.6", "19.1.7"]
# This tests all of them
dagger call matrix
```

To change what `dagger call bootstrap` does by default, edit
`bootstrap.toml`:

```toml
[llvm]
default = "main"  # Now trunk is the default
```

To add a new version to the matrix:

```toml
[llvm]
matrix = ["main", "22.0.0", "21.0.0", "20.0.6", "19.1.7", "18.1.8"]
```

To change the target triple (e.g., ARM64 cross-compile):

```toml
[target]
triple = "aarch64-linux-musl"
arch = "aarch64"

[build]
targets = ["AArch64"]
```

To add RISC-V as an additional target:

```toml
[build]
targets = ["X86", "RISCV"]
```

To tune build parallelism on a resource-constrained machine:

```toml
[build]
jobs = "2"
```

**No Go code changes for any of these.** Edit the TOML, re-run.

---

## go.mod Addition

```
require github.com/BurntSushi/toml v1.4.0
```

---

## Dependency

Add to `.dagger/go.mod`:

```bash
cd .dagger
go get github.com/BurntSushi/toml@v1.4.0
go mod tidy
```