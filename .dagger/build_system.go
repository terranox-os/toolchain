// SPDX-License-Identifier: Apache-2.0

// Build system functions for kernel, strata, and system images.
//
// Restored from commit 8ba519e — these were accidentally dropped
// in commit 3cc5423 (glibc variant) which truncated main.go.
//
// Functions:
//   - ToolchainImage: Package toolchain as OCI container image
//   - KernelBuild: Build TerranoxOS kernel with custom toolchain
//   - LinuxKernelBuild: Build Linux kernel with Clang
//   - LinuxKernelImage: Package kernel into bootable image
//   - BuildStrata: Assemble strata from package manifests
//   - AggregateSboms: Aggregate SBOMs across all packages
//   - BuildCoreSystem: Assemble /core base system

package main

import (
	"context"

	"dagger/terranox-bootstrap/internal/dagger"
)

// ═══════════════════════════════════════════════════════════
// Container Image Export - OCI Images for Distribution
// ═══════════════════════════════════════════════════════════

// ToolchainImage creates a container image with the Stage1 toolchain installed.
// This allows testing in the correct musl environment and sharing between systems.
//
// Usage:
//
//	# Export as OCI tarball:
//	dagger call toolchain-image --toolchain ./toolchain/llvm \
//	  export --path ./terranox-toolchain-21.1.8.tar
//
//	# Publish to registry:
//	dagger call toolchain-image --toolchain ./toolchain/llvm \
//	  publish --address ghcr.io/terranox-os/toolchain:21.1.8
//
//	# Use image with Docker/Podman:
//	docker load < terranox-toolchain-21.1.8.tar
//	docker run -it terranox-toolchain:21.1.8 clang --version
func (m *TerranoxBootstrap) ToolchainImage(
	// Pre-built toolchain directory (output of Stage1 export)
	// +optional
	toolchain *dagger.Directory,
	// +default="21.1.8"
	llvmVersion string,
	// +default="1.2.5"
	muslVersion string,
	// +default="6.12.8"
	linuxVersion string,
	// Repository source directory containing patches/
	// +optional
	source *dagger.Directory,
) *dagger.Container {
	// If no toolchain provided, build Stage1
	if toolchain == nil {
		toolchain = m.MuslStage1(llvmVersion, muslVersion, linuxVersion, nil, nil, source)
	}

	// Build sysroot with musl libc and LLVM runtimes
	sysroot := m.MuslSysroot(llvmVersion, muslVersion, linuxVersion, source)

	// Create container image with toolchain and sysroot
	return dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache",
			"build-base", "cmake", "ninja", "git", "bash", "file"}).
		WithDirectory("/opt/terranox/toolchain", toolchain).
		WithDirectory("/opt/terranox/sysroot", sysroot).
		// Copy runtime libraries to toolchain's resource directory
		// This allows clang to find them without explicit flags
		WithExec([]string{"sh", "-c", `
			mkdir -p /opt/terranox/toolchain/lib/clang/21/lib/x86_64-unknown-linux-musl
			cp -a /opt/terranox/sysroot/usr/lib/linux/* \
				/opt/terranox/toolchain/lib/clang/21/lib/x86_64-unknown-linux-musl/
			cp /opt/terranox/sysroot/usr/lib/crt*.o \
				/opt/terranox/toolchain/lib/clang/21/lib/x86_64-unknown-linux-musl/

			# Create symlinks for expected filenames
			cd /opt/terranox/toolchain/lib/clang/21/lib/x86_64-unknown-linux-musl
			ln -sf libclang_rt.builtins-x86_64.a libclang_rt.builtins.a
			ln -sf clang_rt.crtbegin-x86_64.o crtbeginS.o
			ln -sf clang_rt.crtbegin-x86_64.o crtbegin.o
			ln -sf clang_rt.crtend-x86_64.o crtendS.o
			ln -sf clang_rt.crtend-x86_64.o crtend.o
		`}).
		WithEnvVariable("PATH", "/opt/terranox/toolchain/bin:${PATH}").
		WithEnvVariable("TERRANOX_VERSION", llvmVersion).
		WithEnvVariable("TERRANOX_SYSROOT", "/opt/terranox/sysroot").
		WithLabel("org.opencontainers.image.title", "TerranoxOS Toolchain").
		WithLabel("org.opencontainers.image.description",
			"Pure LLVM/Clang toolchain with musl libc sysroot for TerranoxOS").
		WithLabel("org.opencontainers.image.version", llvmVersion).
		WithLabel("org.opencontainers.image.vendor", "TerranoxOS").
		WithLabel("org.opencontainers.image.licenses", "Apache-2.0").
		WithLabel("terranox.llvm.version", llvmVersion).
		WithLabel("terranox.musl.version", muslVersion).
		WithLabel("terranox.target", Target).
		WithWorkdir("/workspace")
}

// KernelBuild compiles the TerranoxOS kernel using the Stage1 LLVM/Clang toolchain.
//
// This function builds the kernel from C and assembly sources, producing a bootable
// ELF binary suitable for loading with Limine bootloader.
//
// Example usage:
//
//	# Build kernel with existing toolchain:
//	dagger call kernel-build --toolchain ./toolchain/llvm export --path=./kernel.elf
//
//	# Build kernel from scratch (builds Stage1 first):
//	dagger call kernel-build export --path=./kernel.elf
func (m *TerranoxBootstrap) KernelBuild(
	// Pre-built Stage1 toolchain directory
	// If not provided, Stage1 will be built automatically
	// +optional
	toolchain *dagger.Directory,
	// LLVM version to use
	// +default="21.1.8"
	llvmVersion string,
	// musl version for sysroot
	// +default="1.2.5"
	muslVersion string,
	// Linux kernel headers version
	// +default="6.12.8"
	linuxVersion string,
	// Repository source directory containing kernel/ and patches/
	// +optional
	source *dagger.Directory,
) *dagger.Directory {
	// If no toolchain provided, build Stage1
	if toolchain == nil {
		toolchain = m.MuslStage1(llvmVersion, muslVersion, linuxVersion, nil, nil, source)
	}

	// If no source provided, use current directory
	if source == nil {
		source = dag.CurrentModule().Source()
	}

	// Kernel compilation flags (freestanding, no stdlib, kernel model)
	cflags := []string{
		"-ffreestanding",           // No hosted environment
		"-fno-builtin",             // No built-in functions
		"-nostdlib",                // No standard library
		"-nostdinc",                // No standard includes
		"-mno-red-zone",            // No red zone (required for kernel interrupt handling)
		"-mcmodel=kernel",          // Kernel code model (-2GB addressing)
		"-fno-pic",                 // No position-independent code
		"-fno-pie",                 // No position-independent executable
		"-O2",                      // Optimization level 2
		"-Wall",                    // All warnings
		"-Wextra",                  // Extra warnings
		"-Werror",                  // Treat warnings as errors
		"-fstack-protector-strong", // Stack smashing protection
		"-I/opt/terranox/toolchain/lib/clang/21/include", // Compiler built-in headers (stdint.h, stddef.h, etc.)
		"-I/workspace/kernel/boot",                       // Include path for limine.h
	}

	// Assembly flags (similar to C flags)
	asflags := []string{
		"-ffreestanding",
		"-nostdlib",
		"-mno-red-zone",
		"-mcmodel=kernel",
	}

	// Build container with toolchain
	builder := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "bash", "coreutils", "file"}).
		WithDirectory("/opt/terranox/toolchain", toolchain).
		WithDirectory("/workspace", source).
		WithWorkdir("/workspace").
		WithEnvVariable("PATH", "/opt/terranox/toolchain/bin:${PATH}")

	// Compile all C source files
	cSources := []string{
		"kernel/core/builtin.c",
		"kernel/core/console.c",
		"kernel/core/gdt.c",
		"kernel/core/heap.c",
		"kernel/core/idt.c",
		"kernel/core/keyboard.c",
		"kernel/core/klog.c",
		"kernel/core/klog_fb.c",
		"kernel/core/klog_ring.c",
		"kernel/core/klog_serial.c",
		"kernel/core/kmain.c",
		"kernel/core/kprintf.c",
		"kernel/core/panic.c",
		"kernel/core/pic.c",
		"kernel/core/pmm.c",
		"kernel/core/serial.c",
		"kernel/core/shell.c",
		"kernel/core/stack_protector.c",
	}

	// Compile each C file to object file
	for _, src := range cSources {
		obj := src[:len(src)-2] + ".o" // Replace .c with .o
		compileCmd := append([]string{"clang"}, cflags...)
		compileCmd = append(compileCmd, "-c", src, "-o", obj)
		builder = builder.WithExec(compileCmd)
	}

	// Assemble all assembly files
	asmSources := []string{
		"kernel/boot/boot.S",
		"kernel/core/gdt_load.S",
		"kernel/core/idt_load.S",
		"kernel/core/idt_stubs.S",
	}

	// Assemble each .S file to object file
	for _, src := range asmSources {
		obj := src[:len(src)-2] + ".o" // Replace .S with .o
		assembleCmd := append([]string{"clang"}, asflags...)
		assembleCmd = append(assembleCmd, "-c", src, "-o", obj)
		builder = builder.WithExec(assembleCmd)
	}

	// Collect all object files for linking
	allObjects := []string{}
	for _, src := range cSources {
		allObjects = append(allObjects, src[:len(src)-2]+".o")
	}
	for _, src := range asmSources {
		allObjects = append(allObjects, src[:len(src)-2]+".o")
	}

	// Link all object files into kernel ELF binary
	linkCmd := []string{
		"ld.lld",                      // LLVM linker
		"-T", "kernel/link/kernel.ld", // Linker script
		"-nostdlib",        // No standard library
		"-o", "kernel.elf", // Output kernel binary
	}
	linkCmd = append(linkCmd, allObjects...)
	builder = builder.WithExec(linkCmd)

	// Return directory containing kernel.elf
	return builder.Directory("/workspace")
}

// LinuxKernelBuild compiles the mainline Linux kernel using LLVM/Clang toolchain.
//
// This function builds a production Linux kernel with LLVM=1 for the bootable TerranoxOS system.
// This is separate from the custom TerranoxOS kernel - this is the real Linux kernel.
//
// Example usage:
//
//	# Build Linux kernel with existing toolchain:
//	dagger call linux-kernel-build --toolchain ./toolchain/llvm export --path=./linux-build
//
//	# Build from scratch (builds Stage1 first):
//	dagger call linux-kernel-build export --path=./linux-build
func (m *TerranoxBootstrap) LinuxKernelBuild(
	// Pre-built Stage1 toolchain directory
	// If not provided, Stage1 will be built automatically
	// +optional
	toolchain *dagger.Directory,
	// LLVM version to use
	// +default="21.1.8"
	llvmVersion string,
	// musl version for sysroot
	// +default="1.2.5"
	muslVersion string,
	// Linux kernel version to build
	// +default="6.12.8"
	linuxVersion string,
	// Repository source directory (not used, but kept for consistency)
	// +optional
	source *dagger.Directory,
) *dagger.Directory {
	// If no toolchain provided, build Stage1
	if toolchain == nil {
		toolchain = m.MuslStage1(llvmVersion, muslVersion, linuxVersion, nil, nil, source)
	}

	// Download and extract Linux kernel source using Dagger HTTP
	kernelURL := "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-" + linuxVersion + ".tar.xz"

	// Build container with toolchain and build dependencies
	builder := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "bc", "bison", "build-base", "coreutils", "diffutils", "elfutils-dev",
			"findutils", "flex", "gmp-dev", "linux-headers", "make", "mpc1-dev",
			"mpfr-dev", "musl-dev", "ncurses-dev", "openssl-dev", "perl", "python3",
			"rsync", "wget", "xz"}).
		WithDirectory("/opt/terranox/toolchain", toolchain).
		WithEnvVariable("PATH", "/opt/terranox/toolchain/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin").
		WithWorkdir("/tmp").
		// Download and extract in one step
		WithExec([]string{"sh", "-c", "wget -q -O- " + kernelURL + " | xz -d | tar x"}).
		WithWorkdir("/tmp/linux-" + linuxVersion)

	// Set up environment variables for LLVM build
	builder = builder.
		WithEnvVariable("LLVM", "1").
		WithEnvVariable("LLVM_IAS", "1").
		WithEnvVariable("CC", "clang").
		WithEnvVariable("LD", "ld.lld").
		WithEnvVariable("AR", "llvm-ar").
		WithEnvVariable("NM", "llvm-nm").
		WithEnvVariable("OBJCOPY", "llvm-objcopy").
		WithEnvVariable("OBJDUMP", "llvm-objdump").
		WithEnvVariable("READELF", "llvm-readelf").
		WithEnvVariable("STRIP", "llvm-strip").
		// Use Alpine's GCC for building host tools (scripts/basic/fixdep etc)
		// since they need to compile and run on the build system
		WithEnvVariable("HOSTCC", "gcc").
		WithEnvVariable("HOSTCXX", "g++")

	// Use tinyconfig as base (minimal kernel) and enable necessary features
	// Pass HOSTCC=gcc and HOSTLD=ld on command line to override LLVM=1 defaults
	builder = builder.WithExec([]string{"make", "LLVM=1", "HOSTCC=gcc", "HOSTCXX=g++", "HOSTLD=ld", "tinyconfig"})

	// Enable required features for QEMU boot
	configOptions := []string{
		// 64-bit kernel
		"CONFIG_64BIT=y",
		// Basic system
		"CONFIG_PRINTK=y",
		"CONFIG_BLK_DEV_INITRD=y",
		"CONFIG_RD_GZIP=y",
		"CONFIG_RD_BZIP2=y",
		"CONFIG_RD_LZMA=y",
		"CONFIG_RD_XZ=y",
		"CONFIG_RD_LZ4=y",
		"CONFIG_RD_ZSTD=y",
		// Console
		"CONFIG_TTY=y",
		"CONFIG_SERIAL_8250=y",
		"CONFIG_SERIAL_8250_CONSOLE=y",
		// PCI
		"CONFIG_PCI=y",
		// Block devices
		"CONFIG_BLK_DEV=y",
		"CONFIG_BLK_DEV_SD=y",
		// Filesystems
		"CONFIG_EXT4_FS=y",
		"CONFIG_PROC_FS=y",
		"CONFIG_SYSFS=y",
		"CONFIG_TMPFS=y",
		"CONFIG_DEVTMPFS=y",
		"CONFIG_DEVTMPFS_MOUNT=y",
		// Networking (basic)
		"CONFIG_NET=y",
		"CONFIG_INET=y",
		"CONFIG_UNIX=y",
		// Input
		"CONFIG_INPUT=y",
		"CONFIG_INPUT_KEYBOARD=y",
	}

	// Append each config option to .config
	for _, opt := range configOptions {
		builder = builder.WithExec([]string{"sh", "-c", "echo '" + opt + "' >> .config"})
	}

	// Run olddefconfig to resolve dependencies
	builder = builder.WithExec([]string{"make", "LLVM=1", "HOSTCC=gcc", "HOSTCXX=g++", "HOSTLD=ld", "olddefconfig"})

	// Build the kernel (use -j4 for reasonable parallelism in Dagger)
	builder = builder.WithExec([]string{"make", "LLVM=1", "HOSTCC=gcc", "HOSTCXX=g++", "HOSTLD=ld", "-j4"})

	// Return directory with built kernel
	return builder.Directory("/tmp/linux-" + linuxVersion)
}

// LinuxKernelImage extracts just the bzImage from a Linux kernel build.
//
// This is a convenience wrapper around LinuxKernelBuild() that returns
// only the compressed bootable kernel image, making it easier to use
// the kernel for booting or distribution.
//
// Example usage:
//
//	# Extract kernel bzImage:
//	dagger call linux-kernel-image --toolchain ./toolchain/llvm export --path=./vmlinuz-terranox
//
//	# Boot directly in QEMU:
//	qemu-system-x86_64 -kernel vmlinuz-terranox -append "console=ttyS0" -nographic -m 256M
func (m *TerranoxBootstrap) LinuxKernelImage(
	// Pre-built Stage1 toolchain directory
	// +optional
	toolchain *dagger.Directory,
	// LLVM version to use
	// +default="21.1.8"
	llvmVersion string,
	// musl version for sysroot
	// +default="1.2.5"
	muslVersion string,
	// Linux kernel version to build
	// +default="6.12.8"
	linuxVersion string,
	// Repository source directory
	// +optional
	source *dagger.Directory,
) *dagger.File {
	// Build full kernel
	kernelDir := m.LinuxKernelBuild(toolchain, llvmVersion, muslVersion, linuxVersion, source)

	// Extract just the bzImage
	return kernelDir.File("arch/x86/boot/bzImage")
}

// ═══════════════════════════════════════════════════════════
// BuildStrata: Build terranox-strata static binary
// ═══════════════════════════════════════════════════════════

// BuildStrata builds the terranox-strata system state manager
// as a statically-linked binary for /core integration.
//
// The binary is built with:
//   - Rust toolchain from Alpine
//   - x86_64-unknown-linux-musl target (static linking)
//   - Release optimizations
//   - llvm-strip for size reduction
//
// Returns a static ELF binary ready for deployment to /core/usr/bin/
func (m *TerranoxBootstrap) BuildStrata(
	// Source directory containing the crates/ directory
	source *dagger.Directory,
) *dagger.File {
	// Build container with Rust toolchain (using official rust:alpine image with musl support)
	builder := dag.Container().
		From("rust:alpine").
		// Install build dependencies
		WithExec([]string{"apk", "add", "--no-cache", "musl-dev"}).
		// Mount source directory
		WithDirectory("/src", source).
		WithWorkdir("/src/crates").
		// Add musl target
		WithExec([]string{"rustup", "target", "add", "x86_64-unknown-linux-musl"}).
		// Build in release mode with explicit musl target for static linking
		WithExec([]string{"cargo", "build", "-p", "terranox-strata", "--release", "--target", "x86_64-unknown-linux-musl"}).
		// Strip binary
		WithExec([]string{"strip", "target/x86_64-unknown-linux-musl/release/terranox-strata"})

	// Return the static binary
	return builder.File("/src/crates/target/x86_64-unknown-linux-musl/release/terranox-strata")
}

// ═══════════════════════════════════════════════════════════
// System-wide SBOM Aggregation
// ═══════════════════════════════════════════════════════════

// AggregateSboms builds the terranox-sbom-aggregate tool and generates
// a system-wide SBOM by discovering and merging all CycloneDX SBOMs from
// the Terranox Store and active strata.
//
// This function:
//  1. Builds the terranox-sbom-aggregate static binary
//  2. Discovers all .forge/sbom.cdx.json files in the store
//  3. Merges them into a single system-wide SBOM
//  4. Returns the aggregated SBOM as system.cdx.json
//
// The resulting SBOM provides a complete bill of materials for the entire
// system, including all packages and their dependencies.
//
// Example usage:
//
//	# Generate system-wide SBOM from /forge/store
//	dagger call aggregate-sboms --source=. --store=./mock-store export --path=./system.cdx.json
func (m *TerranoxBootstrap) AggregateSboms(
	ctx context.Context,
	// Source directory containing the crates/ directory
	source *dagger.Directory,
	// Store directory containing packages with .forge/sbom.cdx.json files
	store *dagger.Directory,
) (*dagger.File, error) {
	// Step 1: Build terranox-sbom-aggregate static binary
	builder := dag.Container().
		From("rust:alpine").
		// Install build dependencies
		WithExec([]string{"apk", "add", "--no-cache", "musl-dev"}).
		// Mount source directory
		WithDirectory("/src", source).
		WithWorkdir("/src/crates").
		// Add musl target for static linking
		WithExec([]string{"rustup", "target", "add", "x86_64-unknown-linux-musl"}).
		// Build in release mode with explicit musl target
		WithExec([]string{"cargo", "build", "-p", "terranox-sbom-aggregate", "--release", "--target", "x86_64-unknown-linux-musl"}).
		// Strip binary
		WithExec([]string{"strip", "target/x86_64-unknown-linux-musl/release/terranox-sbom-aggregate"})

	binary := builder.File("/src/crates/target/x86_64-unknown-linux-musl/release/terranox-sbom-aggregate")

	// Step 2: Run SBOM aggregation
	aggregator := dag.Container().
		From("alpine:3.19").
		// Copy the binary
		WithFile("/usr/local/bin/terranox-sbom-aggregate", binary).
		// Mount the store directory
		WithDirectory("/forge/store", store).
		// Create output directory
		WithExec([]string{"mkdir", "-p", "/state/sbom"}).
		// Run aggregation
		WithExec([]string{
			"/usr/local/bin/terranox-sbom-aggregate",
			"aggregate",
			"--store", "/forge/store",
			"--output", "/state/sbom/system.cdx.json",
			"--verbose",
		})

	// Return the aggregated SBOM file
	return aggregator.File("/state/sbom/system.cdx.json"), nil
}

// ═══════════════════════════════════════════════════════════
// BuildCoreSystem: Build complete /core base system
// ═══════════════════════════════════════════════════════════

// BuildCoreSystem builds the complete /core base system structure with all
// statically-linked binaries and configuration files required for v0.1.0.
//
// The /core directory is the immutable base layer containing:
//   - Static bash shell
//   - Static busybox (coreutils)
//   - terranox-strata activation tool
//   - terranox-init wrapper
//   - System configuration files
//
// All binaries are statically linked with musl to ensure zero libc dependency,
// making /core agnostic to which libc the active strata use.
//
// Returns a complete /core directory ready for installation.
func (m *TerranoxBootstrap) BuildCoreSystem(
	// Source directory containing the TerranoxOS repository
	source *dagger.Directory,
) (*dagger.Directory, error) {
	// Step 1: Build static bash
	bashBuilder := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache",
			"build-base", "curl", "musl-dev", "linux-headers", "ncurses-dev", "ncurses-static"}).
		WithExec([]string{"curl", "-L", "-o", "/tmp/bash-5.2.37.tar.gz",
			"https://ftp.gnu.org/gnu/bash/bash-5.2.37.tar.gz"}).
		WithExec([]string{"tar", "xf", "/tmp/bash-5.2.37.tar.gz", "-C", "/tmp"}).
		WithWorkdir("/tmp/bash-5.2.37").
		WithExec([]string{"./configure",
			"--enable-static-link",
			"--without-bash-malloc",
			"--disable-nls",
			"LDFLAGS=-static"}).
		WithExec([]string{"sh", "-c", "make -j$(nproc)"}).
		WithExec([]string{"strip", "bash"})

	staticBash := bashBuilder.File("/tmp/bash-5.2.37/bash")

	// Step 2: Build static busybox (coreutils replacement)
	// Note: Using busybox instead of toybox due to better Alpine compatibility
	busyboxBuilder := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache",
			"build-base", "curl", "musl-dev", "linux-headers", "perl"}).
		WithWorkdir("/tmp").
		WithExec([]string{"sh", "-c", "curl -L https://busybox.net/downloads/busybox-1.36.1.tar.bz2 | tar xj"}).
		WithWorkdir("/tmp/busybox-1.36.1").
		// Use defconfig and enable static build
		WithExec([]string{"make", "defconfig"}).
		WithExec([]string{"sh", "-c", "sed -i 's/# CONFIG_STATIC is not set/CONFIG_STATIC=y/' .config"}).
		WithExec([]string{"sh", "-c", "make -j$(nproc)"}).
		WithExec([]string{"strip", "busybox"})

	staticBusybox := busyboxBuilder.File("/tmp/busybox-1.36.1/busybox")

	// Step 3: Build terranox-strata (reuse existing function)
	strataStatic := m.BuildStrata(source)

	// Step 4: Create terranox-init wrapper
	// This is a simple C program that execs dinit
	initSource := `// SPDX-License-Identifier: GPL-2.0-only
/*
 * terranox-init - Minimal PID 1 wrapper for dinit
 *
 * This wrapper serves as the system's init process (PID 1).
 * It simply execs dinit with proper arguments.
 */

#include <unistd.h>
#include <stdio.h>

int main(int argc, char *argv[]) {
    char *dinit_argv[] = {
        "/core/usr/bin/dinit",
        "--services-dir=/core/etc/dinit.d",
        "--services-dir=/etc/dinit.d",
        NULL
    };

    execv("/core/usr/bin/dinit", dinit_argv);

    /* If we get here, exec failed */
    perror("terranox-init: failed to exec dinit");
    return 1;
}
`

	initBuilder := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "build-base", "musl-dev"}).
		WithNewFile("/tmp/terranox-init.c", initSource).
		WithWorkdir("/tmp").
		WithExec([]string{"gcc", "-static", "-O2", "-o", "terranox-init", "terranox-init.c"}).
		WithExec([]string{"strip", "terranox-init"})

	staticInit := initBuilder.File("/tmp/terranox-init")

	// Step 5: Create /core/etc/ configuration files
	osRelease := `NAME="TerranoxOS"
VERSION="0.1.0"
ID=terranox
VERSION_ID="0.1.0"
VERSION_CODENAME=nebulous
PRETTY_NAME="TerranoxOS 0.1.0 (Nebulous)"
HOME_URL="https://terranox.dev"
SUPPORT_URL="https://github.com/terranox-os/terranox-os"
BUG_REPORT_URL="https://github.com/terranox-os/terranox-os/issues"
`

	hostname := "terranox\n"

	passwd := `root:x:0:0:root:/root:/core/usr/bin/bash
nobody:x:65534:65534:nobody:/:/usr/bin/nologin
`

	// Step 6: Assemble /core directory structure
	coreDir := dag.Directory().
		// /core/usr/bin/
		WithFile("usr/bin/bash", staticBash).
		WithFile("usr/bin/busybox", staticBusybox).
		WithFile("usr/bin/terranox-strata", strataStatic).
		WithFile("usr/bin/terranox-init", staticInit).
		// Copy dinit binaries from existing core/ directory
		WithDirectory("usr/bin", source.Directory("core/usr/bin")).
		// /core/usr/share/terranox/
		WithNewFile("usr/share/terranox/os-release", osRelease).
		// /core/etc/
		WithNewFile("etc/os-release", osRelease).
		WithNewFile("etc/hostname", hostname).
		WithNewFile("etc/passwd", passwd).
		// Copy dinit service definitions
		WithDirectory("etc/dinit.d", source.Directory("core/etc/dinit.d"))

	return coreDir, nil
}
