#!/usr/bin/env bash
#
# scripts/proof.sh — deterministic, independently inspectable proof runner
# for the canonical (schema-3, single-runtime) termyard build. See
# docs/release-notes/hard-rewrite.md for the release this proof gate
# evidences.
#
# Runs, in order, and logs start/end time + exit status for each:
#   cd web && npm ci
#   cd web && npm run typecheck
#   cd web && npm run test:ci
#   cd web && npm run build          (produces pkg/server/dist)
#   go build ./...
#   go test ./... -count=1
#   go vet ./...
#   git grep residue check (legacy/v2 vocabulary must not reappear)
#   go test -race <targeted packages> -count=<repeat-race>
#   go test -race ./... -count=<repeat-race>
#   cd web && npm run test:e2e       (only when --e2e 1)
#
# Exits nonzero on the first failed mandatory gate. All logs for steps that
# completed before the failure are retained under --output.
#
# Usage:
#   scripts/proof.sh --output <dir> --e2e 0|1 --repeat-race <count>
#
# All flags are optional:
#   --output       default: .proof/<sha>
#   --e2e          default: 0
#   --repeat-race  default: 1   (used as -count=N for all `go test -race` runs)

set -uo pipefail

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

OUTPUT_DIR=""
E2E=0
REPEAT_RACE=1

usage() {
  cat >&2 <<'EOF'
Usage: scripts/proof.sh [--output <dir>] [--e2e 0|1] [--repeat-race <count>]
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --output)
      OUTPUT_DIR="${2:-}"
      shift 2
      ;;
    --e2e)
      E2E="${2:-}"
      shift 2
      ;;
    --repeat-race)
      REPEAT_RACE="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage
      exit 2
      ;;
  esac
done

case "$E2E" in
  0|1) ;;
  *)
    echo "invalid --e2e '$E2E' (must be 0 or 1)" >&2
    exit 2
    ;;
esac

case "$REPEAT_RACE" in
  ''|*[!0-9]*)
    echo "invalid --repeat-race '$REPEAT_RACE' (must be a positive integer)" >&2
    exit 2
    ;;
esac
if [ "$REPEAT_RACE" -lt 1 ]; then
  echo "invalid --repeat-race '$REPEAT_RACE' (must be >= 1)" >&2
  exit 2
fi

# ---------------------------------------------------------------------------
# Repo root, SHA, output directory
# ---------------------------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

SHA="$(git rev-parse HEAD 2>/dev/null || echo "unknown")"

if [ -z "$OUTPUT_DIR" ]; then
  OUTPUT_DIR=".proof/${SHA}"
fi

# Resolve to absolute path so logs are unambiguous regardless of caller cwd.
mkdir -p "$OUTPUT_DIR"
OUTPUT_DIR="$(cd "$OUTPUT_DIR" && pwd)"
LOG_DIR="$OUTPUT_DIR/logs"
mkdir -p "$LOG_DIR"

SUMMARY_LOG="$OUTPUT_DIR/summary.log"
METADATA_FILE="$OUTPUT_DIR/metadata.txt"
: > "$SUMMARY_LOG"

RUN_STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# ---------------------------------------------------------------------------
# Environment/SHA metadata — written before any test runs
# ---------------------------------------------------------------------------

{
  echo "proof.sh evidence metadata"
  echo "==========================="
  echo "e2e:             $E2E"
  echo "repeat-race:     $REPEAT_RACE"
  echo "output_dir:      $OUTPUT_DIR"
  echo "run_started_at:  $RUN_STARTED_AT"
  echo
  echo "git_sha:         $SHA"
  echo "git_branch:      $(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
  echo "git_status_short:"
  git status --short 2>/dev/null | sed 's/^/  /' || echo "  (unavailable)"
  echo
  echo "go_version:      $(go version 2>/dev/null || echo unavailable)"
  echo "node_version:    $(node --version 2>/dev/null || echo unavailable)"
  echo "npm_version:     $(npm --version 2>/dev/null || echo unavailable)"
  if command -v npx >/dev/null 2>&1 && [ -d "$REPO_ROOT/web/node_modules" ]; then
    echo "playwright_version: $(cd "$REPO_ROOT/web" && npx --no-install playwright --version 2>/dev/null || echo unavailable)"
  else
    echo "playwright_version: unavailable (web/node_modules not installed yet)"
  fi
  echo "uname:           $(uname -a 2>/dev/null || echo unavailable)"
} > "$METADATA_FILE"

echo "proof: SHA=$SHA e2e=$E2E repeat-race=$REPEAT_RACE"
echo "proof: evidence directory: $OUTPUT_DIR"

# ---------------------------------------------------------------------------
# Step runner: logs start/end/duration/exit status for every command,
# writes full stdout+stderr to its own log file, and — on failure — exits
# nonzero immediately while retaining every log written so far.
# ---------------------------------------------------------------------------

run_step() {
  local name="$1"
  shift
  local logfile="$LOG_DIR/${name}.log"
  local start_iso start_epoch end_iso end_epoch duration status

  start_iso="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  start_epoch="$(date +%s)"

  echo "proof: [$name] starting: $*"
  ( "$@" ) >"$logfile" 2>&1
  status=$?

  end_iso="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  end_epoch="$(date +%s)"
  duration=$((end_epoch - start_epoch))

  printf 'STEP=%s\tSTART=%s\tEND=%s\tDURATION_SEC=%s\tEXIT=%s\tCOMMAND=%s\tLOG=%s\n' \
    "$name" "$start_iso" "$end_iso" "$duration" "$status" "$*" "$logfile" \
    >> "$SUMMARY_LOG"

  if [ "$status" -ne 0 ]; then
    echo "proof: [$name] FAILED (exit $status) after ${duration}s — see $logfile" >&2
    echo "RESULT=FAIL FIRST_FAILED_STEP=$name" >> "$SUMMARY_LOG"
    echo "proof: mandatory gate '$name' failed; stopping. Completed step logs retained in $LOG_DIR" >&2
    finalize_and_exit 1
  fi

  echo "proof: [$name] OK (${duration}s)"
  return 0
}

finalize_and_exit() {
  local code="$1"
  {
    echo
    echo "run_ended_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "final_exit_code: $code"
  } >> "$METADATA_FILE"
  exit "$code"
}

# ---------------------------------------------------------------------------
# Web build/test steps (frontend built first so pkg/server/dist exists
# before any Go step that embeds it runs).
# ---------------------------------------------------------------------------

npm_ci() { (cd "$REPO_ROOT/web" && npm ci); }
npm_typecheck() { (cd "$REPO_ROOT/web" && npm run typecheck); }
npm_test_ci() { (cd "$REPO_ROOT/web" && npm run test:ci); }
npm_build() { (cd "$REPO_ROOT/web" && npm run build); }
npm_test_e2e() { (cd "$REPO_ROOT/web" && npm run test:e2e); }
npx_playwright_install() { (cd "$REPO_ROOT/web" && npx playwright install --with-deps chromium); }

run_step "web-npm-ci" npm_ci
run_step "web-typecheck" npm_typecheck
run_step "web-test-ci" npm_test_ci
run_step "web-build" npm_build

# ---------------------------------------------------------------------------
# Go steps
# ---------------------------------------------------------------------------

go_build_all() { (cd "$REPO_ROOT" && go build ./...); }
go_test_all() { (cd "$REPO_ROOT" && go test ./... -count=1); }
go_vet_all() { (cd "$REPO_ROOT" && go vet ./...); }
go_race_targeted() {
  (cd "$REPO_ROOT" && go test -race \
    ./pkg/state ./pkg/peer ./pkg/pty ./pkg/server ./pkg/ws ./pkg/commands/server \
    -count="$REPEAT_RACE")
}
go_race_all() { (cd "$REPO_ROOT" && go test -race ./... -count="$REPEAT_RACE"); }

run_step "go-build" go_build_all
run_step "go-test" go_test_all
run_step "go-vet" go_vet_all

# ---------------------------------------------------------------------------
# Residue check: legacy/v2 vocabulary, dual-runtime selection, and
# compatibility-shim symbols must never reappear in production code. Any
# match must be a justified test assertion or historical doc (excluded via
# docs/history/**) — see docs/release-notes/hard-rewrite.md.
# ---------------------------------------------------------------------------

residue_check() {
  (cd "$REPO_ROOT" && \
    ! git grep -nE 'TERMYARD_V2_STATE|VITE_V2_STATE|termyard\.v2State|AppLegacy|isV2StateEnabled|V2[A-Z]|/api/v2|/ws/v2|state\.Manager|sessionattrs|sessionorder|groupsync|MsgStateUpdate|MsgStateEvent|MsgPeerState|MsgSessionAction|MsgRequestState|_compat|Compat[A-Z]' -- . \
      ':!docs/history/**' ':!pkg/state/INVARIANTS.md' ':!*_test.go' ':!*.test.ts' \
      ':!web/e2e/**' ':!*package-lock.json' ':!testdata/**' ':!scripts/proof.sh' \
  )
}
run_step "residue-check" residue_check

run_step "go-race-targeted" go_race_targeted
run_step "go-race-all" go_race_all

# ---------------------------------------------------------------------------
# E2E (optional). When enabled: install the pinned Playwright browser, run
# the suite with a JSON report captured to the evidence directory, then fail
# the gate if multi-node.spec.ts reports any mandatory skip/fixme.
# ---------------------------------------------------------------------------

if [ "$E2E" = "1" ]; then
  run_step "e2e-playwright-install" npx_playwright_install

  E2E_JSON_REPORT="$OUTPUT_DIR/playwright-report.json"
  export PLAYWRIGHT_JSON_OUTPUT_NAME="$E2E_JSON_REPORT"
  export PLAYWRIGHT_HTML_REPORT="$OUTPUT_DIR/playwright-html-report"
  npm_test_e2e_reported() {
    (cd "$REPO_ROOT/web" && npm run test:e2e -- --reporter=list,json,html)
  }
  run_step "e2e-playwright-run" npm_test_e2e_reported

  # Fail if multi-node.spec.ts reports a mandatory skip or test.fixme.
  # Uses the JSON reporter output (authoritative machine-readable result),
  # not the human-readable list output, to avoid ANSI/format fragility.
  check_no_mandatory_skip() {
    node -e '
      const fs = require("fs");
      const path = process.argv[1];
      const report = JSON.parse(fs.readFileSync(path, "utf8"));
      const offenders = [];
      function walk(suite) {
        for (const spec of suite.specs || []) {
          const file = spec.file || (suite.file || "");
          if (!file.includes("multi-node.spec.ts")) continue;
          for (const t of spec.tests || []) {
            const isFixme = (t.annotations || []).some(a => (a.type || "").toLowerCase() === "fixme");
            const hasSkippedResult = (t.results || []).some(r => r.status === "skipped");
            if (isFixme || t.status === "skipped" || hasSkippedResult) {
              offenders.push(`${file} :: ${spec.title}`);
            }
          }
        }
        for (const s of suite.suites || []) walk(s);
      }
      for (const s of report.suites || []) walk(s);
      if (offenders.length > 0) {
        console.error("mandatory skip/fixme found in multi-node.spec.ts:");
        for (const o of offenders) console.error("  - " + o);
        process.exit(1);
      }
      console.log("no mandatory skip/fixme found in multi-node.spec.ts");
    ' "$E2E_JSON_REPORT"
  }
  run_step "e2e-no-mandatory-skip-check" check_no_mandatory_skip
else
  echo "proof: --e2e 0, skipping Playwright E2E step (not a mandatory gate for this run)"
  printf 'STEP=%s\tSTATUS=%s\n' "e2e-playwright-run" "SKIPPED_BY_FLAG(--e2e 0)" >> "$SUMMARY_LOG"
fi

echo "RESULT=PASS" >> "$SUMMARY_LOG"
echo "proof: all gates passed for SHA=$SHA. Evidence: $OUTPUT_DIR"
finalize_and_exit 0
