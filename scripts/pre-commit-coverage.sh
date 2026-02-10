#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

"${root_dir}/scripts/update-coverage.sh"

git add "${root_dir}/README.md"
