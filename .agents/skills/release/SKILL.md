---
name: "release"
description: "Prepare a semantic version bump, generate human-readable Keep-a-Changelog release notes in CHANGELOG.md, verify quality gates via mise, and create an annotated Git release tag after user approval. Use only when the user explicitly asks to prepare a release, bump the version, or generate a changelog."
---

# Release & Changelog Skill

Synthesizes git commit history and diffs since the last release tag into a human-readable `CHANGELOG.md` entry, calculates the Semantic Version (`MAJOR.MINOR.PATCH`) bump, runs the `mise` verification gate, and tags the release only after explicit user confirmation.

## Guardrails

- **Never trigger on normal `git push`**: Only activate when the user explicitly asks to cut a release, bump a version, or draft a changelog.
- **Strict tag immutability**: Never delete, overwrite, force-push, or move an already-published Git tag or GitHub Release (`git tag -f`, `git push --force`, or `git push origin :refs/tags/v*`). Once a `vX.Y.Z` tag is pushed, any subsequent commits MUST be released under a new incremented Semantic Version (`PATCH`, `MINOR`, or `MAJOR`).
- **Derive version from Git tags + `VERSION` file**: Keep `VERSION`, `CHANGELOG.md`, and the annotated Git tag (`vX.Y.Z`) in lockstep.
- **No mechanical commit dumps**: Group changes by user-visible impact (`Added`, `Changed`, `Fixed`, `Security`, `Breaking Changes`), lead with a bold outcome summary, and omit internal noise (`test:`, formatting, spec chores).
- **Always pause for user review**: Never create a release commit, tag, or push without presenting the draft changelog and version bump to the user first.

## Workflow

### Step 1: Inspect Repository & Previous Tag
1. Check existing tags and current `VERSION` file (if present):
   ```bash
   LAST_TAG=$(git tag --list 'v*' --sort=-v:refname | head -n 1)
   ```
2. Collect commits and diff statistics since `${LAST_TAG}` (or from initial commit if no tag exists):
   ```bash
   if [ -n "$LAST_TAG" ]; then
     git log "${LAST_TAG}..HEAD" --oneline
     git diff "${LAST_TAG}..HEAD" --stat
   else
     git log --oneline
   fi
   ```

### Step 2: Calculate Semantic Version Bump
Determine the next version (`vMAJOR.MINOR.PATCH`) using these rules (unless overridden by user flags `--major`, `--minor`, `--patch`, or an explicit version like `v0.1.0`):
1. **No prior tag**: Default to `v0.1.0` (initial feature release) or `v1.0.0` if requested.
2. **Breaking changes** (`feat!:`, `BREAKING CHANGE:`, incompatible config/API removal): Increment `MAJOR` (`X+1.0.0`, or `0.Y+1.0` while in `0.x`).
3. **New features** (`feat:`): Increment `MINOR` (`X.Y+1.0`).
4. **Bug fixes / security patches only** (`fix:`, `perf:`): Increment `PATCH` (`X.Y.Z+1`).

### Step 3: Draft Human-Centric Changelog Entry
Draft the new section for `CHANGELOG.md` following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/):

```markdown
## [X.Y.Z] - YYYY-MM-DD

### Security
- **Bold outcome lead.** Concise explanation of the threat prevented or vulnerability fixed and the affected component.

### Added
- **Bold capability lead.** User-facing description of the new capability, flags, endpoints, or configuration options.

### Changed
- **Bold change lead.** Behavioral or operational improvements and any default value changes.

### Fixed
- **Bold fix lead.** Root cause and user-visible fix.
```

Rules for drafting entries:
- Omit empty sections.
- If breaking changes exist, place `### Breaking Changes` at the very top with explicit migration steps.
- Consolidate multiple related commits (e.g., initial implementation + follow-up fixes in the same release window) into a single coherent bullet.
- Update the comparison links at the bottom of `CHANGELOG.md` (`[X.Y.Z]: https://github.com/<owner>/<repo>/compare/vPrev...vX.Y.Z`).

### Step 4: Present Dry-Run Preview & Ask for Confirmation
Before writing or committing any release tag:
1. Write or update `CHANGELOG.md` (and `VERSION` if used) in the working tree so the user can inspect the exact diff.
2. Present the proposed version (`vPrev -> vX.Y.Z`) and rendered release notes.
3. Ask the user to confirm:
   - Whether to approve the version and changelog text (or edit first).
   - Whether to create the release commit (`chore(release): vX.Y.Z`) and annotated tag (`git tag -a vX.Y.Z`).
   - Whether to push the tag to `origin` to trigger the GitHub Actions release pipeline.

### Step 5: Verify, Commit, Tag & Push (After Approval Only)
Once the user approves:
1. Run the project quality gate:
   ```bash
   mise run check-all
   ```
2. Extract the current version's markdown section from `CHANGELOG.md` into a temporary file (`$TMPDIR/release-notes-vX.Y.Z.md`).
3. Stage and commit:
   ```bash
   git add CHANGELOG.md
   git commit -m "chore(release): vX.Y.Z"
   ```
4. Create an annotated tag using the extracted release notes file:
   ```bash
   git tag -a "vX.Y.Z" -F "$TMPDIR/release-notes-vX.Y.Z.md"
   ```
5. If the user confirmed pushing to remote, push `main` and the tag (`git push origin main && git push origin vX.Y.Z`).
