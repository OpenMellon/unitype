#!/usr/bin/env bash
# Pre-commit hook: reject staged files larger than MAX_TOKENS (tokens ~= bytes / 4).
#
# Skipped: lockfiles/checksums, generated code, binaries and vendored/test fixture data.
# A modified file that is already over the limit is allowed as long as it does not grow,
# so that splitting or trimming an existing oversized file is never blocked.
set -euo pipefail

MAX_TOKENS="${MAX_TOKENS:-5000}"
MAX_BYTES=$((MAX_TOKENS * 4))

skip_path() {
  case "$1" in
    go.sum | go.work.sum | *.lock | *.lockb | *-lock.json | *-lock.yaml | *.lock.json) return 0 ;;
    package-lock.json | pnpm-lock.yaml | yarn.lock | Cargo.lock | poetry.lock | uv.lock | Gemfile.lock) return 0 ;;
    *_gen.go | *.gen.go | *.pb.go | *_generated.go | *.generated.* | *.min.js | *.min.css | *.map) return 0 ;;
    vendor/* | */vendor/* | node_modules/* | */node_modules/* | testdata/* | */testdata/*) return 0 ;;
    *.svg | *.pdf | *.png | *.jpg | *.jpeg | *.gif | *.webp | *.ico | *.ttf | *.otf | *.woff | *.woff2) return 0 ;;
    *.zip | *.gz | *.tar | *.bin | *.icc | *.sbom | *.bundle) return 0 ;;
  esac
  return 1
}

is_generated() {
  # Standard Go marker plus the common "DO NOT EDIT" / "@generated" headers.
  git show ":$1" | head -n 20 | grep -qE '^(//|#|/\*|\*) ?(Code generated .* DO NOT EDIT\.?|@generated|DO NOT EDIT)'
}

is_binary() {
  [ "$(git show ":$1" | head -c 8000 | LC_ALL=C tr -d '\000' | wc -c)" -ne "$(git show ":$1" | head -c 8000 | wc -c)" ]
}

failures=()
for f in "$@"; do
  skip_path "$f" && continue
  git cat-file -e ":$f" 2>/dev/null || continue # deleted or not staged
  size=$(git cat-file -s ":$f")
  [ "$size" -le "$MAX_BYTES" ] && continue
  is_binary "$f" && continue
  is_generated "$f" && continue
  if old=$(git cat-file -s "HEAD:$f" 2>/dev/null) && [ "$size" -le "$old" ]; then
    continue
  fi
  failures+=("$f|$((size / 4))")
done

[ "${#failures[@]}" -eq 0 ] && exit 0

{
  echo "ERROR: staged file(s) exceed ${MAX_TOKENS} tokens (bytes / 4):"
  for entry in "${failures[@]}"; do
    echo "  ${entry%%|*}  (~${entry##*|} tokens)"
  done
  cat <<MSG

Large files overflow an agent's context and make edits slow and error-prone. Split each one:

  1. Read the file and list its distinct responsibilities (types, constructors, one feature or
     one group of related functions each). Aim for files of 1,000-3,000 tokens (4-12 KB).
  2. Create a new file per responsibility, next to the original and named for it
     (e.g. foo.go -> foo_parse.go, foo_write.go, foo_options.go; foo.ts -> foo/parse.ts, foo/index.ts).
     Prefix sibling files with the original stem so they sort together.
  3. MOVE code verbatim - cut and paste, never retype or rewrite - and change nothing else in the
     same commit, so the diff is a pure move.
  4. Go: keep the same 'package' clause so no imports or call sites change. Put tests in the
     matching *_test.go file. Run 'gofmt -w' / goimports on every new file.
     Other languages: re-export from the original module (index file, __init__.py, mod.rs) so
     existing imports keep working.
  5. Keep the original file for whatever remains (typically the core type and its constructor).
  6. Verify nothing was lost or changed: build, run the tests, then 're-stage' ('git add') the
     original and every new file and commit again.

Only ever split code you wrote or maintain by hand. Generated files should carry a
'Code generated ... DO NOT EDIT.' header (or be excluded in .pre-commit-config.yaml) instead.
MSG
} >&2
exit 1
