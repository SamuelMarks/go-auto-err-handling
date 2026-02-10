#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

cleanup() {
  if [[ -n "${tmp_dir:-}" && -d "$tmp_dir" ]]; then
    rm -rf "$tmp_dir"
  fi
}
trap cleanup EXIT

tmp_dir="$(mktemp -d)"
cover_profile="$tmp_dir/coverage.out"

printf "Running tests for coverage...\n"
go test ./... -coverprofile "$cover_profile" >/dev/null

test_pct="$(go tool cover -func "$cover_profile" | awk '/^total:/{gsub(/%/, "", $3); print $3}')"
if [[ -z "$test_pct" ]]; then
  echo "Failed to parse test coverage percent" >&2
  exit 1
fi

doc_pct="$(go run ./cmd/doccov -format percent)"

color_for() {
  awk -v pct="$1" 'BEGIN {
    if (pct >= 100) print "brightgreen";
    else if (pct >= 90) print "green";
    else if (pct >= 80) print "yellowgreen";
    else if (pct >= 70) print "yellow";
    else print "red";
  }'
}

test_color="$(color_for "$test_pct")"
doc_color="$(color_for "$doc_pct")"

badge_link="https://github.com/SamuelMarks/go-auto-err-handling/actions/workflows/ci.yml"

ensure_badge_line() {
  local label="$1"
  local slug="$2"
  local url="$3"
  local line="[![${label}](${url})](${badge_link})"

  if ! grep -q "img.shields.io/badge/${slug}-" README.md; then
    if grep -q "\[!\[go test\]" README.md; then
      awk -v line="$line" '
        { print }
        !inserted && /\[!\[go test\]/ { print line; inserted=1 }
      ' README.md > README.md.tmp && mv README.md.tmp README.md
    else
      awk -v line="$line" '
        { print }
        !inserted && NF == 0 { print line; inserted=1 }
        END { if (!inserted) print line }
      ' README.md > README.md.tmp && mv README.md.tmp README.md
    fi
  fi
}

update_badge_url() {
  local slug="$1"
  local value="$2"
  local color="$3"
  local encoded_value="${value//%/%25}"
  local full="${slug}-${encoded_value}-${color}"

  perl -0pi -e "s|img\.shields\.io/badge/${slug}-[^)]+|img.shields.io/badge/${full}|g" README.md
}

test_value="${test_pct}%"
doc_value="${doc_pct}%"

test_url="https://img.shields.io/badge/test%20coverage-${test_value//%/%25}-${test_color}"
doc_url="https://img.shields.io/badge/doc%20coverage-${doc_value//%/%25}-${doc_color}"

# Insert doc badge first so test badge ends up above it.
ensure_badge_line "Doc Coverage" "doc%20coverage" "$doc_url"
ensure_badge_line "Test Coverage" "test%20coverage" "$test_url"

update_badge_url "test%20coverage" "$test_value" "$test_color"
update_badge_url "doc%20coverage" "$doc_value" "$doc_color"

printf "Test coverage: %s%%\n" "$test_pct"
printf "Doc coverage: %s%%\n" "$doc_pct"
