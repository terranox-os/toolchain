// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"dagger/terranox-bootstrap/internal/dagger"

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
	Sdk    SdkConfig    `toml:"sdk"`
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
	Jobs      string        `toml:"jobs"`
	Projects  []string      `toml:"projects"`
	Targets   []string      `toml:"targets"`
	Runtimes  []string      `toml:"runtimes"`
	DisableRT []string      `toml:"disable_rt"`
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

type SdkConfig struct {
	Packages []string `toml:"packages"`
}

// LoadConfig reads bootstrap.toml from the given directory.
// Returns sensible defaults if the file doesn't exist.
func LoadConfig(ctx context.Context, source *dagger.Directory) (*Config, error) {
	cfg := defaultConfig()

	// Try to read bootstrap.toml from the source directory
	content, err := source.File("bootstrap.toml").Contents(ctx)
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
			Default: "21.1.8",
			Matrix:  []string{"main", "22.1.1", "21.1.8", "20.1.8", "19.1.7"},
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
		Sdk: SdkConfig{
			Packages: []string{"zlib-dev", "zlib-static", "openssl-dev", "openssl-libs-static", "ncurses-dev", "readline-dev"},
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
