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
