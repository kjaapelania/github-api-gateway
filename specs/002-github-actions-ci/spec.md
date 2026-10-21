# Feature Specification: Automated CI with Ports & Adapters (Mise, GitHub Actions, and Act)

**Feature Branch**: `002-github-actions-ci`  
**Created**: 2026-10-02  
**Status**: Ready for Implementation  
**Input**: "Automate Continuous Integration with GitHub Actions, mise, and act using Ports and Adapters architecture"

---

## 1. System Architecture: Ports & Adapters

The CI and task automation in this repository strictly adheres to the **Hexagonal (Ports and Adapters)** architecture, organized into modular domain files:

```text
+-----------------------------------------------------------------------------------+
|                        DRIVING ADAPTERS (When & Where)                            |
+-----------------------------------------------------------------------------------+
|  [ Developer Shell ]        [ Local Container Runner ]    [ GitHub Actions (CI) ] |
|   `mise run check-all`          `.act/ci.sh [job]`           `.github/workflows/` |
+-----------------------------------------------------------------------------------+
                                         │
                                         ▼ (invokes)
+-----------------------------------------------------------------------------------+
|               CORE PORT: mise.toml & .mise/tasks/*.toml (What & How)              |
+-----------------------------------------------------------------------------------+
|  mise.toml                ──► Pinned tools only (go, act, watchexec)              |
|                                                                                   |
|  .mise/tasks/check.toml   ──► Verification Gate (check-all, check:fmt, vet, test) |
|  .mise/tasks/build.toml   ──► Compilation & Cross-Compilation (build-all, ...)    |
|  .mise/tasks/bin.toml     ──► Application & Runtime Lifecycle (bin:run, ...)      |
|  .mise/tasks/dev.toml     ──► Developer Ergonomics (dev:fmt, dev:watch)           |
|  .mise/tasks/docker.toml  ──► Container Operations (docker:build-debian, etc.)     |
+-----------------------------------------------------------------------------------+
```

### Core Architecture Rules:
- **Core Port (`mise.toml` + `.mise/tasks/*.toml`)**: Authoritative, vendor-neutral definition of tools (`go`, `act`, `watchexec`) and application tasks. Split cleanly across domain TOML files in `.mise/tasks/` to eliminate single-file bloat while avoiding 1-line script file proliferation.
- **Driving Adapters**:
  - **Remote Cloud Adapter (`.github/workflows/ci.yml` & `.github/workflows/release.yml`)**: Thin GitHub Actions workflows invoking granular `mise run check:*`, `mise run docker:build-*`, and `mise run build-all` steps.
  - **Local Simulation Adapter (`.act/ci.sh`)**: Standalone shell script providing Docker pre-flight checks and running `act` locally with 1:1 symmetry to `ci.yml`.
  - **AI Release Skill Adapter (`.agents/skills/release/SKILL.md`)**: Guided Keep-a-Changelog release preparation and annotated Git tag workflow.
- **Pure Verification Gate**: `check-all` runs strictly read-only checks (`fmt`, `vet`, `test`), creating zero build artifacts.
- **Zero Aliases Rule**: Every task has exactly one canonical name; no duplicate or redirect tasks exist.
- **Visual Ergonomics**: Standardizes on `x64` instead of `amd64` in task names and output binaries to prevent confusion with `arm64`.
- **Conflict-Free Environment Variable Rule**: All project environment variables MUST use the `GITHUB_API_GATEWAY_` prefix (e.g. `GITHUB_API_GATEWAY_CONTAINER_TARGET`).

---

## 2. Container Flavor & Tagging Strategy

The repository provides three distinct container targets from a single multi-stage `Dockerfile`:
1. **Debian (`debian:bookworm-slim`) [Default]**: Standard GNU coreutils, `bash`, and `curl` for seamless local development, testing, and troubleshooting.
2. **Alpine (`alpine:3.20`)**: Traditional minimal container image (<10 MB, `ca-certificates`, `tzdata`, BusyBox `/bin/sh`).
3. **Distroless (`gcr.io/distroless/static-debian12:nonroot`)**: SOTA hardened, zero-trust production image (<2MB base overhead, 0 CVEs, zero shell `/bin/sh`, non-root user).

### Explicit Tagging Convention:
All container tags explicitly declare their base flavor (`-debian`, `-alpine`, `-distroless`), and timestamped commit tags include UTC date and time:
- **Local Dev**:
  - `github-api-gateway:debian` (default)
  - `github-api-gateway:alpine`
  - `github-api-gateway:distroless`
  - `github-api-gateway:debian-latest` & `github-api-gateway:latest` (convenience tags for Debian)
  - `github-api-gateway:alpine-latest` (convenience tag for Alpine)
  - `github-api-gateway:distroless-latest` (convenience tag for Distroless)
- **CI / Test Images (`ghcr.io/<owner>/github-api-gateway-test`)**:
  - `...-test:pr-<num>-debian`
  - `...-test:pr-<num>-alpine`
  - `...-test:pr-<num>-distroless`
  - `...-test:YYYYMMDD-HHMM-sha-<hash>-debian`
  - `...-test:YYYYMMDD-HHMM-sha-<hash>-alpine`
  - `...-test:YYYYMMDD-HHMM-sha-<hash>-distroless`
- **Production Releases (`ghcr.io/<owner>/github-api-gateway`)**:
  - `...:<version>-debian`
  - `...:<version>-alpine`
  - `...:<version>-distroless`
  - `...:debian-latest` & `...:latest` (points to `<version>-debian`)
  - `...:alpine-latest` (points to `<version>-alpine`)
  - `...:distroless-latest` (points to `<version>-distroless`)

---

## 3. User Scenarios & Testing

### User Story 1 - Hermetic Automated CI Pipeline on GitHub (Priority: P1)

As an engineer opening pull requests or pushing to `main`, I want GitHub Actions to automatically run granular verification steps (`check:fmt`, `check:vet`, `check:test`) and verify container builds (`docker:build-debian`, `docker:build-alpine`, `docker:build-distroless`) via `mise`, so that regressions, formatting issues, static analysis errors, and container build defects are caught before merging.

- **Independent Test**: Pushing a commit triggers `.github/workflows/ci.yml`, which executes `mise run check:fmt`, `mise run check:vet`, `mise run check:test`, and container build verification tasks.
- **Acceptance Scenarios**:
  1. **Given** clean Go code, **When** pushed to GitHub, **Then** matrix jobs (Go 1.25.x on `ubuntu-latest`) execute `check:fmt`, `check:vet`, and `check:test`, completing successfully.
  2. **Given** unformatted Go code, **When** CI executes, **Then** the `Verify Code Formatting` step fails immediately with exit code 1, while vet and test steps are clearly differentiated.
  3. **Given** container verification, **Then** Debian, Alpine, and Distroless container targets compile successfully via `mise run docker:build-debian`, `mise run docker:build-alpine`, and `mise run docker:build-distroless`.

---

### User Story 2 - Local Containerized CI Simulation via Act Adapter (Priority: P2)

As a developer iterating locally, I want to execute the exact GitHub Actions workflow inside a container using `.act/ci.sh`, so that I can reproduce CI runs on macOS Apple Silicon without cloud turnaround times and with clear pre-flight Docker validation.

- **Independent Test**: Running `.act/ci.sh` triggers `act -j test` against local Docker and passes all verification steps cleanly.
- **Acceptance Scenarios**:
  1. **Given** Docker is not running on the host, **When** running `.act/ci.sh`, **Then** the script halts with a clear error instructing the user to start Docker/Colima.
  2. **Given** Docker running on the host, **When** running `.act/ci.sh`, **Then** `act` loads `.actrc`, pulls the runner container, executes the workflow, and finishes exit code 0.
  3. **Given** local development, **When** running individual tasks (`mise run check:test` or `mise run build:local`), **Then** tasks execute natively without container overhead.

---

### User Story 3 - Multi-Platform Distribution Builds (Priority: P3)

As a release engineer, I want reproducible cross-compilation tasks (`build-all`) covering Linux, macOS, and Windows with clear `x64` and `arm64` naming, static `CGO_ENABLED=0` linking, `-X main.version` injection, and SHA-256 checksum generation (`build:checksums`), so that release binaries and integrity manifests can be built deterministically from any developer machine or CI runner.

- **Independent Test**: Running `VERSION=v0.1.0 mise run build-all` creates static stripped binaries in `bin/` for Linux (`x64`, `arm64`), Darwin (`x64`, `arm64`), and Windows (`x64.exe`), plus `bin/checksums.txt`.

---

### User Story 4 - Automated Tag-Driven Release Pipeline & Release Skill (Priority: P3)

As a maintainer cutting a new semantic version release, I want an interactive AI skill (`.agents/skills/release/SKILL.md`) that drafts human-readable [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) entries in `CHANGELOG.md`, runs `mise run check-all` and `mise run build-all`, and pushes an annotated `vX.Y.Z` tag that triggers `.github/workflows/release.yml` to idempotently publish binaries and `checksums.txt` to GitHub Releases without duplicate-run race conditions.

- **Independent Test**: Pushing a `v*` tag triggers `.github/workflows/release.yml`, which deduplicates concurrent runs via `concurrency`, extracts the matching version section from `CHANGELOG.md`, compiles all 5 platform binaries and `checksums.txt` via `mise run build-all`, and publishes or updates the GitHub Release idempotently.
- **Acceptance Scenarios**:
  1. **Given** an annotated `vX.Y.Z` tag with a matching `## [X.Y.Z]` section in `CHANGELOG.md`, **When** pushed to GitHub, **Then** `.github/workflows/release.yml` runs `mise run check-all`, runs `VERSION=vX.Y.Z mise run build-all`, extracts the release notes from `CHANGELOG.md`, and publishes all 5 platform binaries plus `checksums.txt` to GitHub Releases.
  2. **Given** duplicate or re-triggered workflow dispatches for the same `vX.Y.Z` tag, **When** `.github/workflows/release.yml` executes, **Then** the `concurrency` group cancels superseded in-progress runs and the release step idempotently updates existing releases (`gh release view` / `gh release edit` + `gh release upload --clobber`) instead of failing with an "already exists" error.

---

### User Story 5 - Automated Dependency & Toolchain Maintenance via Renovate (Priority: P3)

As a maintainer, I want a declarative Renovate configuration (`.github/renovate.json`) covering `mise.toml`, `go.mod`, `Dockerfile`, `docker-compose.yml`, and `.github/workflows/*.yml`, so that Go toolchain versions, container base images, and CI actions stay synchronized without noisy single-file PR spam.

- **Independent Test**: Validate `.github/renovate.json` with `jq empty .github/renovate.json` and verify manager and `packageRules` grouping definitions for Go toolchain, container images, and CI/developer tooling.
- **Acceptance Scenarios**:
  1. **Given** a new Go patch/minor release, **When** Renovate evaluates the repository, **Then** updates across `mise.toml`, `go.mod`, `Dockerfile`, and GitHub Actions are grouped into a single `Go toolchain` pull request with `gomodTidy` enabled.
  2. **Given** updates to container images (`Dockerfile`, `docker-compose.yml`) or CI actions (`.github/workflows/`), **When** Renovate runs, **Then** updates are grouped logically (`Container images`, `CI & developer tooling`) to minimize PR noise.

---

## 4. Requirements

### Functional Requirements
- **FR-001**: `mise.toml` MUST pin tool versions (`go = "1.25.8"`, `act = "latest"`, `watchexec = "latest"`), free of vendor-specific CI code.
- **FR-002**: `.mise/tasks/check.toml` MUST implement `check-all` running exclusively `check:fmt`, `check:vet`, and `check:test`.
- **FR-003**: `.mise/tasks/build.toml` MUST provide dedicated cross-compilation targets (`build:local`, `build:linux-x64`, `build:linux-arm64`, `build:darwin-x64`, `build:darwin-arm64`, `build:windows-x64`), `build:checksums` (`bin/checksums.txt`), and `build-all`, injecting `-X main.version=${VERSION:-dev}` across all binary targets.
- **FR-004**: `.mise/tasks/bin.toml` MUST preserve all runtime lifecycle tasks (`bin:run`, `bin:reload`, `bin:status`).
- **FR-005**: `.mise/tasks/dev.toml` MUST provide developer ergonomics tasks: `dev:fmt` (auto-format) and `dev:watch` (event-driven test watcher using `watchexec`).
- **FR-006**: `.mise/tasks/docker.toml` MUST define container tasks (`docker:build-debian`, `docker:build-alpine`, `docker:build-distroless`, `docker:build`, `docker:run`, `docker:exec`, `compose:up`, `compose:down`, `compose:ps`, `compose:reload`, `compose:logs`, `compose:exec`).
- **FR-007**: A dedicated driving adapter `.act/ci.sh` MUST handle local `act` execution with Docker pre-flight verification.
- **FR-008**: All tasks MUST have exactly ONE canonical name; no alias tasks are permitted.
- **FR-009**: `.github/workflows/ci.yml` MUST be a driving adapter utilizing `jdx/mise-action@v2` and delegating to `mise run check:*` and `mise run docker:build-*`, with concurrency deduplication per branch/ref.
- **FR-010**: CI MUST run on Go version `1.25.x` on `ubuntu-latest`.
- **FR-011**: `.actrc` MUST configure `act` with multi-arch runner mappings for Apple Silicon compatibility.
- **FR-012**: `.github/workflows/release.yml` MUST trigger on `v*` tags, enforce `concurrency` deduplication per workflow/ref, run `mise run check-all` and `VERSION=${{ github.ref_name }} mise run build-all`, extract release notes from `CHANGELOG.md` (with annotated tag fallback), and idempotently create or update the GitHub Release with all 5 binaries and `checksums.txt`.
- **FR-013**: `.agents/skills/release/SKILL.md` MUST define the guided release preparation workflow (commit analysis, SemVer recommendation, `CHANGELOG.md` drafting in Keep-a-Changelog format, `mise run check-all` + `mise run build-all` verification, and annotated Git tag creation after user approval).
- **FR-014**: `.github/renovate.json` MUST configure Renovate with `gomod`, `mise`, `dockerfile`, `docker-compose`, and `github-actions` managers and grouped `packageRules` (`Go toolchain`, `Container images`, `CI & developer tooling`).

### Non-Functional Requirements
- **NFR-001 (Constitution Compliance)**: Zero runtime Go dependencies added (Go standard library + `gopkg.in/yaml.v3` only).
- **NFR-002 (Hermetic Execution)**: All tests MUST execute hermetically without external network access.
- **NFR-003 (Strict Race Detection)**: All tests MUST execute with `-race` enabled.
- **NFR-004 (Resource Efficiency)**: File watching MUST be event-driven via `watchexec`, consuming 0.0% CPU when idle.
- **NFR-005 (Visual Ergonomics)**: Task and artifact naming MUST use `x64` instead of `amd64` to avoid visual confusion with `arm64`.
- **NFR-006 (Zero-Artifact Quality Gate)**: `check-all` MUST NOT generate build artifacts in the workspace.
- **NFR-007 (Static Linking Safety)**: All Linux binaries MUST be compiled with `CGO_ENABLED=0` to guarantee libc/musl compatibility.
- **NFR-008 (Prefix Discipline)**: All project environment variables MUST adhere to the conflict-free `GITHUB_API_GATEWAY_` prefix.
