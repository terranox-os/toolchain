#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Apply TerranoxOS patches to LLVM source tree
#
# Usage:
#   cd /path/to/llvm-project
#   /path/to/terranox-os/patches/llvm/apply_patches.sh

set -euo pipefail

# Determine script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Check we're in an LLVM source tree
if [[ ! -d "llvm" ]] || [[ ! -d "clang" ]]; then
    echo "Error: Must run this script from llvm-project root directory" >&2
    echo "Expected directories: llvm/, clang/" >&2
    exit 1
fi

# Check for clean git state
if ! git diff-index --quiet HEAD -- 2>/dev/null; then
    echo "Warning: Working directory has uncommitted changes" >&2
    read -p "Continue anyway? [y/N] " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

echo "Applying TerranoxOS patches to LLVM..."
echo

# Apply each patch in order
for patch in "$SCRIPT_DIR"/*.patch; do
    patch_name="$(basename "$patch")"
    echo "Applying: $patch_name"

    if git am < "$patch"; then
        echo "✓ $patch_name applied successfully"
    else
        echo "✗ Failed to apply $patch_name" >&2
        echo "  Fix conflicts and run: git am --continue" >&2
        exit 1
    fi
    echo
done

echo "All patches applied successfully!"
echo
echo "Next steps:"
echo "  1. Build LLVM: cmake -S llvm -B build -G Ninja -DLLVM_ENABLE_PROJECTS='clang;lld'"
echo "  2. Compile: ninja -C build"
echo "  3. Test: clang --target=x86_64-pc-terranox -dM -E - < /dev/null"
echo
