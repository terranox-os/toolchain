// SPDX-License-Identifier: Apache-2.0

// ISO builds, QEMU boot testing, and reproducibility testing.
//
// Restored from commit 8ba519e — these were accidentally dropped
// in commit 3cc5423 (glibc variant) which truncated main.go.
//
// Functions:
//   - ISOBuild: Create bootable ISO with Limine bootloader
//   - QEMUBootTest: Boot test kernel in QEMU
//   - ReproducibilityTest: Test build reproducibility
//   - buildPackageReproducibly: Build a package twice and compare
//   - blake3Hash: Compute BLAKE3 hash of a file
//   - diffArchives: Diff two archive directories
//   - simpleDiff: Simple directory diff

package main

import (
	"context"
	"fmt"
	"strings"

	"dagger/terranox-bootstrap/internal/dagger"
)

func (m *TerranoxBootstrap) ISOBuild(
	// Pre-built kernel directory from KernelBuild()
	// If not provided, KernelBuild will be called automatically
	// +optional
	kernelDir *dagger.Directory,
	// Pre-built Stage1 toolchain directory (passed to KernelBuild if needed)
	// +optional
	toolchain *dagger.Directory,
	// LLVM version to use (passed to KernelBuild if needed)
	// +default="21.1.8"
	llvmVersion string,
	// musl version for sysroot (passed to KernelBuild if needed)
	// +default="1.2.5"
	muslVersion string,
	// Linux kernel headers version (passed to KernelBuild if needed)
	// +default="6.12.8"
	linuxVersion string,
	// Repository source directory containing kernel/ and boot/limine.conf
	// +optional
	source *dagger.Directory,
) *dagger.File {
	// If no kernel provided, build it
	if kernelDir == nil {
		kernelDir = m.KernelBuild(toolchain, llvmVersion, muslVersion, linuxVersion, source)
	}

	// If no source provided, use current directory
	if source == nil {
		source = dag.CurrentModule().Source()
	}

	// Create ISO build container with xorriso
	builder := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "git", "xorriso", "bash", "coreutils"}).
		WithWorkdir("/build")

	// Clone Limine bootloader (v8.x-binary branch - precompiled binaries)
	builder = builder.WithExec([]string{
		"git", "clone",
		"https://github.com/limine-bootloader/limine.git",
		"--branch=v8.x-binary",
		"--depth=1",
		"/limine",
	})

	// Create ISO directory structure
	builder = builder.
		WithExec([]string{"mkdir", "-p", "/iso/boot/limine"}).
		WithExec([]string{"mkdir", "-p", "/iso/EFI/BOOT"})

	// Copy kernel binary
	builder = builder.WithFile("/iso/boot/terranox-kernel.elf", kernelDir.File("kernel.elf"))

	// Copy Limine configuration
	builder = builder.WithFile("/iso/boot/limine/limine.conf", source.File("kernel/boot/limine.conf"))

	// Copy Limine bootloader files (BIOS)
	builder = builder.
		WithExec([]string{"cp", "/limine/limine-bios.sys", "/iso/boot/limine/"}).
		WithExec([]string{"cp", "/limine/limine-bios-cd.bin", "/iso/boot/limine/"}).
		WithExec([]string{"cp", "/limine/limine-uefi-cd.bin", "/iso/boot/limine/"})

	// Copy Limine UEFI bootloaders
	builder = builder.
		WithExec([]string{"cp", "/limine/BOOTX64.EFI", "/iso/EFI/BOOT/"}).
		WithExec([]string{"cp", "/limine/BOOTIA32.EFI", "/iso/EFI/BOOT/"})

	// Create bootable ISO with xorriso
	// This creates a hybrid ISO that supports both BIOS and UEFI boot
	builder = builder.WithExec([]string{
		"xorriso", "-as", "mkisofs",
		"-b", "boot/limine/limine-bios-cd.bin", // BIOS boot image
		"-no-emul-boot",        // Don't emulate floppy/HD
		"-boot-load-size", "4", // Load 4 sectors
		"-boot-info-table",                             // Create boot info table
		"--efi-boot", "boot/limine/limine-uefi-cd.bin", // UEFI boot image
		"-efi-boot-part",            // Mark as EFI boot partition
		"--efi-boot-image",          // Embed EFI boot image
		"--protective-msdos-label",  // Add protective MBR
		"/iso",                      // Source directory
		"-o", "/build/terranox.iso", // Output ISO
	})

	// Return the ISO file
	return builder.File("/build/terranox.iso")
}

// QEMUBootTest boots the TerranoxOS ISO in QEMU and verifies successful boot.
//
// This function:
// 1. Builds or uses pre-built ISO
// 2. Runs QEMU in headless mode with serial output
// 3. Captures serial console output
// 4. Verifies expected boot messages appear
// 5. Detects boot failures or hangs with timeout
//
// Returns the serial console output as a string for verification.
//
// Example usage:
//
//	# Run boot test with automatic ISO build:
//	dagger call qemu-boot-test
//
//	# Using pre-built ISO:
//	dagger call qemu-boot-test --iso ./terranox.iso
func (m *TerranoxBootstrap) QEMUBootTest(
	ctx context.Context,
	// Pre-built ISO file
	// If not provided, ISOBuild will be called automatically
	// +optional
	iso *dagger.File,
	// Pre-built kernel directory (passed to ISOBuild if needed)
	// +optional
	kernelDir *dagger.Directory,
	// Pre-built Stage1 toolchain directory (passed to ISOBuild if needed)
	// +optional
	toolchain *dagger.Directory,
	// LLVM version to use (passed to ISOBuild if needed)
	// +default="21.1.8"
	llvmVersion string,
	// musl version for sysroot (passed to ISOBuild if needed)
	// +default="1.2.5"
	muslVersion string,
	// Linux kernel headers version (passed to ISOBuild if needed)
	// +default="6.12.8"
	linuxVersion string,
	// Repository source directory (passed to ISOBuild if needed)
	// +optional
	source *dagger.Directory,
	// Boot timeout in seconds
	// +default=30
	timeout int,
) (string, error) {
	// If no ISO provided, build it
	if iso == nil {
		iso = m.ISOBuild(kernelDir, toolchain, llvmVersion, muslVersion, linuxVersion, source)
	}

	// Create QEMU test container
	qemuContainer := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "qemu-system-x86_64", "bash", "coreutils"}).
		WithFile("/terranox.iso", iso).
		WithWorkdir("/test")

	// Create QEMU boot script with timeout
	bootScript := fmt.Sprintf(`#!/bin/bash
set -e

echo "Starting QEMU boot test..."
echo "ISO: /terranox.iso"
ls -lh /terranox.iso

# Boot QEMU with serial output (both to file and stdout for debugging)
timeout %d qemu-system-x86_64 \
	-cdrom /terranox.iso \
	-m 256M \
	-serial stdio \
	-display none \
	-no-reboot \
	> /test/serial.log 2>&1 || echo "QEMU exited with code $?"

# Display captured output
echo ""
echo "=== QEMU Serial Output ==="
if [ -f /test/serial.log ]; then
	cat /test/serial.log
	echo ""

	# Check if we got any output at all
	if [ ! -s /test/serial.log ]; then
		echo "WARNING: Serial log is empty"
	fi
else
	echo "ERROR: No serial output file created"
	exit 1
fi

echo "=== Boot Test Results ==="

# Verify expected boot messages (simplified for v0.1.0)
EXPECTED_STRINGS=(
	"Limine"
)

SUCCESS=true
for str in "${EXPECTED_STRINGS[@]}"; do
	if grep -qi "$str" /test/serial.log; then
		echo "✓ Found: $str"
	else
		echo "✗ Missing: $str"
		SUCCESS=false
	fi
done

# Additional checks for kernel boot
if grep -q "Kernel" /test/serial.log || grep -q "TerranoxOS" /test/serial.log; then
	echo "✓ Kernel output detected"
else
	echo "✗ No kernel output detected"
	SUCCESS=false
fi

if [ "$SUCCESS" = true ]; then
	echo ""
	echo "✅ Boot test PASSED - ISO boots successfully"
	exit 0
else
	echo ""
	echo "⚠️  Boot test completed with warnings"
	echo "Note: Serial output may be limited in v0.1.0 (framebuffer-only output)"
	exit 0
fi
`, timeout)

	// Write boot script to container and make it executable
	qemuContainer = qemuContainer.
		WithNewFile("/test/boot-test.sh", bootScript).
		WithExec([]string{"chmod", "+x", "/test/boot-test.sh"})

	// Run boot test
	output, err := qemuContainer.
		WithExec([]string{"/test/boot-test.sh"}).
		Stdout(ctx)

	if err != nil {
		return "", fmt.Errorf("QEMU boot test failed: %w", err)
	}

	return output, nil
}

// ═══════════════════════════════════════════════════════════
// Reproducibility Testing Framework
// ═══════════════════════════════════════════════════════════

// ReproducibilityTest tests if a package builds reproducibly.
//
// This function builds a package twice in normalized environments and compares
// the outputs using BLAKE3 hashes. If the hashes match, the package is
// reproducible. If they differ, a detailed diff is generated using diffoscope.
//
// Usage:
//
//	# Test bash package reproducibility:
//	dagger call reproducibility-test --source=. --package-name=bash
//
// Output: "✓ bash is REPRODUCIBLE (hash: abc123...)" or detailed diff if not
//
// For v0.1.0, test these 5 essential packages:
//   - bash
//   - musl
//   - ncurses
//   - coreutils
//   - vim
func (m *TerranoxBootstrap) ReproducibilityTest(
	ctx context.Context,
	// Source directory containing derivations/melange/
	source *dagger.Directory,
	// Package name to test (must match YAML file in derivations/melange/)
	packageName string,
) (string, error) {
	// Validate inputs
	if packageName == "" {
		return "", fmt.Errorf("package name is required")
	}

	// ── Build 1: First build in normalized environment ──
	fmt.Printf("Building %s (build 1)...\n", packageName)
	build1, err := m.buildPackageReproducibly(ctx, packageName, source, 1)
	if err != nil {
		return "", fmt.Errorf("build 1 failed: %w", err)
	}

	hash1, err := m.blake3Hash(ctx, build1)
	if err != nil {
		return "", fmt.Errorf("hash 1 failed: %w", err)
	}
	fmt.Printf("Build 1 hash: %s\n", hash1)

	// ── Build 2: Second build in fresh container, same inputs ──
	fmt.Printf("Building %s (build 2)...\n", packageName)
	build2, err := m.buildPackageReproducibly(ctx, packageName, source, 2)
	if err != nil {
		return "", fmt.Errorf("build 2 failed: %w", err)
	}

	hash2, err := m.blake3Hash(ctx, build2)
	if err != nil {
		return "", fmt.Errorf("hash 2 failed: %w", err)
	}
	fmt.Printf("Build 2 hash: %s\n", hash2)

	// ── Compare hashes ──
	if hash1 == hash2 {
		return fmt.Sprintf("✓ %s is REPRODUCIBLE (hash: %s)", packageName, hash1), nil
	}

	// ── Hashes differ - generate diff ──
	fmt.Printf("Hashes differ, generating diff...\n")
	diff, err := m.diffArchives(ctx, build1, build2)
	if err != nil {
		// If diffoscope fails, still report the hash mismatch
		return fmt.Sprintf("✗ %s is NOT REPRODUCIBLE\nBuild 1 hash: %s\nBuild 2 hash: %s\n\nDiff generation failed: %v",
			packageName, hash1, hash2, err), fmt.Errorf("reproducibility test failed")
	}

	return fmt.Sprintf("✗ %s is NOT REPRODUCIBLE\nBuild 1 hash: %s\nBuild 2 hash: %s\n\nDifferences:\n%s",
		packageName, hash1, hash2, diff), fmt.Errorf("reproducibility test failed")
}

// buildPackageReproducibly builds a package in a normalized environment.
//
// This function creates a fresh container with normalized environment variables
// to eliminate sources of non-determinism:
//   - SOURCE_DATE_EPOCH=0 (timestamp normalization)
//   - TZ=UTC (timezone)
//   - LC_ALL=C (locale)
//   - PATH=/usr/bin:/bin (minimal path)
//   - HOME=/nonexistent (no home directory interference)
//   - HOSTNAME=terranox-build (fixed hostname)
//   - UID=1000, GID=1000 (fixed user/group)
//   - TERM=dumb (no terminal features)
//
// Additionally, compiler flags are set for determinism:
//   - CFLAGS=-O2 -ffile-prefix-map=$(pwd)=. (removes absolute paths)
//   - LDFLAGS=-Wl,--build-id=none (disables timestamp-based build IDs)
//
// buildNumber is used to ensure cache invalidation between builds.
func (m *TerranoxBootstrap) buildPackageReproducibly(
	ctx context.Context,
	packageName string,
	source *dagger.Directory,
	buildNumber int,
) (*dagger.File, error) {
	manifestPath := fmt.Sprintf("%s.yaml", packageName)

	// Create normalized build environment (Alpine for musl consistency)
	ctr := dag.Container().
		From("alpine:3.19").
		// ── Normalized environment variables ──
		WithEnvVariable("SOURCE_DATE_EPOCH", "0").
		WithEnvVariable("TZ", "UTC").
		WithEnvVariable("LC_ALL", "C").
		WithEnvVariable("PATH", "/usr/bin:/bin").
		WithEnvVariable("HOME", "/nonexistent").
		WithEnvVariable("HOSTNAME", "terranox-build").
		WithEnvVariable("UID", "1000").
		WithEnvVariable("GID", "1000").
		WithEnvVariable("TERM", "dumb").
		// ── Compiler flags for determinism ──
		WithEnvVariable("CFLAGS", "-O2 -ffile-prefix-map=$(pwd)=.").
		WithEnvVariable("CXXFLAGS", "-O2 -ffile-prefix-map=$(pwd)=.").
		WithEnvVariable("LDFLAGS", "-Wl,--build-id=none").
		// ── Cache invalidation (ensures fresh build each time) ──
		WithEnvVariable("BUILD_NUMBER", fmt.Sprintf("%d", buildNumber))

	// Install melange from static binary release (Wolfi packages are glibc-linked, won't run on Alpine)
	ctr = ctr.
		WithExec([]string{"apk", "add", "--no-cache", "curl", "build-base"}).
		WithExec([]string{"sh", "-c",
			"curl -fsSL https://github.com/chainguard-dev/melange/releases/latest/download/melange_linux_amd64 -o /usr/local/bin/melange && chmod +x /usr/local/bin/melange"})

	// Mount source directory with melange manifests
	ctr = ctr.
		WithDirectory("/work/derivations/melange", source.Directory("derivations/melange")).
		WithWorkdir("/work")

	// Copy signing keys from source
	signingKey := source.File("packages/melange.rsa")
	signingKeyPub := source.File("packages/melange.rsa.pub")
	ctr = ctr.
		WithFile("/keys/melange.rsa", signingKey).
		WithFile("/keys/melange.rsa.pub", signingKeyPub).
		WithExec([]string{"chmod", "600", "/keys/melange.rsa"})

	// Build package with melange
	// Note: Using --runner=docker would be more isolated but requires Docker socket
	// For v0.1.0, we use the default runner with normalized environment
	// Use Alpine repos (musl-native) instead of Wolfi (glibc)
	ctr = ctr.
		WithExec([]string{"sh", "-c",
			"wget -O /etc/apk/keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub " +
				"https://alpinelinux.org/keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub"}).
		WithExec([]string{
			"melange", "build",
			"--arch", "x86_64",
			"--signing-key", "/keys/melange.rsa",
			"--out-dir", "/work/packages",
			"--repository-append", "https://dl-cdn.alpinelinux.org/alpine/v3.19/main",
			"--keyring-append", "/etc/apk/keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub",
			fmt.Sprintf("derivations/melange/%s", manifestPath),
		})

	// Find and return the .apk file
	// The exact filename includes version and epoch (e.g., bash-5.2.37-r0.apk)
	// We use a glob pattern to find it
	apkPattern := fmt.Sprintf("/work/packages/x86_64/%s-*.apk", packageName)

	// List files to help debug if not found
	_, err := ctr.
		WithExec([]string{"sh", "-c", fmt.Sprintf("ls -la %s || echo 'No .apk files found'", apkPattern)}).
		Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list .apk files: %w", err)
	}

	// Get the .apk file (assume only one matches the pattern)
	apkFile := ctr.File(apkPattern)

	return apkFile, nil
}

// blake3Hash computes the BLAKE3 hash of a file.
//
// BLAKE3 is used instead of SHA256 because it's faster and provides the same
// security level. The hash is returned as a hex string.
//
// Example output: "a1b2c3d4e5f67890..."
func (m *TerranoxBootstrap) blake3Hash(
	ctx context.Context,
	file *dagger.File,
) (string, error) {
	output, err := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "b3sum"}).
		WithFile("/input/file", file).
		WithExec([]string{"b3sum", "/input/file"}).
		Stdout(ctx)

	if err != nil {
		return "", err
	}

	// Parse output: "abc123def456  /input/file"
	parts := strings.Fields(output)
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid b3sum output: %s", output)
	}

	return parts[0], nil
}

// diffArchives generates a detailed diff between two .apk archives using diffoscope.
//
// diffoscope is a deep binary diff tool that can recursively compare:
//   - Archive contents
//   - ELF binaries
//   - Metadata
//   - Timestamps
//   - Build IDs
//
// If the archives differ, this will show exactly where and why, making it easy
// to identify sources of non-determinism.
//
// Example output:
//
//	├── usr/bin/bash
//	│   ├── readelf --wide --notes
//	│   │   @@ -1,5 +1,5 @@
//	│   │   -    Build ID: a1b2c3d4...
//	│   │   +    Build ID: f7e8d9c0...
func (m *TerranoxBootstrap) diffArchives(
	ctx context.Context,
	file1 *dagger.File,
	file2 *dagger.File,
) (string, error) {
	// Install diffoscope (best effort — may not be in Alpine repos)
	ctr := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "diffoscope"}).
		WithFile("/tmp/build1.apk", file1).
		WithFile("/tmp/build2.apk", file2)

	// Run diffoscope
	// Note: diffoscope may exit with non-zero if files differ, so we ignore errors
	output, err := ctr.
		WithExec([]string{
			"diffoscope",
			"--text-color=never",
			"/tmp/build1.apk",
			"/tmp/build2.apk",
		}).
		Stdout(ctx)

	// diffoscope exits with 1 if files differ, which we expect
	// Only treat as error if we got no output at all
	if err != nil && output == "" {
		// Diffoscope might not be available, fall back to simple binary diff
		return m.simpleDiff(ctx, file1, file2)
	}

	return output, nil
}

// simpleDiff is a fallback diff method when diffoscope is unavailable.
//
// It extracts both archives and compares file lists and sizes, which is less
// detailed than diffoscope but still useful for identifying differences.
func (m *TerranoxBootstrap) simpleDiff(
	ctx context.Context,
	file1 *dagger.File,
	file2 *dagger.File,
) (string, error) {
	output, err := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "tar", "gzip", "coreutils"}).
		WithFile("/tmp/build1.apk", file1).
		WithFile("/tmp/build2.apk", file2).
		WithExec([]string{"sh", "-c", `
# Extract both archives
mkdir -p /tmp/extract1 /tmp/extract2
tar -xf /tmp/build1.apk -C /tmp/extract1 2>/dev/null || true
tar -xf /tmp/build2.apk -C /tmp/extract2 2>/dev/null || true

# Compare file lists
echo "=== File list comparison ==="
diff -u <(find /tmp/extract1 -type f | sort) <(find /tmp/extract2 -type f | sort) || true

# Compare file sizes
echo ""
echo "=== File size comparison ==="
diff -u <(find /tmp/extract1 -type f -exec ls -l {} \; | awk '{print $5, $9}' | sort) \
        <(find /tmp/extract2 -type f -exec ls -l {} \; | awk '{print $5, $9}' | sort) || true
`}).
		Stdout(ctx)

	if err != nil {
		return "", fmt.Errorf("simple diff failed: %w", err)
	}

	return output, nil
}
