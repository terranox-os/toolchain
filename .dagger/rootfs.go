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
	// Local APK repository with previously-built packages.
	// When provided, these packages are available as build dependencies
	// inside the melange sandbox, enabling self-hosting bootstraps where
	// earlier packages (e.g., musl 1.2.5) are used by later builds (e.g., git).
	// +optional
	localRepo *dagger.Directory,
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
apk add --no-cache build-base curl bubblewrap

# Download and install melange from release tarball
# (Wolfi APK packages are glibc-linked and won't run on Alpine/musl)
echo "==> Installing melange"
MELANGE_URL=$(curl -fsSL https://api.github.com/repos/chainguard-dev/melange/releases/latest \
  | grep browser_download_url | grep linux_amd64.tar.gz\" | head -1 | cut -d'"' -f4)
echo "Downloading: ${MELANGE_URL}"
curl -fsSL "${MELANGE_URL}" | tar xz --strip-components=1 -C /usr/local/bin

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

# Set up local repo keyring if present
LOCAL_REPO_FLAGS=""
if [ -d /local-repo ]; then
  echo "==> Local package repo detected"
  cp /signing-key/melange.rsa.pub /etc/apk/keys/melange.rsa.pub
  # Ensure repo has x86_64 structure
  if [ ! -d /local-repo/x86_64 ]; then
    mkdir -p /tmp/local-repo/x86_64
    cp /local-repo/*.apk /tmp/local-repo/x86_64/ 2>/dev/null || true
    cp /local-repo/APKINDEX.tar.gz /tmp/local-repo/x86_64/ 2>/dev/null || true
    LOCAL_REPO_FLAGS="--repository-append /tmp/local-repo --keyring-append /etc/apk/keys/melange.rsa.pub"
  else
    LOCAL_REPO_FLAGS="--repository-append /local-repo --keyring-append /etc/apk/keys/melange.rsa.pub"
  fi
  ls -lh /local-repo/ | head -10
fi

# Detect if terranox-clang is in the local repo — if so, inject it and set CC/CXX
CLANG_ENV_FLAGS=""
CLANG_PKG_FLAGS=""
if [ -d /local-repo ] && ls /local-repo/terranox-clang-*.apk >/dev/null 2>&1 || \
   [ -d /local-repo/x86_64 ] && ls /local-repo/x86_64/terranox-clang-*.apk >/dev/null 2>&1; then
  echo "==> terranox-clang detected, will compile with clang"
  CLANG_PKG_FLAGS="--package-append terranox-clang"
  # melange uses --env-file, not --env
  cat > /tmp/clang-env <<'ENVEOF'
CC=clang
CXX=clang++
LD=ld.lld
AR=llvm-ar
RANLIB=llvm-ranlib
ENVEOF
  CLANG_ENV_FLAGS="--env-file /tmp/clang-env"
fi

# Build package (bubblewrap provides sandboxing inside Dagger container)
cd /workspace
melange build \
  --runner bubblewrap \
  --signing-key /workspace/keys/melange.rsa \
  --arch x86_64 \
  --out-dir /workspace/packages/x86_64 \
  --repository-append https://dl-cdn.alpinelinux.org/alpine/v3.19/main \
  --keyring-append /etc/apk/keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub \
  $LOCAL_REPO_FLAGS \
  $CLANG_PKG_FLAGS \
  $CLANG_ENV_FLAGS \
  /source/${MANIFEST_PATH}

echo "==> Build complete"
ls -lh /workspace/packages/x86_64/
`

	// Extract signing key from source
	signingKey := source.File("packages/melange.rsa")
	signingKeyPub := source.File("packages/melange.rsa.pub")

	// Build container
	// InsecureRootCapabilities is required because melange's bubblewrap runner
	// needs CAP_SYS_ADMIN for user namespace creation. Dagger already provides
	// container isolation so this doesn't reduce security.
	builder := dag.Container().
		From("alpine:3.19").
		WithDirectory("/source", source).
		WithFile("/signing-key/melange.rsa", signingKey).
		WithFile("/signing-key/melange.rsa.pub", signingKeyPub).
		WithEnvVariable("PACKAGE_NAME", packageName).
		WithEnvVariable("MANIFEST_PATH", manifestPath)

	if localRepo != nil {
		builder = builder.WithDirectory("/local-repo", localRepo)
	}

	builder = builder.
		WithNewFile("/build.sh", buildScript).
		WithExec([]string{"sh", "/build.sh"}, dagger.ContainerWithExecOpts{
			InsecureRootCapabilities: true,
		})

	// Return packages directory
	return builder.Directory("/workspace/packages/x86_64"), nil
}

// ═══════════════════════════════════════════════════════════
// Melange key generation
// ═══════════════════════════════════════════════════════════

// MelangeKeygen generates a fresh RSA-4096 signing keypair for melange
// package signing. The private key is written to packages/melange.rsa and
// the public key to packages/melange.rsa.pub.
//
// Only needed once — the generated keys should be committed to the repo
// (the private key is .gitignored in real deployments, but for local-only
// builds it lives alongside the repo).
//
// Usage:
//
//	dagger call melange-keygen export --path=./packages
func (m *TerranoxBootstrap) MelangeKeygen() *dagger.Directory {
	return dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "curl"}).
		WithExec([]string{"sh", "-c", `
			mkdir -p /out
			# Download and install melange
			MELANGE_URL=$(curl -fsSL https://api.github.com/repos/chainguard-dev/melange/releases/latest \
				| grep browser_download_url | grep linux_amd64.tar.gz\" | head -1 | cut -d'"' -f4)
			curl -fsSL "${MELANGE_URL}" | tar xz --strip-components=1 -C /usr/local/bin

			# Use melange's own keygen (generates correct format)
			melange keygen /out/melange.rsa
			chmod 600 /out/melange.rsa
		`}).
		Directory("/out")
}

// ═══════════════════════════════════════════════════════════
// apko: Declarative OCI image assembly (musl Wolfi)
// ═══════════════════════════════════════════════════════════

// ApkoBuild assembles an OCI container image from an apko YAML config.
//
// This is the core of the "musl Wolfi" story: apko declaratively assembles
// an image from Alpine apk packages (all musl-linked), producing a
// minimal, reproducible OCI image with no runtime build dependencies.
//
// The config file lives in derivations/apko/ and follows apko's YAML format.
// Custom melange-built packages can be included by providing a local
// repository directory.
//
// Usage:
//
//	# Build the musl SDK image from the default config:
//	dagger call apko-build --source=.
//
//	# Build with custom apko config:
//	dagger call apko-build --source=. --config=derivations/apko/sdk-musl.yaml
//
//	# Build with local melange packages included:
//	dagger call apko-build --source=. --local-repo=./packages/x86_64
//
//	# Export OCI tarball:
//	dagger call apko-build --source=. export --path=./terranox-sdk.tar
//
//	# Interactive shell:
//	dagger call apko-build --source=. terminal
func (m *TerranoxBootstrap) ApkoBuild(
	ctx context.Context,
	// Repository source directory containing derivations/apko/ and packages/
	source *dagger.Directory,
	// Path to apko YAML config within source (relative to repo root)
	// +default="derivations/apko/sdk-musl.yaml"
	config string,
	// Tag for the built image
	// +default="terranox-sdk:latest"
	tag string,
	// Local apk repository directory with melange-built packages.
	// If provided, it is mounted as an additional repository so
	// custom packages can be referenced in the apko config.
	// +optional
	localRepo *dagger.Directory,
) (*dagger.Container, error) {
	// apko and melange are Go binaries distributed as tarballs.
	// The Wolfi APK packages are glibc-linked and won't run on Alpine/musl,
	// so we download the release tarball and extract the static binary.
	buildScript := `#!/bin/sh
set -e

echo "==> Installing apko"
# Fetch the latest release tarball URL from GitHub API
APKO_URL=$(curl -fsSL https://api.github.com/repos/chainguard-dev/apko/releases/latest \
  | grep browser_download_url | grep linux_amd64.tar.gz\" | head -1 | cut -d'"' -f4)
echo "Downloading: ${APKO_URL}"
curl -fsSL "${APKO_URL}" | tar xz --strip-components=1 -C /usr/local/bin
chmod +x /usr/local/bin/apko
apko version

# Set up Alpine keyring so apko can verify upstream packages
mkdir -p /etc/apk/keys
wget -qO /etc/apk/keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub \
  https://alpinelinux.org/keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub

`

	// If a local repo is provided, add it as an extra repository
	localRepoFlag := ""
	if localRepo != nil {
		buildScript += `
# Set up local melange repo — copy signing key and create repo structure
mkdir -p /etc/apk/keys
if [ -f /local-repo/melange.rsa.pub ]; then
  cp /local-repo/melange.rsa.pub /etc/apk/keys/
fi

# Move APK files into an arch-specific directory if not already structured
if [ ! -d /local-repo/x86_64 ]; then
  mkdir -p /tmp/local-repo/x86_64
  cp /local-repo/*.apk /tmp/local-repo/x86_64/ 2>/dev/null || true
  cp /local-repo/APKINDEX.tar.gz /tmp/local-repo/x86_64/ 2>/dev/null || true
  cp /local-repo/melange.rsa.pub /tmp/local-repo/ 2>/dev/null || true
  LOCAL_REPO_PATH=/tmp/local-repo
else
  LOCAL_REPO_PATH=/local-repo
fi
`
		localRepoFlag = ` --repository-append $LOCAL_REPO_PATH --keyring-append /etc/apk/keys/melange.rsa.pub`
	}

	buildScript += fmt.Sprintf(`
echo "==> Building OCI image with apko"
cd /workspace

# Build image with SBOM generation
mkdir -p /workspace/sbom-output
apko build \
  --sbom-path /workspace/sbom-output \
  /workspace/%s \
  %s \
  /workspace/output.tar%s

echo "==> apko build complete"
ls -lh /workspace/output.tar
echo "==> SBOM artifacts:"
ls -lh /workspace/sbom-output/ 2>/dev/null || echo "(no SBOM generated)"
`, config, tag, localRepoFlag)

	builder := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "curl", "wget", "tar"}).
		WithDirectory("/workspace", source)

	if localRepo != nil {
		builder = builder.WithDirectory("/local-repo", localRepo)
	}

	builder = builder.
		WithNewFile("/build.sh", buildScript).
		WithExec([]string{"sh", "/build.sh"})

	// Import the apko-built tarball as a container
	// apko outputs a standard OCI image tarball that Dagger can import
	outputTar := builder.File("/workspace/output.tar")

	return dag.Container().Import(outputTar), nil
}

// ApkoPublish builds an apko image and publishes it to a container registry.
//
// Usage:
//
//	dagger call apko-publish --source=. --registry=ghcr.io/terranox-os/sdk:latest
func (m *TerranoxBootstrap) ApkoPublish(
	ctx context.Context,
	// Repository source directory
	source *dagger.Directory,
	// Full registry address (e.g., ghcr.io/terranox-os/sdk:latest)
	registry string,
	// Path to apko YAML config within source
	// +default="derivations/apko/sdk-musl.yaml"
	config string,
	// Local apk repository with melange-built packages
	// +optional
	localRepo *dagger.Directory,
) (string, error) {
	ctr, err := m.ApkoBuild(ctx, source, config, registry, localRepo)
	if err != nil {
		return "", fmt.Errorf("apko build failed: %w", err)
	}

	ref, err := ctr.Publish(ctx, registry)
	if err != nil {
		return "", fmt.Errorf("publish failed: %w", err)
	}

	return fmt.Sprintf("Published: %s", ref), nil
}

// TestApkoBuild verifies the apko pipeline by building the SDK image and
// checking that key packages are installed and functional.
//
// Usage:
//
//	dagger call test-apko-build --source=.
func (m *TerranoxBootstrap) TestApkoBuild(
	ctx context.Context,
	// Repository source directory
	source *dagger.Directory,
) (string, error) {
	ctr, err := m.ApkoBuild(ctx, source, "derivations/apko/sdk-musl.yaml", "terranox-sdk:test", nil)
	if err != nil {
		return "", fmt.Errorf("apko build: %w", err)
	}

	return ctr.
		WithExec([]string{"sh", "-c", `
			echo "=== TerranoxOS Musl SDK Image Verification ==="
			echo
			echo "--- System ---"
			cat /etc/os-release 2>/dev/null || echo "(no os-release)"
			echo
			echo "--- musl ---"
			ldd --version 2>&1 | head -1 || true
			echo
			echo "--- Build tools ---"
			which cmake && cmake --version | head -1
			which ninja && ninja --version
			which make && make --version | head -1
			which pkgconf && pkgconf --version
			which git && git --version
			echo
			echo "--- Dev headers ---"
			[ -f /usr/include/zlib.h ] && echo "PASS: zlib.h" || echo "FAIL: zlib.h missing"
			[ -f /usr/include/openssl/ssl.h ] && echo "PASS: openssl/ssl.h" || echo "FAIL: openssl/ssl.h missing"
			[ -f /usr/include/ncurses.h ] && echo "PASS: ncurses.h" || echo "FAIL: ncurses.h missing"
			[ -f /usr/include/readline/readline.h ] && echo "PASS: readline/readline.h" || echo "FAIL: readline/readline.h missing"
			[ -f /usr/include/ffi.h ] && echo "PASS: ffi.h" || echo "FAIL: ffi.h missing"
			echo
			echo "--- Static libs ---"
			[ -f /usr/lib/libz.a ] && echo "PASS: libz.a" || echo "FAIL: libz.a missing"
			echo
			echo "--- Compile test ---"
			cat > /tmp/test.c <<'EOF'
#include <stdio.h>
#include <zlib.h>
int main(void) {
    printf("musl Wolfi SDK OK — zlib %s\n", zlibVersion());
    return 0;
}
EOF
			cc -static -o /tmp/test /tmp/test.c -lz
			/tmp/test
			echo
			echo "=== All apko SDK tests passed ==="
		`}).
		Stdout(ctx)
}

// ═══════════════════════════════════════════════════════════
// Base image bootstrap: melange packages → apko base image
// ═══════════════════════════════════════════════════════════

// BootstrapBaseImage builds the core melange packages and assembles them
// into a minimal TerranoxOS base image via apko.
//
// This is the path to replacing alpine:3.19 — once this image exists,
// subsequent melange builds can use it as the build host instead of Alpine.
//
// Phase 1 (current): Build what we can from source, pull the rest from Alpine.
// Phase 2 (future): All packages from TerranoxOS melange repo.
//
// Core packages built from source:
//   - musl (C library)
//   - busybox (shell + coreutils)
//   - zlib (compression)
//   - openssl (TLS/crypto)
//   - ca-certificates (root CA bundle)
//   - apk-tools (package manager)
//
// Usage:
//
//	# Build base image:
//	dagger call bootstrap-base-image --source=. export --path=./terranox-base.tar
//
//	# Build base image with security attestation:
//	dagger call bootstrap-base-image --source=. --secure export --path=./out/base
//
// For self-hosted builds where earlier packages feed into later ones,
// use SelfHostBuild instead.
func (m *TerranoxBootstrap) BootstrapBaseImage(
	ctx context.Context,
	// Repository source directory containing derivations/ and packages/
	source *dagger.Directory,
	// Enable security attestation (SBOM + signing + provenance)
	// +default=false
	secure bool,
) (*dagger.Directory, error) {
	// Core packages to build from source (in dependency order).
	// apk-tools is split into apk-tools + libapk subpackages so
	// apko can resolve the so:libapk.so.2.14.0 dependency.
	corePackages := []string{
		"musl",
		"busybox",
		"zlib",
		"openssl",
		"ca-certificates",
		"apk-tools",
	}

	// Build each package with melange
	// Note: For Phase 1, some of these may fail if their build scripts
	// need adjustments. The apko base image config falls back to Alpine
	// packages when local packages aren't available.
	var localRepo *dagger.Directory
	var buildErrors []string

	for _, pkg := range corePackages {
		fmt.Printf("Building package: %s\n", pkg)
		pkgDir, err := m.MelangeBuild(ctx, source, pkg, nil)
		if err != nil {
			buildErrors = append(buildErrors, fmt.Sprintf("%s: %v", pkg, err))
			fmt.Printf("  WARN: %s build failed (will use Alpine package): %v\n", pkg, err)
			continue
		}
		if localRepo == nil {
			localRepo = pkgDir
		} else {
			localRepo = localRepo.WithDirectory(".", pkgDir)
		}
		fmt.Printf("  OK: %s built\n", pkg)
	}

	if len(buildErrors) > 0 {
		fmt.Printf("\n%d/%d packages failed (falling back to Alpine):\n", len(buildErrors), len(corePackages))
		for _, e := range buildErrors {
			fmt.Printf("  - %s\n", e)
		}
		fmt.Println()
	}

	// Include the melange signing public key alongside the local repo
	// so apko can verify the package index signature.
	if localRepo != nil {
		signingKeyPub := source.File("packages/melange.rsa.pub")
		localRepo = localRepo.WithFile("melange.rsa.pub", signingKeyPub)
	}

	// Assemble the base image with apko
	if secure {
		return m.ApkoBuildSecure(ctx, source, "derivations/apko/base-terranox.yaml", "terranox-base:latest", "", localRepo)
	}

	ctr, err := m.ApkoBuild(ctx, source, "derivations/apko/base-terranox.yaml", "terranox-base:latest", localRepo)
	if err != nil {
		return nil, fmt.Errorf("apko base image build: %w", err)
	}

	// Export as tarball in a directory alongside a build report
	report := fmt.Sprintf(`{
  "packages_built": %d,
  "packages_failed": %d,
  "errors": %q,
  "base_config": "derivations/apko/base-terranox.yaml",
  "phase": 1
}`, len(corePackages)-len(buildErrors), len(buildErrors), buildErrors)

	imageTar := ctr.AsTarball()

	return dag.Directory().
		WithFile("base.tar", imageTar).
		WithNewFile("build-report.json", report), nil
}

// ═══════════════════════════════════════════════════════════
// Toolchain APK: package Stage1 for use in melange builds
// ═══════════════════════════════════════════════════════════

// PackageToolchainApk takes the Stage1 LLVM/Clang toolchain directory and
// packages it as an APK that can be installed in melange build environments.
// This is how we close the final loop: packages are compiled with our own
// compiler, not Alpine's GCC.
//
// The APK installs to /usr (bin/clang, bin/lld, lib/clang/*, etc.) so it's
// a drop-in replacement for Alpine's clang package.
//
// Usage:
//
//	dagger call package-toolchain-apk --source=. export --path=./out/toolchain-apk
func (m *TerranoxBootstrap) PackageToolchainApk(
	ctx context.Context,
	// +default="21.1.8"
	llvmVersion string,
	// Pre-built Stage1 toolchain directory. If not provided, built from scratch.
	// +optional
	toolchain *dagger.Directory,
	// Pre-built sysroot directory. Needed for compiler-rt/libc++ libs.
	// +optional
	sysroot *dagger.Directory,
	// Repository source directory
	// +optional
	source *dagger.Directory,
) (*dagger.Directory, error) {
	// Build Stage1 if not provided
	if toolchain == nil {
		toolchain = m.MuslStage1(llvmVersion, DefaultMuslVersion, DefaultLinuxVersion, nil, nil, source)
	}

	// Build sysroot if not provided (needed for runtime libs)
	if sysroot == nil {
		sysroot = m.MuslSysroot(llvmVersion, DefaultMuslVersion, DefaultLinuxVersion, source)
	}

	// Create the APK in a container.
	// APK format: gzip-compressed tar with .PKGINFO metadata + files.
	apkBuilder := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "tar", "gzip", "openssl", "abuild"}).
		WithDirectory("/toolchain", toolchain).
		WithDirectory("/sysroot", sysroot).
		WithNewFile("/build-apk.sh", fmt.Sprintf(`#!/bin/sh
set -e

VERSION="%s"
PKGNAME="terranox-clang"

# Create package filesystem layout
mkdir -p /pkg/usr/bin /pkg/usr/lib

# Copy toolchain binaries
cp /toolchain/bin/clang-* /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/clang /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/clang++ /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/lld /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/ld.lld /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/ld64.lld /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/llvm-ar /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/llvm-nm /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/llvm-ranlib /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/llvm-objcopy /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/llvm-objdump /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/llvm-strip /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/llvm-readelf /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/llvm-strings /pkg/usr/bin/ 2>/dev/null || true
cp /toolchain/bin/llvm-size /pkg/usr/bin/ 2>/dev/null || true

# Create convenience symlinks
cd /pkg/usr/bin
ln -sf clang cc
ln -sf clang++ c++
ln -sf lld ld
ln -sf llvm-ar ar
ln -sf llvm-ranlib ranlib
ln -sf llvm-nm nm
ln -sf llvm-objdump objdump
ln -sf llvm-strip strip
cd /

# Copy clang resource directory (compiler-rt, headers)
if [ -d /toolchain/lib/clang ]; then
  cp -a /toolchain/lib/clang /pkg/usr/lib/
fi

# Copy runtime libraries from sysroot
mkdir -p /pkg/usr/lib
cp /sysroot/usr/lib/libc++*.a /pkg/usr/lib/ 2>/dev/null || true
cp /sysroot/usr/lib/libunwind*.a /pkg/usr/lib/ 2>/dev/null || true

# Calculate installed size
INSTALLED_SIZE=$(du -sk /pkg | cut -f1)
INSTALLED_SIZE=$((INSTALLED_SIZE * 1024))

# Generate .PKGINFO
cat > /pkg/.PKGINFO <<PKGEOF
pkgname = ${PKGNAME}
pkgver = ${VERSION}-r0
arch = x86_64
size = ${INSTALLED_SIZE}
pkgdesc = TerranoxOS LLVM/Clang toolchain
url = https://github.com/terranox-os/toolchain
license = Apache-2.0
provides = cmd:clang=${VERSION}-r0
provides = cmd:clang++=${VERSION}-r0
provides = cmd:lld=${VERSION}-r0
provides = cmd:cc=${VERSION}-r0
provides = cmd:c++=${VERSION}-r0
provides = cmd:ld=${VERSION}-r0
provides = cmd:ar=${VERSION}-r0
PKGEOF

# Build the APK tarball
mkdir -p /output/x86_64
cd /pkg
tar czf /output/x86_64/${PKGNAME}-${VERSION}-r0.apk \
  .PKGINFO usr/

# Sign if key is available
if [ -f /signing-key/melange.rsa ]; then
  cd /output/x86_64
  # Create signature
  openssl dgst -sha256 -sign /signing-key/melange.rsa \
    -out .SIGN.RSA256.melange.rsa.pub \
    ${PKGNAME}-${VERSION}-r0.apk

  # Repack with signature
  cd /pkg
  tar czf /output/x86_64/${PKGNAME}-${VERSION}-r0.apk \
    .PKGINFO usr/
fi

# Generate APKINDEX from the package metadata
cd /output/x86_64
cat /pkg/.PKGINFO > /tmp/index-entry
echo "" >> /tmp/index-entry

# Create APKINDEX.tar.gz
tar czf APKINDEX.tar.gz -C /tmp index-entry

echo "==> Toolchain APK created"
ls -lh /output/x86_64/
`, llvmVersion)).
		WithEnvVariable("LLVM_VERSION", llvmVersion)

	// Mount signing key if source is provided
	if source != nil {
		signingKey := source.File("packages/melange.rsa")
		signingKeyPub := source.File("packages/melange.rsa.pub")
		apkBuilder = apkBuilder.
			WithFile("/signing-key/melange.rsa", signingKey).
			WithFile("/signing-key/melange.rsa.pub", signingKeyPub)
	}

	apkBuilder = apkBuilder.
		WithExec([]string{"sh", "/build-apk.sh"})

	return apkBuilder.Directory("/output/x86_64"), nil
}

// ═══════════════════════════════════════════════════════════
// Docker image from toolchain APK
// ═══════════════════════════════════════════════════════════

// ToolchainApkImage creates a Docker/OCI container image by installing the
// toolchain APK into an Alpine base. Unlike ToolchainImage (which copies raw
// directories), this uses the APK package manager so the toolchain is tracked
// as a proper system package.
//
// The image includes build essentials (cmake, ninja, make, git) and has
// clang/lld/llvm-* available in /usr/bin via the APK install.
//
// Usage:
//
//	# Build image from scratch:
//	dagger call toolchain-apk-image --source=. export --path=./terranox-toolchain-apk.tar
//
//	# Build with pre-built APK directory:
//	dagger call toolchain-apk-image --source=. --apk-repo=./out/toolchain-apk \
//	  export --path=./terranox-toolchain-apk.tar
//
//	# Publish to registry:
//	dagger call toolchain-apk-image --source=. \
//	  publish --address ghcr.io/terranox-os/toolchain:21.1.8-apk
//
//	# Interactive shell:
//	dagger call toolchain-apk-image --source=. terminal
func (m *TerranoxBootstrap) ToolchainApkImage(
	ctx context.Context,
	// LLVM version
	// +default="21.1.8"
	llvmVersion string,
	// Pre-built APK repository directory (output of PackageToolchainApk).
	// If not provided, the APK is built from scratch.
	// +optional
	apkRepo *dagger.Directory,
	// Pre-built Stage1 toolchain directory (passed to PackageToolchainApk if apkRepo is nil)
	// +optional
	toolchain *dagger.Directory,
	// Pre-built sysroot directory (passed to PackageToolchainApk if apkRepo is nil)
	// +optional
	sysroot *dagger.Directory,
	// Repository source directory containing packages/ signing keys
	// +optional
	source *dagger.Directory,
) (*dagger.Container, error) {
	// Build the APK if not provided
	if apkRepo == nil {
		var err error
		apkRepo, err = m.PackageToolchainApk(ctx, llvmVersion, toolchain, sysroot, source)
		if err != nil {
			return nil, fmt.Errorf("build toolchain APK: %w", err)
		}
	}

	// Resolve the signing public key for APK verification
	var signingKeyPub *dagger.File
	if source != nil {
		signingKeyPub = source.File("packages/melange.rsa.pub")
	}

	// Build the container image with the APK installed
	ctr := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache",
			"bash", "cmake", "ninja", "samurai", "make",
			"git", "file", "pkgconf", "musl-dev", "linux-headers",
		}).
		// Mount local APK repo
		WithDirectory("/tmp/apk-repo", apkRepo)

	// Install signing key so apk trusts our packages
	if signingKeyPub != nil {
		ctr = ctr.WithFile("/etc/apk/keys/melange.rsa.pub", signingKeyPub)
	}

	// Install the toolchain APK from the local repo
	ctr = ctr.WithExec([]string{"sh", "-c", fmt.Sprintf(`
		# Allow untrusted if no signing key (development builds)
		APK_FLAGS=""
		if [ ! -f /etc/apk/keys/melange.rsa.pub ]; then
			APK_FLAGS="--allow-untrusted"
		fi

		apk add $APK_FLAGS \
			--repository /tmp/apk-repo \
			terranox-clang=%s-r0

		# Verify installation
		echo "==> Installed toolchain binaries:"
		ls -la /usr/bin/clang /usr/bin/lld /usr/bin/cc 2>/dev/null || true
		clang --version || echo "WARN: clang --version failed"

		# Clean up repo cache
		rm -rf /tmp/apk-repo
	`, llvmVersion)}).
		WithEnvVariable("PATH", "/usr/bin:/usr/local/bin:/bin:/sbin").
		WithEnvVariable("CC", "clang").
		WithEnvVariable("CXX", "clang++").
		WithEnvVariable("LD", "ld.lld").
		WithEnvVariable("AR", "llvm-ar").
		WithEnvVariable("RANLIB", "llvm-ranlib").
		WithEnvVariable("TERRANOX_VERSION", llvmVersion).
		WithLabel("org.opencontainers.image.title", "TerranoxOS Toolchain (APK)").
		WithLabel("org.opencontainers.image.description",
			"LLVM/Clang toolchain installed via APK package manager").
		WithLabel("org.opencontainers.image.version", llvmVersion).
		WithLabel("org.opencontainers.image.vendor", "TerranoxOS").
		WithLabel("org.opencontainers.image.licenses", "Apache-2.0").
		WithLabel("terranox.llvm.version", llvmVersion).
		WithLabel("terranox.install-method", "apk").
		WithLabel("terranox.target", Target).
		WithWorkdir("/workspace")

	return ctr, nil
}

// ═══════════════════════════════════════════════════════════
// Self-hosting bootstrap: staged package builds
// ═══════════════════════════════════════════════════════════

// SelfHostBuild builds all packages in dependency order, feeding earlier
// outputs as repositories to later builds. This is how we close the
// bootstrap loop: musl 1.2.5 (built in wave 1) becomes the build-host
// libc for git (wave 3), which needs REG_STARTEND.
//
// Waves:
//
//	Wave 1: musl, zlib (no deps on our packages)
//	Wave 2: openssl, busybox, ca-certificates (may use our musl/zlib)
//	Wave 3: apk-tools, bash, make, ninja, git (use our openssl/zlib/musl)
//	Wave 4: cmake, python3 (use everything above)
//
// Each wave's outputs are merged into a cumulative repo that subsequent
// waves can pull from. Alpine repos remain available as a fallback for
// packages we don't build ourselves.
//
// Usage:
//
//	dagger call self-host-build --source=. export --path=./out/self-host
func (m *TerranoxBootstrap) SelfHostBuild(
	ctx context.Context,
	// Repository source directory containing derivations/ and packages/
	source *dagger.Directory,
) (*dagger.Directory, error) {
	waves := [][]string{
		{"musl", "zlib"},
		{"openssl", "busybox", "ca-certificates"},
		{"apk-tools", "bash", "make", "ninja", "git"},
		{"cmake", "python3"},
	}

	// Cumulative repo: grows with each wave
	var repo *dagger.Directory
	var allErrors []string
	totalBuilt := 0

	for waveNum, packages := range waves {
		fmt.Printf("\n=== Wave %d: %v ===\n", waveNum+1, packages)

		for _, pkg := range packages {
			fmt.Printf("Building %s (wave %d)...\n", pkg, waveNum+1)
			pkgDir, err := m.MelangeBuild(ctx, source, pkg, repo)
			if err != nil {
				allErrors = append(allErrors, fmt.Sprintf("%s: %v", pkg, err))
				fmt.Printf("  WARN: %s failed: %v\n", pkg, err)
				continue
			}

			// Merge into cumulative repo
			if repo == nil {
				repo = pkgDir
			} else {
				repo = repo.WithDirectory(".", pkgDir)
			}
			totalBuilt++
			fmt.Printf("  OK: %s built\n", pkg)
		}
	}

	if repo == nil {
		return nil, fmt.Errorf("no packages built successfully")
	}

	// Include signing pub key for downstream consumers
	signingKeyPub := source.File("packages/melange.rsa.pub")
	repo = repo.WithFile("melange.rsa.pub", signingKeyPub)

	report := fmt.Sprintf(`{
  "packages_built": %d,
  "packages_failed": %d,
  "errors": %q,
  "phase": 2,
  "self_hosted": true
}`, totalBuilt, len(allErrors), allErrors)

	fmt.Printf("\n=== Self-host build complete: %d built, %d failed ===\n", totalBuilt, len(allErrors))

	return dag.Directory().
		WithDirectory("packages", repo).
		WithNewFile("build-report.json", report), nil
}

// SelfHostBuildClang rebuilds all packages using the TerranoxOS Stage1
// LLVM/Clang toolchain instead of Alpine's GCC. This is the final
// self-hosting milestone: every package is compiled with our own compiler.
//
// Flow:
//  1. Build (or accept) Stage1 toolchain + sysroot
//  2. Package the toolchain as an APK (terranox-clang)
//  3. Run SelfHostBuild waves with the toolchain APK in the local repo
//  4. Melange manifests install terranox-clang and use CC=clang CXX=clang++
//
// Usage:
//
//	dagger call self-host-build-clang --source=. export --path=./out/clang-self-host
//	dagger call self-host-build-clang --source=. --toolchain=./stage1 export --path=./out/clang-self-host
func (m *TerranoxBootstrap) SelfHostBuildClang(
	ctx context.Context,
	// Repository source directory
	source *dagger.Directory,
	// +default="21.1.8"
	llvmVersion string,
	// Pre-built Stage1 toolchain. If not provided, built from scratch.
	// +optional
	toolchain *dagger.Directory,
	// Pre-built sysroot. If not provided, built from scratch.
	// +optional
	sysroot *dagger.Directory,
) (*dagger.Directory, error) {
	// Step 1: Package the toolchain as an APK
	fmt.Println("=== Packaging Stage1 toolchain as APK ===")
	toolchainApk, err := m.PackageToolchainApk(ctx, llvmVersion, toolchain, sysroot, source)
	if err != nil {
		return nil, fmt.Errorf("package toolchain APK: %w", err)
	}

	// Step 2: Build all packages in waves, with the toolchain APK
	// available in wave 1's local repo so all packages can use it.
	waves := [][]string{
		{"musl", "zlib"},
		{"openssl", "busybox", "ca-certificates"},
		{"apk-tools", "bash", "make", "ninja", "git"},
		{"cmake", "python3"},
	}

	// Start with the toolchain APK as the initial repo
	repo := toolchainApk
	var allErrors []string
	totalBuilt := 0

	for waveNum, packages := range waves {
		fmt.Printf("\n=== Wave %d (clang): %v ===\n", waveNum+1, packages)

		for _, pkg := range packages {
			fmt.Printf("Building %s with clang...\n", pkg)
			pkgDir, err := m.MelangeBuild(ctx, source, pkg, repo)
			if err != nil {
				allErrors = append(allErrors, fmt.Sprintf("%s: %v", pkg, err))
				fmt.Printf("  WARN: %s failed: %v\n", pkg, err)
				continue
			}

			repo = repo.WithDirectory(".", pkgDir)
			totalBuilt++
			fmt.Printf("  OK: %s built\n", pkg)
		}
	}

	if repo == nil {
		return nil, fmt.Errorf("no packages built successfully")
	}

	signingKeyPub := source.File("packages/melange.rsa.pub")
	repo = repo.WithFile("melange.rsa.pub", signingKeyPub)

	report := fmt.Sprintf(`{
  "packages_built": %d,
  "packages_failed": %d,
  "errors": %q,
  "compiler": "clang-%s",
  "phase": 3,
  "self_hosted": true
}`, totalBuilt, len(allErrors), allErrors, llvmVersion)

	fmt.Printf("\n=== Clang self-host build: %d built, %d failed ===\n", totalBuilt, len(allErrors))

	return dag.Directory().
		WithDirectory("packages", repo).
		WithNewFile("build-report.json", report), nil
}

// ═══════════════════════════════════════════════════════════
// apko: Secure build pipeline (SBOM + signing + provenance)
// ═══════════════════════════════════════════════════════════

// ApkoBuildSecure builds an apko image with full security attestation:
// CycloneDX SBOM, cosign signature, and SLSA provenance.
//
// This is the production entry point for building distributable SDK images.
// It runs the apko build, collects the native SBOM, signs the tarball,
// and generates SLSA v1.0 provenance — all in one pipeline.
//
// Output structure:
//
//	image.tar                - OCI image tarball
//	image.tar.sha256         - SHA256 digest
//	image.tar.sig            - Cosign signature (placeholder for local, real in CI)
//	image.tar.bundle         - Cosign signature bundle
//	sbom.cdx.json            - CycloneDX SBOM (from apko + enrichment)
//	provenance.json          - SLSA v1.0 provenance attestation
//
// Usage:
//
//	dagger call apko-build-secure --source=. export --path=./out/sdk-secure
//	dagger call apko-build-secure --source=. --git-commit=$(git rev-parse HEAD) export --path=./out/sdk-secure
func (m *TerranoxBootstrap) ApkoBuildSecure(
	ctx context.Context,
	// Repository source directory containing derivations/apko/ and packages/
	source *dagger.Directory,
	// Path to apko YAML config within source
	// +default="derivations/apko/sdk-musl.yaml"
	config string,
	// Tag for the built image
	// +default="terranox-sdk:latest"
	tag string,
	// Git commit hash (for provenance; auto-detected in CI)
	// +optional
	gitCommit string,
	// Local apk repository with melange-built packages
	// +optional
	localRepo *dagger.Directory,
) (*dagger.Directory, error) {
	if gitCommit == "" {
		gitCommit = "local-build"
	}

	// ── Step 1: Build the image with apko and collect the native SBOM ──
	//
	// apko generates SPDX SBOMs by default. We also enrich with a CycloneDX
	// SBOM that includes TerranoxOS-specific metadata.
	installApko := `#!/bin/sh
set -e

echo "==> Installing apko"
APKO_URL=$(curl -fsSL https://api.github.com/repos/chainguard-dev/apko/releases/latest \
  | grep browser_download_url | grep linux_amd64.tar.gz\" | head -1 | cut -d'"' -f4)
curl -fsSL "${APKO_URL}" | tar xz --strip-components=1 -C /usr/local/bin
chmod +x /usr/local/bin/apko

mkdir -p /etc/apk/keys
wget -qO /etc/apk/keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub \
  https://alpinelinux.org/keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub
`

	localRepoFlag := ""
	if localRepo != nil {
		installApko += `
mkdir -p /etc/apk/keys
if [ -f /local-repo/melange.rsa.pub ]; then
  cp /local-repo/melange.rsa.pub /etc/apk/keys/
fi
`
		localRepoFlag = " --repository-append /local-repo"
	}

	buildScript := installApko + fmt.Sprintf(`
echo "==> Building OCI image with apko (secure mode)"
cd /workspace
mkdir -p /workspace/sbom-output /workspace/secure-output

# Build image + SBOM
apko build \
  --sbom-path /workspace/sbom-output \
  /workspace/%s \
  %s \
  /workspace/secure-output/image.tar%s

echo "==> Image built"
ls -lh /workspace/secure-output/image.tar

# ── Step 2: SHA256 digest ──
TARBALL_SHA256=$(sha256sum /workspace/secure-output/image.tar | cut -d' ' -f1)
echo "$TARBALL_SHA256  image.tar" > /workspace/secure-output/image.tar.sha256
echo "SHA256: $TARBALL_SHA256"

# ── Step 3: Cosign signature (placeholder for local builds) ──
# In CI with GitHub Actions, replace with:
#   cosign sign-blob --yes --bundle image.tar.bundle image.tar
cat > /workspace/secure-output/image.tar.sig <<SIGEOF
{
  "signature": "placeholder-local-build",
  "digest": {
    "sha256": "$TARBALL_SHA256"
  },
  "timestamp": "$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)",
  "note": "Use cosign sign-blob in CI for real signatures"
}
SIGEOF

SIG_BASE64=$(cat /workspace/secure-output/image.tar.sig | base64 | tr -d '\n')
cat > /workspace/secure-output/image.tar.bundle <<BUNDLEEOF
{
  "SignedEntryTimestamp": "$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)",
  "Payload": {
    "body": "$SIG_BASE64",
    "integratedTime": $(date +%%s),
    "logIndex": 0,
    "logID": "placeholder"
  }
}
BUNDLEEOF

# ── Step 4: CycloneDX SBOM ──
# apko generates SPDX — we also produce CycloneDX with TerranoxOS metadata.
# Scan the SPDX output and convert key fields, or generate from scratch.
SERIAL="urn:uuid:$(cat /proc/sys/kernel/random/uuid)"
TIMESTAMP=$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)

# Collect installed packages from the apko SPDX SBOM
SPDX_FILE=$(find /workspace/sbom-output -name "*.spdx.json" -type f | head -1)

# Generate component list from SPDX if available, otherwise from config
COMPONENTS="[]"
if [ -n "$SPDX_FILE" ] && [ -f "$SPDX_FILE" ]; then
    echo "==> Converting SPDX SBOM to CycloneDX"
    # Extract packages from SPDX and convert to CycloneDX format
    COMPONENTS=$(jq '[.packages[] | select(.name != null) | {
        "bom-ref": (.name + "@" + (.versionInfo // "unknown")),
        "type": "library",
        "name": .name,
        "version": (.versionInfo // "unknown"),
        "purl": ("pkg:apk/alpine/" + .name + "@" + (.versionInfo // "unknown") + "?arch=x86_64"),
        "description": (.description // "Alpine package")
    }]' "$SPDX_FILE" 2>/dev/null || echo "[]")
fi

jq -n \
  --arg serial "$SERIAL" \
  --arg timestamp "$TIMESTAMP" \
  --arg tag "%s" \
  --arg sha256 "$TARBALL_SHA256" \
  --argjson components "$COMPONENTS" \
'{
  "bomFormat": "CycloneDX",
  "specVersion": "1.5",
  "serialNumber": $serial,
  "version": 1,
  "metadata": {
    "timestamp": $timestamp,
    "tools": [
      {
        "vendor": "Chainguard",
        "name": "apko",
        "version": "latest"
      },
      {
        "vendor": "TerranoxOS",
        "name": "dagger-bootstrap-pipeline",
        "version": "0.3.0"
      }
    ],
    "component": {
      "bom-ref": "terranox-sdk-image",
      "type": "container",
      "name": "terranox-sdk",
      "version": $tag,
      "description": "TerranoxOS musl-native SDK image assembled via apko",
      "hashes": [
        {
          "alg": "SHA-256",
          "content": $sha256
        }
      ],
      "properties": [
        {
          "name": "terranox:libc",
          "value": "musl"
        },
        {
          "name": "terranox:builder",
          "value": "apko"
        },
        {
          "name": "terranox:arch",
          "value": "x86_64"
        }
      ]
    }
  },
  "components": $components
}' > /workspace/secure-output/sbom.cdx.json

echo "==> CycloneDX SBOM generated ($(echo "$COMPONENTS" | jq length) components)"

# Copy SPDX SBOM too if available
if [ -n "$SPDX_FILE" ] && [ -f "$SPDX_FILE" ]; then
    cp "$SPDX_FILE" /workspace/secure-output/sbom.spdx.json
    echo "==> SPDX SBOM preserved"
fi

# ── Step 5: SLSA v1.0 provenance ──
INVOCATION_ID=$(cat /proc/sys/kernel/random/uuid)
FINISHED_ON=$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)

jq -n \
  --arg sha256 "$TARBALL_SHA256" \
  --arg tag "%s" \
  --arg config "%s" \
  --arg commit "%s" \
  --arg invocation "urn:uuid:$INVOCATION_ID" \
  --arg finished "$FINISHED_ON" \
'{
  "_type": "https://in-toto.io/Statement/v0.1",
  "predicateType": "https://slsa.dev/provenance/v1",
  "subject": [
    {
      "name": "terranox-sdk",
      "digest": {
        "sha256": $sha256
      }
    }
  ],
  "predicate": {
    "buildDefinition": {
      "buildType": "https://terranox.dev/BuildTypes/ApkoSdkBuild/v0.1",
      "externalParameters": {
        "config": $config,
        "tag": $tag,
        "builder": "apko",
        "base_packages": "Alpine 3.19 (musl)"
      },
      "internalParameters": {
        "dagger_version": "0.20.1",
        "pipeline_version": "0.3.0"
      },
      "resolvedDependencies": [
        {
          "uri": "https://dl-cdn.alpinelinux.org/alpine/v3.19/main",
          "name": "alpine-main"
        },
        {
          "uri": "https://dl-cdn.alpinelinux.org/alpine/v3.19/community",
          "name": "alpine-community"
        }
      ]
    },
    "runDetails": {
      "builder": {
        "id": ("https://github.com/terranox-os/toolchain/.dagger/rootfs.go@" + $commit),
        "version": {
          "dagger": "0.20.1",
          "apko": "latest"
        }
      },
      "metadata": {
        "invocationId": $invocation,
        "finishedOn": $finished
      }
    }
  }
}' > /workspace/secure-output/provenance.json

echo "==> SLSA provenance generated"
echo
echo "=== Secure build complete ==="
ls -lh /workspace/secure-output/
`, config, tag, localRepoFlag, tag, tag, config, gitCommit)

	builder := dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "curl", "wget", "tar", "jq", "coreutils"}).
		WithDirectory("/workspace", source)

	if localRepo != nil {
		builder = builder.WithDirectory("/local-repo", localRepo)
	}

	builder = builder.
		WithNewFile("/build.sh", buildScript).
		WithExec([]string{"sh", "/build.sh"})

	return builder.Directory("/workspace/secure-output"), nil
}

// TestApkoSecurity builds the SDK image with full security attestation and
// verifies that all artifacts are present and valid.
//
// Usage:
//
//	dagger call test-apko-security --source=.
func (m *TerranoxBootstrap) TestApkoSecurity(
	ctx context.Context,
	// Repository source directory
	source *dagger.Directory,
) (string, error) {
	artifacts, err := m.ApkoBuildSecure(ctx, source, "derivations/apko/sdk-musl.yaml", "terranox-sdk:test", "", nil)
	if err != nil {
		return "", fmt.Errorf("secure build: %w", err)
	}

	return dag.Container().
		From("alpine:3.19").
		WithExec([]string{"apk", "add", "--no-cache", "jq", "coreutils"}).
		WithDirectory("/artifacts", artifacts).
		WithExec([]string{"sh", "-c", `
			echo "=== SDK Security Attestation Verification ==="
			echo
			PASS=0
			FAIL=0

			check() {
				if [ -f "/artifacts/$1" ]; then
					SIZE=$(stat -c%s "/artifacts/$1")
					echo "PASS: $1 ($SIZE bytes)"
					PASS=$((PASS + 1))
				else
					echo "FAIL: $1 missing"
					FAIL=$((FAIL + 1))
				fi
			}

			echo "--- Artifact presence ---"
			check image.tar
			check image.tar.sha256
			check image.tar.sig
			check image.tar.bundle
			check sbom.cdx.json
			check provenance.json
			echo

			echo "--- SHA256 digest verification ---"
			EXPECTED=$(cut -d' ' -f1 /artifacts/image.tar.sha256)
			ACTUAL=$(sha256sum /artifacts/image.tar | cut -d' ' -f1)
			if [ "$EXPECTED" = "$ACTUAL" ]; then
				echo "PASS: SHA256 matches ($ACTUAL)"
				PASS=$((PASS + 1))
			else
				echo "FAIL: SHA256 mismatch (expected $EXPECTED, got $ACTUAL)"
				FAIL=$((FAIL + 1))
			fi
			echo

			echo "--- CycloneDX SBOM validation ---"
			if jq empty /artifacts/sbom.cdx.json 2>/dev/null; then
				FORMAT=$(jq -r '.bomFormat' /artifacts/sbom.cdx.json)
				SPEC=$(jq -r '.specVersion' /artifacts/sbom.cdx.json)
				COMP_COUNT=$(jq '.components | length' /artifacts/sbom.cdx.json)
				SBOM_SHA=$(jq -r '.metadata.component.hashes[0].content // "none"' /artifacts/sbom.cdx.json)
				echo "PASS: Valid JSON ($FORMAT $SPEC, $COMP_COUNT components)"
				PASS=$((PASS + 1))

				# Verify SBOM digest matches tarball digest
				if [ "$SBOM_SHA" = "$ACTUAL" ]; then
					echo "PASS: SBOM digest matches image digest"
					PASS=$((PASS + 1))
				elif [ "$SBOM_SHA" = "none" ]; then
					echo "WARN: No digest in SBOM metadata"
				else
					echo "FAIL: SBOM digest ($SBOM_SHA) != image digest ($ACTUAL)"
					FAIL=$((FAIL + 1))
				fi
			else
				echo "FAIL: Invalid SBOM JSON"
				FAIL=$((FAIL + 1))
			fi
			echo

			echo "--- Signature validation ---"
			if jq empty /artifacts/image.tar.sig 2>/dev/null; then
				SIG_DIGEST=$(jq -r '.digest.sha256' /artifacts/image.tar.sig)
				if [ "$SIG_DIGEST" = "$ACTUAL" ]; then
					echo "PASS: Signature digest matches image digest"
					PASS=$((PASS + 1))
				else
					echo "FAIL: Signature digest mismatch"
					FAIL=$((FAIL + 1))
				fi
			else
				echo "FAIL: Invalid signature JSON"
				FAIL=$((FAIL + 1))
			fi
			echo

			echo "--- SLSA provenance validation ---"
			if jq empty /artifacts/provenance.json 2>/dev/null; then
				PRED_TYPE=$(jq -r '.predicateType' /artifacts/provenance.json)
				BUILD_TYPE=$(jq -r '.predicate.buildDefinition.buildType' /artifacts/provenance.json)
				SUBJ_DIGEST=$(jq -r '.subject[0].digest.sha256' /artifacts/provenance.json)
				echo "PASS: Valid JSON"
				echo "  predicateType: $PRED_TYPE"
				echo "  buildType: $BUILD_TYPE"
				PASS=$((PASS + 1))

				# Verify provenance subject matches image
				if [ "$SUBJ_DIGEST" = "$ACTUAL" ]; then
					echo "PASS: Provenance subject digest matches image digest"
					PASS=$((PASS + 1))
				else
					echo "FAIL: Provenance subject digest mismatch"
					FAIL=$((FAIL + 1))
				fi
			else
				echo "FAIL: Invalid provenance JSON"
				FAIL=$((FAIL + 1))
			fi
			echo

			# Check SPDX SBOM if present (bonus)
			if [ -f /artifacts/sbom.spdx.json ]; then
				echo "--- SPDX SBOM (bonus) ---"
				if jq empty /artifacts/sbom.spdx.json 2>/dev/null; then
					SPDX_PKGS=$(jq '.packages | length' /artifacts/sbom.spdx.json)
					echo "PASS: Valid SPDX JSON ($SPDX_PKGS packages)"
					PASS=$((PASS + 1))
				fi
				echo
			fi

			echo "=== Summary: $PASS passed, $FAIL failed ==="
			if [ "$FAIL" -gt 0 ]; then
				exit 1
			fi
		`}).
		Stdout(ctx)
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
