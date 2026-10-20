# Implementation Tasks: Automated CI with Ports & Adapters (Mise, GitHub Actions, and Act)

**Feature**: Automated CI with GitHub Actions, mise, and act  
**Branch**: `002-github-actions-ci` | **Spec**: [specs/002-github-actions-ci/spec.md](spec.md) | **Plan**: [specs/002-github-actions-ci/plan.md](plan.md)  
**Status**: Ready for Implementation

---

## Phase 1: Core Port & Local Adapter Configuration

**Purpose**: Establish the single source of truth for tools in `mise.toml` and modular domain task files in `.mise/tasks/*.toml` with zero vendor-specific code, plus `.act/ci.sh` and `.actrc`.

- [x] **T001**: Configure root `mise.toml` in project root pinning tools (`go = "1.25.8"`, `act = "latest"`, `watchexec = "latest"`).
- [x] **T002**: Create `.mise/tasks/check.toml` defining pure verification tasks: `check-all` (depends on `check:fmt`, `check:vet`, `check:test`), `check:fmt`, `check:vet`, and `check:test`.
- [x] **T003**: Create `.mise/tasks/build.toml` defining compilation tasks: `build:local`, `build:linux-x64`, `build:linux-arm64`, `build:darwin-x64`, `build:darwin-arm64`, `build:windows-x64`, and `build-all`.
- [x] **T004**: Create `.mise/tasks/bin.toml` defining operational and runtime lifecycle tasks: `bin:run`, `bin:reload`, `bin:status`.
- [x] **T005**: Create `.mise/tasks/dev.toml` defining developer ergonomics tasks: `dev:fmt` (auto-format) and `dev:watch` (event-driven watchexec test runner).
- [x] **T006**: Create `.mise/tasks/docker.toml` defining container tasks: `docker:build-debian`, `docker:build-alpine`, `docker:build-distroless`, `docker:build`, `docker:run`, `docker:exec`, `compose:up`, `compose:down`, `compose:logs`, `compose:exec`.
- [x] **T007**: Create local driving adapter script `.act/ci.sh` with Docker pre-flight checks and executable permissions.
- [x] **T008**: Create `.actrc` with default runner flags and image configuration optimized for local macOS Apple Silicon Docker execution.

---

## Phase 2: Multi-Target Container & Remote CI Driving Adapter

**Purpose**: Update `Dockerfile` to produce Debian (default), Alpine, and Distroless targets, and create the GitHub Actions workflow adapter delegating to `mise`.

- [x] **T009**: Update `Dockerfile` with multi-target stages: shared Go static builder, pure minimal `alpine` stage, SOTA hardened `distroless` stage, and `debian` slim default stage with `bash` and `curl`.
- [x] **T010**: Update `docker-compose.yml` to use `GITHUB_API_GATEWAY_CONTAINER_TARGET` and `compose:exec` compatibility.
- [x] **T011**: [US1] Create `.github/workflows/ci.yml` triggering on `push` and `pull_request` to `main`.
- [x] **T012**: [US1] Configure Go version (`1.25.x`) on `ubuntu-latest` with `actions/setup-go@v5` and `jdx/mise-action@v2`.
- [x] **T013**: [US1] Add granular verification steps in `.github/workflows/ci.yml`: `mise run check:fmt`, `mise run check:vet`, `mise run check:test`.
- [x] **T014**: [US3] Add repository hygiene check in `.github/workflows/ci.yml` ensuring `.env` is uncommitted and git-ignored.
- [x] **T015**: [US1] Add container build verification job (`container`) in `.github/workflows/ci.yml` invoking `mise run docker:build-debian`, `mise run docker:build-alpine`, and `mise run docker:build-distroless`.

---

## Phase 3: Local Verification & Task Execution

**Purpose**: Verify native tasks, quality gates, and full cross-compilation on the host environment.

- [x] **T016**: Execute `mise run check-all` and verify all dependent tasks (`check:fmt`, `check:vet`, `check:test`) pass cleanly with exit code 0 and leave no artifacts on disk.
- [x] **T017**: Execute `mise run build:local` and `mise run build-all`, verifying all 5 cross-compilation binaries (`linux-x64`, `linux-arm64`, `darwin-x64`, `darwin-arm64`, `windows-x64.exe`) are produced in `bin/`.
- [x] **T018**: Test developer helpers: verify `mise run dev:fmt` formats files cleanly and `mise run dev:watch` operates with `watchexec`.
- [x] **T019**: Test container builds: verify `mise run docker:build-debian`, `mise run docker:build-alpine`, and `mise run docker:build-distroless` build cleanly.

---

## Phase 4: Local Container CI Simulation (`act`)

**Purpose**: Validate that the GitHub Actions workflow executes identically inside local Docker containers via `.act/ci.sh`.

- [x] **T020**: [US2] Execute `./.act/ci.sh -n` to validate workflow syntax and event matching without running containers.
- [x] **T021**: [US2] Execute `./.act/ci.sh test` against local Docker daemon and verify that the containerized pipeline succeeds.
- [x] **T022**: [US2] Execute `./.act/ci.sh container` against local Docker daemon and verify that the container build job succeeds.

---

## Phase 5: Documentation & Polish

**Purpose**: Update documentation and finalize the branch.

- [x] **T023**: Update `README.md` documenting task usage (`mise run check-all`, `mise run build-all`, `mise run dev:watch`, `mise run dev:fmt`, `./.act/ci.sh`, `mise run bin:run`, etc.).
- [x] **T024**: Clean up temporary build artifacts (`bin/`) and review git status.
- [x] **T025**: Add `compose:ps` task to `.mise/tasks/docker.toml` running `docker compose ps` and document in `README.md`.
- [x] **T026**: Implement cross-platform hot-reload: add `compose:reload` in `.mise/tasks/docker.toml`, update `bin:run` and `bin:reload` in `.mise/tasks/bin.toml` with `pkill -HUP` and Windows error check, add loopback guard to `POST /_gateway/reload` in `cmd/gateway/app.go`, add unit tests in `cmd/gateway/main_test.go`, and document in `README.md`.
- [x] **T027**: Migrate default gateway port from `8080` to collision-free `28280` across `internal/config/config.go`, `Dockerfile`, `docker-compose.yml`, `.mise/tasks/docker.toml`, `.mise/tasks/bin.toml`, tests, `.env.example`, and `README.md`.

---

## Phase 6: Convergence

**Purpose**: Close remaining gaps identified by convergence analysis against `spec.md` and `plan.md`.

- [x] **T028**: [US3] [US4] Document `VERSION` ldflags injection, `build:checksums` (`bin/checksums.txt`) in `.mise/tasks/build.toml`, `.agents/skills/release/SKILL.md`, and `CHANGELOG.md` release workflow in `README.md`.
- [x] **T029**: [US1] [US4] Harden `.github/workflows/release.yml` and `.github/workflows/ci.yml` with `concurrency` deduplication (`${{ github.workflow }}-${{ github.ref }}`) and make the `Publish GitHub Release` step in `.github/workflows/release.yml` idempotent (`gh release view` / `gh release edit` + `gh release upload --clobber`) to eliminate duplicate-dispatch race failures.
- [x] **T030**: [US2] [US4] Validate updated workflows and quality gates via `./.act/ci.sh -n` and `mise run check-all`.
