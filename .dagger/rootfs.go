// SPDX-License-Identifier: Apache-2.0

// Root filesystem, initramfs, strata, and package build functions.
//
// Restored from commit 8ba519e — these were accidentally dropped
// in commit 3cc5423 (glibc variant) which truncated main.go.
//
// Functions:
//   - BuildRootFS: Create complete filesystem layout
//   - BuildInitramfs: Build early boot initramfs
//   - BuildDeveloperStratum: Assemble developer stratum manifest
//   - MelangeBuild: Build packages from melange YAML manifests

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dagger/terranox-bootstrap/internal/dagger"
)

func (m *TerranoxBootstrap) BuildRootFS(
	// Source directory containing the TerranoxOS repository
	source *dagger.Directory,
) (*dagger.Directory, error) {
	// Step 1: Build /core base system
	coreDir, err := m.BuildCoreSystem(source)
	if err != nil {
		return nil, fmt.Errorf("failed to build core system: %w", err)
	}

	// Step 2: Create /state directory structure
	// /state is the persistent mutable data layer
	stateStructure := `#!/bin/sh
# Create /state subdirectories
mkdir -p /state/strata/objects
mkdir -p /state/strata/refs
mkdir -p /state/machine/keys
mkdir -p /state/machine/network
mkdir -p /state/etc
mkdir -p /state/home/root
mkdir -p /state/log
mkdir -p /state/lib
mkdir -p /state/cache
mkdir -p /state/sentinel/logs
mkdir -p /state/sentinel/evidence
mkdir -p /state/sentinel/workspaces

# Create machine-id
cat /proc/sys/kernel/random/uuid > /state/machine/machine-id || echo "00000000-0000-0000-0000-000000000000" > /state/machine/machine-id
chmod 444 /state/machine/machine-id

# Create initial hostname
echo "terranox" > /state/machine/hostname

# Create empty active.json (no strata active initially)
cat > /state/strata/active.json <<EOF
{
  "version": "1.0",
  "strata": [],
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF
`

	stateDir := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "util-linux"}).
		WithNewFile("/create-state.sh", stateStructure).
		WithExec([]string{"sh", "/create-state.sh"}).
		Directory("/state")

	// Step 3: Create /forge directory structure
	forgeStructure := `#!/bin/sh
# Create /forge subdirectories
mkdir -p /forge/store
mkdir -p /forge/cache/sources
mkdir -p /forge/cache/ccache
mkdir -p /forge/tmp

# Add README for store
cat > /forge/store/README.md <<EOF
# TerranoxOS Package Store

This directory contains content-addressable packages built by the World Builder.

Format: /forge/store/<hash>-<name>-<version>-<arch>/

Example:
  /forge/store/a1b2c3d4-musl-1.2.5-x86_64/
  /forge/store/e5f6g7h8-bash-5.2.37-x86_64/

Packages are immutable once written.
EOF
`

	forgeDir := dag.Container().
		From("alpine:3.19").
		WithNewFile("/create-forge.sh", forgeStructure).
		WithExec([]string{"sh", "/create-forge.sh"}).
		Directory("/forge")

	// Step 4: Create /strata and /user empty structures
	strataUserStructure := `#!/bin/sh
# /strata will contain stratum layers
mkdir -p /strata

# /user will contain user-installed packages
mkdir -p /user/packages
mkdir -p /user/bin
mkdir -p /user/lib
mkdir -p /user/overlays
mkdir -p /user/profiles
`

	strataUserDir := dag.Container().
		From("alpine:3.19").
		WithNewFile("/create-strata-user.sh", strataUserStructure).
		WithExec([]string{"sh", "/create-strata-user.sh"}).
		Directory("/")

	// Step 5: Assemble complete root filesystem in a single container
	// This ensures proper symlink handling
	assembleScript := `#!/bin/sh
set -e

# Create base directories
mkdir -p /rootfs

# Create FHS directories at root
mkdir -p /rootfs/usr/bin
mkdir -p /rootfs/usr/lib
mkdir -p /rootfs/usr/share
mkdir -p /rootfs/usr/include
mkdir -p /rootfs/etc
mkdir -p /rootfs/lib
mkdir -p /rootfs/lib64
mkdir -p /rootfs/opt
mkdir -p /rootfs/boot

# Create /var with symlinks to /state
mkdir -p /rootfs/var
ln -s /state/log /rootfs/var/log
ln -s /state/lib /rootfs/var/lib
ln -s /state/cache /rootfs/var/cache
ln -s /run /rootfs/var/run

# Create /home symlink to /state/home
ln -s /state/home /rootfs/home

# Create tmp and run directories (will be tmpfs at boot)
mkdir -p /rootfs/tmp
mkdir -p /rootfs/run
chmod 1777 /rootfs/tmp
chmod 755 /rootfs/run
`

	rootFS := dag.Container().
		From("alpine:3.19").
		// Copy in all the subdirectories we built
		WithDirectory("/core-build", coreDir).
		WithDirectory("/state-build", stateDir).
		WithDirectory("/forge-build", forgeDir).
		WithDirectory("/strata-build", strataUserDir.Directory("strata")).
		WithDirectory("/user-build", strataUserDir.Directory("user")).
		// Run assembly script
		WithNewFile("/assemble.sh", assembleScript).
		WithExec([]string{"sh", "/assemble.sh"}).
		// Copy built directories into place
		WithExec([]string{"cp", "-a", "/core-build", "/rootfs/core"}).
		WithExec([]string{"cp", "-a", "/state-build", "/rootfs/state"}).
		WithExec([]string{"cp", "-a", "/forge-build", "/rootfs/forge"}).
		WithExec([]string{"cp", "-a", "/strata-build", "/rootfs/strata"}).
		WithExec([]string{"cp", "-a", "/user-build", "/rootfs/user"}).
		Directory("/rootfs")

	return rootFS, nil
}

// ═══════════════════════════════════════════════════════════
// BuildInitramfs: Build initramfs with early boot init
// ═══════════════════════════════════════════════════════════

// BuildInitramfs builds the initial RAM filesystem (initramfs) that the kernel
// loads at boot time before mounting the real root filesystem.
//
// The initramfs contains:
//   - /init script - Early boot initialization (mounts filesystems, runs strata activation)
//   - Static busybox - Provides mount, switch_root, sh, and basic utilities
//   - Static terranox-strata - Activates strata and creates FHS symlinks
//
// Boot sequence:
//  1. Kernel loads initramfs into RAM
//  2. Kernel executes /init
//  3. /init mounts /proc, /sys, /dev
//  4. /init mounts real root filesystem
//  5. /init mounts /core (ro), /strata (ro), /forge (ro), /state (rw,noexec), /user (rw)
//  6. /init runs terranox-strata activate (creates FHS symlinks at /)
//  7. /init calls switch_root to pivot to real root
//  8. /init execs /core/usr/bin/terranox-init (which execs dinit as PID 1)
//
// Returns a compressed initramfs.cpio.gz file ready for bootloader.
func (m *TerranoxBootstrap) BuildInitramfs(
	// Source directory containing the TerranoxOS repository
	source *dagger.Directory,
) (*dagger.File, error) {
	// Step 1: Build static busybox (reuse from BuildCoreSystem logic)
	busyboxBuilder := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache",
			"build-base", "curl", "musl-dev", "linux-headers", "perl"}).
		WithWorkdir("/tmp").
		WithExec([]string{"sh", "-c", "curl -L https://busybox.net/downloads/busybox-1.36.1.tar.bz2 | tar xj"}).
		WithWorkdir("/tmp/busybox-1.36.1").
		WithExec([]string{"make", "defconfig"}).
		WithExec([]string{"sh", "-c", "sed -i 's/# CONFIG_STATIC is not set/CONFIG_STATIC=y/' .config"}).
		WithExec([]string{"sh", "-c", "make -j$(nproc)"}).
		WithExec([]string{"strip", "busybox"})

	staticBusybox := busyboxBuilder.File("/tmp/busybox-1.36.1/busybox")

	// Step 2: Build terranox-strata (reuse existing function)
	strataStatic := m.BuildStrata(source)

	// Step 3: Create /init script
	initScript := `#!/bin/busybox sh
# TerranoxOS initramfs init script
# This runs as PID 1 in the initial RAM filesystem

set -e

# Print banner
echo "TerranoxOS v0.1.0 initramfs"
echo "Initializing early boot environment..."

# Mount pseudo-filesystems
echo "Mounting pseudo-filesystems..."
/bin/busybox mount -t proc proc /proc
/bin/busybox mount -t sysfs sysfs /sys
/bin/busybox mount -t devtmpfs devtmpfs /dev
/bin/busybox mkdir -p /dev/pts /dev/shm
/bin/busybox mount -t devpts devpts /dev/pts
/bin/busybox mount -t tmpfs tmpfs /dev/shm

# Mount real root filesystem
# For v0.1.0, assume root is on /dev/vda (QEMU default)
# TODO: Parse kernel cmdline for root= parameter
echo "Mounting root filesystem..."
/bin/busybox mkdir -p /newroot
/bin/busybox mount -t ext4 /dev/vda /newroot || {
    echo "FATAL: Failed to mount root filesystem on /dev/vda"
    echo "Dropping to emergency shell..."
    exec /bin/busybox sh
}

# Mount TerranoxOS layered filesystems
# For v0.1.0 single-partition layout, these are directories not separate mounts
echo "TerranoxOS layered filesystem ready (single-partition v0.1.0)"

# Create tmpfs for /tmp and /run
echo "Creating tmpfs mounts..."
/bin/busybox mount -t tmpfs -o size=2G,nosuid,nodev tmpfs /newroot/tmp
/bin/busybox mount -t tmpfs -o mode=755,nosuid,nodev tmpfs /newroot/run

# Run terranox-strata activation
# For v0.1.0, this is a no-op since we don't have strata yet
# But we prepare the structure for when #46 is complete
echo "Running strata activation..."
if [ -x /newroot/core/usr/bin/terranox-strata ]; then
    # TODO: Once #46 is complete, run activation here
    # /newroot/core/usr/bin/terranox-strata activate --root /newroot
    echo "Strata activation skipped (no strata defined for v0.1.0)"
else
    echo "Warning: terranox-strata not found in /core"
fi

# Create minimal FHS symlinks for v0.1.0
# This is temporary until terranox-strata activation (#46) does this
echo "Creating minimal FHS symlinks..."
if [ ! -e /newroot/usr/bin/bash ]; then
    /bin/busybox mkdir -p /newroot/usr/bin
    /bin/busybox ln -sf /core/usr/bin/bash /newroot/usr/bin/bash
fi
if [ ! -e /newroot/usr/bin/sh ]; then
    /bin/busybox ln -sf /core/usr/bin/bash /newroot/usr/bin/sh
fi

# Switch to real root
echo "Switching to real root..."
cd /newroot
exec /bin/busybox switch_root /newroot /core/usr/bin/terranox-init

# If we get here, switch_root failed
echo "FATAL: switch_root failed"
exec /bin/busybox sh
`

	// Step 4: Assemble initramfs directory structure
	assembleInitramfs := `#!/bin/sh
set -e

# Create initramfs directory structure
mkdir -p /initramfs/bin
mkdir -p /initramfs/dev
mkdir -p /initramfs/proc
mkdir -p /initramfs/sys
mkdir -p /initramfs/newroot
mkdir -p /initramfs/run

# Copy binaries
cp /busybox-static /initramfs/bin/busybox
cp /terranox-strata-static /initramfs/bin/terranox-strata
chmod +x /initramfs/bin/busybox
chmod +x /initramfs/bin/terranox-strata

# Create busybox symlinks for common utilities
cd /initramfs/bin
for cmd in sh mount umount mkdir ln cp mv rm cat echo ls; do
    ln -sf busybox $cmd
done
cd /

# Install init script
cp /init-script /initramfs/init
chmod +x /initramfs/init

# Create initramfs.cpio.gz
cd /initramfs
find . -print0 | cpio --null --create --format=newc | gzip -9 > /initramfs.cpio.gz

echo "Initramfs built successfully: $(stat -c%s /initramfs.cpio.gz) bytes"
`

	// Step 5: Build and compress initramfs
	initramfsBuilder := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "cpio", "gzip", "findutils"}).
		WithFile("/busybox-static", staticBusybox).
		WithFile("/terranox-strata-static", strataStatic).
		WithNewFile("/init-script", initScript).
		WithNewFile("/assemble.sh", assembleInitramfs).
		WithExec([]string{"sh", "/assemble.sh"})

	return initramfsBuilder.File("/initramfs.cpio.gz"), nil
}

// ═══════════════════════════════════════════════════════════
// BuildDeveloperStratum: Build mock developer stratum for v0.1.0
// ═══════════════════════════════════════════════════════════

// BuildDeveloperStratum builds a mock developer stratum with placeholder binaries
// for testing the strata activation system in v0.1.0.
//
// This creates a /strata/developer/ directory structure with:
//   - Mock binaries (clang, vim, git, make, cmake, llvm-*)
//   - Placeholder man pages
//   - Shell scripts that identify themselves when executed
//
// For v0.1.0, these are just mock binaries to test activation.
// Real packages will come from the Forge store in later versions.
//
// Returns a complete /strata/developer/ directory ready for activation.
func (m *TerranoxBootstrap) BuildDeveloperStratum(
	ctx context.Context,
	// Source directory containing the TerranoxOS repository (for manifest)
	source *dagger.Directory,
) (*dagger.Directory, error) {
	// Define manifest structure
	type StratumManifest struct {
		Name        string   `json:"name"`
		Version     string   `json:"version"`
		Description string   `json:"description"`
		Libc        string   `json:"libc"`
		Packages    []string `json:"packages"`
	}

	// Read manifest file
	manifestContent, err := source.File("strata/developer.json").Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	// Parse manifest
	var manifest StratumManifest
	if err := json.Unmarshal([]byte(manifestContent), &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	// Generate shell script to create mock binaries for all packages
	scriptHeader := `#!/bin/sh
set -e

# Create directory structure
mkdir -p /strata/developer/usr/bin
mkdir -p /strata/developer/usr/lib
mkdir -p /strata/developer/usr/share/man/man1
mkdir -p /strata/developer/usr/include

echo "Creating mock binaries for ` + fmt.Sprintf("%d", len(manifest.Packages)) + ` packages..."

`

	// Generate mock binary creation for each package in manifest
	var packageScripts strings.Builder
	for _, pkg := range manifest.Packages {
		packageScripts.WriteString(fmt.Sprintf(`
# Create mock %s
cat > /strata/developer/usr/bin/%s <<'EOF'
#!/bin/sh
echo "TerranoxOS Developer Stratum - %s (mock v0.1.0)"
echo "This is a placeholder for v0.1.0 testing"
echo "Real implementation will be integrated from /forge/store/"
exit 0
EOF
chmod +x /strata/developer/usr/bin/%s

`, pkg, pkg, pkg, pkg))
	}

	scriptFooter := `
# Create README explaining this is mock
cat > /strata/developer/README.md <<'EOF'
# TerranoxOS Developer Stratum (v0.1.0 Mock)

This is a mock/placeholder developer stratum for v0.1.0 testing.
Built from manifest: strata/developer.json

## Package List

This stratum contains ` + fmt.Sprintf("%d", len(manifest.Packages)) + ` mock binaries based on the developer manifest:

` + strings.Join(manifest.Packages, ", ") + `

## Purpose

This stratum exists to test the strata activation system (issue #46).
The binaries are shell scripts that print version info and exit.

## Real Implementation

In v0.2.0+, these will be replaced with:
- Real packages from /forge/store/ (issue #45)
- Proper dependency resolution
- Multiple libc variants (musl, glibc)

## Testing

To test activation:
  terranox-strata activate developer
  ls -la /usr/bin/clang /usr/bin/vim /usr/bin/git
  clang --version
EOF

echo "Developer stratum structure created successfully (` + fmt.Sprintf("%d", len(manifest.Packages)) + ` packages)"
`

	createMockBinaries := scriptHeader + packageScripts.String() + scriptFooter

	// Build stratum directory
	developerStratum := dag.Container().
		From("alpine:3.19").
		WithNewFile("/create-stratum.sh", createMockBinaries).
		WithExec([]string{"sh", "/create-stratum.sh"}).
		Directory("/strata/developer")

	return developerStratum, nil
}

// MelangeBuild builds a package using melange and signs it with terranox key.
//
// This function builds TerranoxOS packages (.apk) from melange YAML manifests
// located in derivations/melange/. All packages are signed with the terranox
// signing key (packages/melange.rsa) for supply chain security.
//
// Usage:
//
//	# Build bash package:
//	dagger call melange-build --package=bash export --path=./packages/x86_64
//
//	# Build multiple packages:
//	dagger call melange-build --package=bash export --path=./build/bash
//	dagger call melange-build --package=ncurses export --path=./build/ncurses
//
// Output: Signed .apk files in packages/x86_64/
func (m *TerranoxBootstrap) MelangeBuild(
	ctx context.Context,
	// Source directory containing derivations/melange/ and packages/
	source *dagger.Directory,
	// Package name (must match YAML file in derivations/melange/)
	packageName string,
) (*dagger.Directory, error) {
	// Validate package name
	if packageName == "" {
		return nil, fmt.Errorf("package name is required")
	}

	manifestPath := fmt.Sprintf("derivations/melange/%s.yaml", packageName)

	// Build package with melange
	buildScript := `#!/bin/sh
set -e

echo "==> Building package: ${PACKAGE_NAME}"
echo "==> Manifest: ${MANIFEST_PATH}"

# Install build dependencies
apk add --no-cache build-base curl

# Download and install melange static binary
# (Wolfi APK packages are glibc-linked and won't run on Alpine/musl)
echo "==> Installing melange (static binary)"
curl -fsSL https://github.com/chainguard-dev/melange/releases/latest/download/melange_linux_amd64 \
  -o /usr/local/bin/melange
chmod +x /usr/local/bin/melange

# Verify melange
melange version

# Create output directory
mkdir -p /workspace/packages/x86_64

# Copy signing key
mkdir -p /workspace/keys
cp /signing-key/melange.rsa /workspace/keys/melange.rsa
cp /signing-key/melange.rsa.pub /workspace/keys/melange.rsa.pub
chmod 600 /workspace/keys/melange.rsa

# Set up Alpine keyring for package dependencies
mkdir -p /etc/apk/keys
wget -qO /etc/apk/keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub \
  https://alpinelinux.org/keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub

# Build package (no Docker runner - sandboxing handled by Dagger container)
cd /workspace
melange build \
  --signing-key /workspace/keys/melange.rsa \
  --arch x86_64 \
  --out-dir /workspace/packages/x86_64 \
  --repository-append https://dl-cdn.alpinelinux.org/alpine/v3.19/main \
  --keyring-append /etc/apk/keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub \
  /source/${MANIFEST_PATH}

echo "==> Build complete"
ls -lh /workspace/packages/x86_64/
`

	// Extract signing key from source
	signingKey := source.File("packages/melange.rsa")
	signingKeyPub := source.File("packages/melange.rsa.pub")

	// Build container
	// Note: Docker socket mounting for --runner docker requires Dagger API not yet available
	// For v0.1.0, this documents the infrastructure - actual builds blocked by sandbox limitation
	builder := dag.Container().
		From("alpine:3.19").
		WithDirectory("/source", source).
		WithFile("/signing-key/melange.rsa", signingKey).
		WithFile("/signing-key/melange.rsa.pub", signingKeyPub).
		WithEnvVariable("PACKAGE_NAME", packageName).
		WithEnvVariable("MANIFEST_PATH", manifestPath).
		WithNewFile("/build.sh", buildScript).
		WithExec([]string{"sh", "/build.sh"})

	// Return packages directory
	return builder.Directory("/workspace/packages/x86_64"), nil
}

// ISOBuild creates a bootable ISO image with Limine bootloader and TerranoxOS kernel.
//
// This function:
// 1. Downloads Limine v8.x-binary bootloader
// 2. Creates ISO directory structure with kernel and bootloader files
// 3. Generates bootable ISO using xorriso with both BIOS and UEFI support
//
// The resulting ISO can be booted in QEMU or written to physical media.
//
// Example usage:
//
//	# Build kernel and create bootable ISO:
//	dagger call iso-build export --path=./terranox.iso
//
//	# Using pre-built kernel:
//	dagger call iso-build --kernel-dir ./build export --path=./terranox.iso
//
//	# Boot in QEMU:
//	qemu-system-x86_64 -cdrom terranox.iso -m 256M
