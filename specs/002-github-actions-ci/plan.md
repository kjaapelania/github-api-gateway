# Implementation Plan: Automated CI with Ports & Adapters (Mise, GitHub Actions, and Act)

**Branch**: `002-github-actions-ci` | **Date**: 2026-10-02 | **Spec**: [specs/002-github-actions-ci/spec.md](spec.md)  
**Input**: "Automate Continuous Integration with GitHub Actions, mise, and act using Ports and Adapters architecture"

---

## 1. Summary

Implement a hermetic, vendor-neutral continuous integration pipeline following the **Ports and Adapters (Hexagonal)** architecture:
1. **Core Port (`mise.toml` & `.mise/tasks/*.toml`)**:
   - `mise.toml` pins tools (`go`, `act`, `watchexec`).
   - `.mise/tasks/check.toml` defines the pure quality gate (`check-all`, `check:fmt`, `check:vet`, `check:test`).
   - `.mise/tasks/build.toml` defines static cross-compilation with readable `x64` naming (`build-all`, `build:local`, `build:linux-x64`, etc.).
   - `.mise/tasks/bin.toml` defines application runtime operations (`bin:run`, `bin:reload`, `bin:status`).
   - `.mise/tasks/dev.toml` defines developer helpers (`dev:fmt`, `dev:watch`).
   - `.mise/tasks/docker.toml` defines container & compose operations (`docker:build-debian`, `docker:build-alpine`, `docker:build-distroless`, `docker:build`, `docker:exec`, `compose:*`).
2. **Driving Adapters**:
   - **Remote CI (`.github/workflows/ci.yml`)**: Multi-job workflow invoking granular `mise run check:*` tasks and `mise run docker:build-*` tasks.
   - **Local Container Runner (`.act/ci.sh`)**: Standalone shell script with pre-flight Docker validation executing `act` locally, perfectly mirroring `ci.yml`.
   - **Local Shell**: Allows developers to execute `mise run check-all`, individual checks, or release builds natively.

---

## 2. Technical Context

- **Language / Runtime**: Go 1.25.x
- **Toolchain Manager**: `mise` (v2026.6.5) with modular `.mise/tasks/*.toml` discovery
- **CI Runner / Emulator**: `act` (v0.2.89) via Docker (Engine 28.0.4 on macOS Apple Silicon)
- **Watcher Utility**: `watchexec` (v2.7.3) for 0% CPU kernel event-driven testing
- **CI Action**: `jdx/mise-action@v2`
- **Dependencies**: Go Standard Library + `gopkg.in/yaml.v3` only
- **Constraints**:
  - No alias tasks in any TOML file (one canonical name per task).
  - No vendor-specific CI logic or `[env]` block in `mise.toml`.
  - Architecture naming follows `x64` for user ergonomics while passing `GOARCH=amd64` to the Go compiler.
  - Zero live credentials required for testing; mock server hermeticity.
  - Environment variables strictly use the `GITHUB_API_GATEWAY_` prefix.

---

## 3. Constitution Check

| Principle | Requirement | Compliance Status |
| :--- | :--- | :--- |
| **I. Go Standard Library First** | Zero added Go runtime dependencies | **PASS** — Tooling and workflow changes only |
| **II. Reverse Proxy Quota Protection** | Core proxy behavior preserved | **PASS** — Application logic untouched |
| **III. Strict TDD** | Mandatory test execution with race detector | **PASS** — `go test -race` is enforced via `check:test` |
| **IV. Observability & Graceful Failure** | Never expose secrets in logs | **PASS** — CI runs in hermetic mock mode |
| **V. Fail-Closed Governance** | Ensure security boundaries intact | **PASS** — CI validates all rate-limiting and auth checks |

---

## 4. Core Port Definitions

### Root `mise.toml` (Tools Only)
```toml
[tools]
go = "1.25.8"
act = "latest"
watchexec = "latest"
```

### `.mise/tasks/check.toml` (Verification Gate)
```toml
["check:fmt"]
description = "Verify code formatting without modifying files"
run = 'test -z "$(gofmt -s -l .)" || (echo "Unformatted files:" && gofmt -s -l . && exit 1)'

["check:vet"]
description = "Run static analysis"
run = "go vet ./..."

["check:test"]
description = "Run tests with race detector"
run = "go test -race -v ./..."

[check-all]
description = "Run all verification checks (fmt, vet, test)"
depends = ["check:fmt", "check:vet", "check:test"]
```

### `.mise/tasks/build.toml` (Compilation & Cross-Compilation)
```toml
["build:local"]
description = "Compile gateway binary for current host architecture"
run = "go build -v -o bin/gateway ./cmd/gateway"

["build:linux-x64"]
description = "Cross-compile static binary for Linux 64-bit Intel/AMD"
run = "CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -v -trimpath -ldflags='-s -w' -o bin/gateway-linux-x64 ./cmd/gateway"

["build:linux-arm64"]
description = "Cross-compile static binary for Linux ARM64"
run = "CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -v -trimpath -ldflags='-s -w' -o bin/gateway-linux-arm64 ./cmd/gateway"

["build:darwin-x64"]
description = "Cross-compile static binary for macOS Intel 64-bit"
run = "CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -v -trimpath -ldflags='-s -w' -o bin/gateway-darwin-x64 ./cmd/gateway"

["build:darwin-arm64"]
description = "Compile static binary for macOS Apple Silicon ARM64"
run = "CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -v -trimpath -ldflags='-s -w' -o bin/gateway-darwin-arm64 ./cmd/gateway"

["build:windows-x64"]
description = "Cross-compile static binary for Windows 64-bit"
run = "CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -v -trimpath -ldflags='-s -w' -o bin/gateway-windows-x64.exe ./cmd/gateway"

[build-all]
description = "Compile release binaries for all 5 target platforms"
depends = [
  "build:linux-x64",
  "build:linux-arm64",
  "build:darwin-x64",
  "build:darwin-arm64",
  "build:windows-x64"
]
```

### `.mise/tasks/bin.toml` (Runtime & Operational Lifecycle)
```toml
["bin:run"]
description = "Run gateway binary locally"
run = "go run ./cmd/gateway"

["bin:reload"]
description = "Hot-reload gateway policy and keys without restart"
run = "curl -i -X POST http://localhost:${GITHUB_API_GATEWAY_PORT:-8080}/_gateway/reload"

["bin:status"]
description = "Query gateway status and telemetry"
run = "curl -s http://localhost:${GITHUB_API_GATEWAY_PORT:-28280}/_gateway/status | jq ."
```

### `.mise/tasks/dev.toml` (Developer Ergonomics)
```toml
["dev:fmt"]
description = "Auto-format all Go source files"
run = "gofmt -s -w ."

["dev:watch"]
description = "Event-driven test watcher via watchexec (0% idle CPU)"
run = "watchexec -e go --clear -- go test -v ./..."
```

### `.mise/tasks/docker.toml` (Containers & Compose)
```toml
["docker:build-debian"]
description = "Build Debian-slim container image (default, with bash & curl)"
run = "docker build --target debian -t github-api-gateway:debian -t github-api-gateway:debian-latest -t github-api-gateway:latest ."

["docker:build-alpine"]
description = "Build minimal Alpine container image (<10MB)"
run = "docker build --target alpine -t github-api-gateway:alpine -t github-api-gateway:alpine-latest ."

["docker:build-distroless"]
description = "Build SOTA hardened zero-trust Distroless container image (<2MB base, 0 CVEs)"
run = "docker build --target distroless -t github-api-gateway:distroless -t github-api-gateway:distroless-latest ."

["docker:build"]
description = "Build all container images (Debian, Alpine, Distroless)"
depends = ["docker:build-debian", "docker:build-alpine", "docker:build-distroless"]

["docker:run"]
description = "Run default gateway container (Debian)"
run = "docker run --rm -p 28280:28280 -v $(pwd)/policy.yaml:/app/policy.yaml:ro --env-file .env github-api-gateway:debian"

["docker:exec"]
description = "Open an interactive bash shell in the running gateway container"
run = "docker exec -it github-api-gateway bash"

["compose:up"]
description = "Start gateway service via Docker Compose"
run = "docker compose up -d --build"

["compose:down"]
description = "Stop Docker Compose services"
run = "docker compose down"

["compose:logs"]
description = "Follow Docker Compose logs"
run = "docker compose logs -f"

["compose:exec"]
description = "Open an interactive bash shell in the Compose gateway service"
run = "docker compose exec gateway bash"

["compose:ps"]
description = "List running Docker Compose services and status"
run = "docker compose ps"
```

---

## 5. Driving Adapter: Remote CI (`.github/workflows/ci.yml`)

```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  test:
    name: Code Verification Gate
    runs-on: ubuntu-latest
    strategy:
      matrix:
        go-version: ['1.25.x']
    steps:
      - name: Checkout Code
        uses: actions/checkout@v4

      - name: Install mise
        uses: jdx/mise-action@v2
        with:
          version: 2026.6.5

      - name: Setup Go ${{ matrix.go-version }}
        uses: actions/setup-go@v5
        with:
          go-version: ${{ matrix.go-version }}
          cache: true

      - name: Verify Code Formatting
        run: mise run check:fmt

      - name: Static Analysis (go vet)
        run: mise run check:vet

      - name: Run Tests with Race Detector
        run: mise run check:test

      - name: Hygiene Check (.env not tracked)
        run: |
          if git ls-files --error-unmatch .env 2>/dev/null; then
            echo "ERROR: .env file is tracked in git!"
            exit 1
          fi

  container:
    name: Container Build Verification
    needs: [test]
    runs-on: ubuntu-latest
    steps:
      - name: Checkout Code
        uses: actions/checkout@v4

      - name: Install mise
        uses: jdx/mise-action@v2
        with:
          version: 2026.6.5

      - name: Build Debian Container Image
        run: mise run docker:build-debian

      - name: Build Alpine Container Image
        run: mise run docker:build-alpine

      - name: Build Distroless Container Image
        run: mise run docker:build-distroless
```

---

## 6. Driving Adapter: Local Simulation (`.act/ci.sh`)

```bash
#!/usr/bin/env bash
set -euo pipefail

# Pre-flight check: Is Docker daemon running?
if ! docker info >/dev/null 2>&1; then
  echo "ERROR: Docker daemon is not running."
  echo "Please start Docker Desktop or Colima before running act."
  exit 1
fi

# Locate act executable (fallback to mise exec if not in global PATH)
ACT_RUNNER=("act")
if ! command -v act >/dev/null 2>&1; then
  if command -v mise >/dev/null 2>&1; then
    ACT_RUNNER=("mise" "exec" "--" "act")
  else
    echo "ERROR: act is not found on PATH and mise is not installed."
    exit 1
  fi
fi

# Parse job name and extra flags
JOB="test"
if [[ $# -gt 0 && ! "$1" =~ ^- ]]; then
  JOB="$1"
  shift
fi

echo "==> Running local CI simulation via act (job: ${JOB})..."
exec "${ACT_RUNNER[@]}" -j "${JOB}" "$@"
```

---

## 7. Implementation Phases

- **Phase 1**: Configure Core Port (`mise.toml`, `.mise/tasks/*.toml`), Local Adapter (`.act/ci.sh`), and Runner Config (`.actrc`).
- **Phase 2**: Create Remote CI Driving Adapter (`.github/workflows/ci.yml`) and update `Dockerfile` for multi-stage Debian, Alpine, and Distroless targets.
- **Phase 3**: Verify Quality Gate and Cross-compilation locally (`mise run check-all`, `mise run build-all`, `dev:*` tasks, container build tasks).
- **Phase 4**: Execute containerized verification via `./.act/ci.sh`.
- **Phase 5**: Documentation & final review.
