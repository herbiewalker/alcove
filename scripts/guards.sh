#!/usr/bin/env bash
# Alcove — constraint guards.
#
# The single source of truth for CLAUDE.md's hard constraints as executable
# checks (principle 1: a rule a human has to remember is a rule that will be
# broken; principle 4: one source). The Makefile, bootstrap.ps1 and CI all call
# THIS script; do not reimplement a guard anywhere else.
#
# Usage:  scripts/guards.sh [all|digest-pin|no-secrets|no-outbound|no-plaintext]
#
# Runs on Git Bash (Windows dev box) and on Ubuntu (CI). Keep it POSIX-ish.
# Guards that need code which does not exist yet report SKIP, never a fake OK.

set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

FAIL=0

pass() { printf '  \033[32mOK\033[0m    %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; FAIL=1; }
skip() { printf '  \033[33mSKIP\033[0m  %s\n' "$1"; }

# --- hard constraint 9: base images pinned by digest -------------------------
guard_digest_pin() {
  if [ ! -f Dockerfile ]; then
    skip "digest-pin: no Dockerfile yet (arrives in M0)"
    return
  fi
  local bad
  bad=$(grep -E '^FROM ' Dockerfile | grep -v '@sha256:')
  if [ -n "$bad" ]; then
    fail "digest-pin: unpinned base image in Dockerfile"
    printf '        %s\n' "$bad"
    return
  fi
  pass "digest-pin: every FROM pinned by @sha256:"
}

# --- hard constraints 4 + 10: nothing secret-shaped or personal in git -------
# Includes real .tryst files: they are people's actual private history, and one
# committed by accident is unrecoverable once pushed.
guard_no_secrets() {
  local ok=1

  if git ls-files --error-unmatch .env >/dev/null 2>&1; then
    fail "no-secrets: .env is tracked"
    ok=0
  fi

  local tracked
  tracked=$(git ls-files '*.tryst' '*.key' '*.pem' '*.pfx' '*.p12' 2>/dev/null)
  if [ -n "$tracked" ]; then
    fail "no-secrets: secret-shaped or real-backup files are tracked"
    printf '        %s\n' "$tracked"
    ok=0
  fi

  # A long literal assigned to a password/passphrase/secret/key name. Test files
  # are exempt: they legitimately use fixture values.
  local hits
  hits=$(git ls-files '*.go' '*.yml' '*.yaml' '*.ps1' 2>/dev/null \
    | grep -v '_test\.go$' \
    | xargs grep -nEI '(passphrase|password|secret|apikey|api_key)[[:space:]]*[:=][[:space:]]*"[A-Za-z0-9/+_-]{12,}"' 2>/dev/null)
  if [ -n "$hits" ]; then
    fail "no-secrets: possible hardcoded credential"
    printf '        %s\n' "$hits"
    ok=0
  fi

  [ "$ok" = "1" ] && pass "no-secrets: no tracked secrets, no hardcoded credentials"
}

# --- hard constraint 3: no outbound network code outside the allowlist -------
# The only sanctioned outbound clients are the optional notifier (internal/notify)
# and the localhost healthcheck probe in cmd/alcove/main.go. Anything else that
# builds an HTTP client or dials out is a constraint violation.
guard_no_outbound() {
  local hits
  hits=$(git ls-files '*.go' 2>/dev/null \
    | grep -v '_test\.go$' \
    | grep -v '^internal/notify/' \
    | grep -v '^cmd/alcove/main\.go$' \
    | xargs grep -nE 'http\.(Get|Post|Head|PostForm|DefaultClient|NewRequest)|http\.Client\{|net\.Dial|tls\.Dial' 2>/dev/null)
  if [ -n "$hits" ]; then
    fail "no-outbound: network client code outside the allowlist"
    printf '        %s\n' "$hits"
    return
  fi
  pass "no-outbound: no network client code outside internal/notify and the healthcheck probe"
}

# --- hard constraint 4 / N16: no plaintext user data on the data volume -----------
# Armed when internal/store exists (M2): a Go test ingests a synthetic dataset and
# scans the data directory for ZIP magic, JSON and a sentinel string.
guard_no_plaintext() {
  if [ ! -d internal/store ]; then
    skip "no-plaintext: no storage layer yet (arrives in M2)"
    return
  fi
  if go test -run TestGuard_NoPlaintext -count=1 ./internal/store/ >/tmp/alcove-guard.log 2>&1; then
    pass "no-plaintext: no plaintext found on the data volume"
  else
    fail "no-plaintext: TestGuard_NoPlaintext failed"
    sed -n 's/^/        /p' /tmp/alcove-guard.log | head -20
  fi
}

main() {
  local what="${1:-all}"
  echo "alcove constraint guards (${what})"
  case "$what" in
    all)         guard_digest_pin; guard_no_secrets; guard_no_outbound; guard_no_plaintext ;;
    digest-pin)  guard_digest_pin ;;
    no-secrets)  guard_no_secrets ;;
    no-outbound) guard_no_outbound ;;
    no-plaintext) guard_no_plaintext ;;
    *) echo "unknown guard: $what" >&2; exit 2 ;;
  esac

  if [ "$FAIL" != "0" ]; then
    echo "guards FAILED — see CLAUDE.md 'Hard constraints'" >&2
    exit 1
  fi
  echo "guards passed"
}

main "$@"
