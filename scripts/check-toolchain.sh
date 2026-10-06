#!/usr/bin/env bash
# Compares the installed toolchain against .tool-versions and reports every
# mismatch at once rather than the first one.
#
# Reporting all of them is deliberate. Fixing one version at a time, with a
# fresh CI run between each, is the slow way to discover that three things
# disagree.
set -uo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Two files on purpose. .tool-versions is read by mise and may only name tools
# mise can resolve; the database clients are recorded apart from it so that
# pinning them does not make mise warn on every command run here.
mise_pins="$root/.tool-versions"
db_pins="$root/.db-versions"

for f in "$mise_pins" "$db_pins"; do
  if [ ! -f "$f" ]; then
    echo "error: $f is missing, so its tools are unpinned" >&2
    exit 2
  fi
done

# Tools named on the command line are the only ones checked. CI names the ones it
# actually installs, because a runner image has no bun and no pinned psql, and a
# check that demands a tool the environment never had would fail on being
# correct. Locally the argument is empty, so everything is checked.
requested=("$@")

status=0
report() { printf '  %-9s pinned %-10s found %s\n' "$1" "$2" "$3"; }

# golang is pinned with a patch component while "go version" prints
# "go version go1.27.0 linux/amd64", so it is matched by prefix.
wanted() {
  [ ${#requested[@]} -eq 0 ] && return 0
  local t
  for t in "${requested[@]}"; do [ "$t" = "$1" ] && return 0; done
  return 1
}

check_go() {
  local want found
  want="$(awk '$1=="golang"{print $2}' "$mise_pins" "$db_pins")"
  if [ -z "$want" ]; then
    # Without this the patterns below degenerate to ?*, which matches any
    # nonempty version, so dropping the pin would make the check pass for every
    # possible Go. A pin file with no Go entry must be an error, not a pass.
    report golang "(unpinned)" "-"
    return 1
  fi
  found="$(go env GOVERSION 2>/dev/null || echo missing)"
  found="${found#go}"
  report golang "$want" "${found:-missing}"
  # A two component pin is a minor line and matches any patch in it, which is
  # what go.mod actually declares. A three component pin is exact apart from
  # build metadata, so go1.27.0-X:nodwarf5 satisfies a pin of 1.27.0 while a
  # 1.27.01 does not.
  case "$want" in
    *.*.*)
      case "$found" in
        "$want"|"$want"?*) ;;
        *) status=1 ;;
      esac
      ;;
    *)
      case "$found" in
        "$want"|"$want".*) ;;
        *) status=1 ;;
      esac
      ;;
  esac
}

# The pin name and the binary name differ, asdf calls Node "nodejs" while the
# binary is "node". Keeping them in one place stops the file being read by a
# human and the check being run by a machine from disagreeing.
check_simple() {
  local pin="$1" bin="$2" flag="${3:---version}" want found
  want="$(awk -v t="$pin" '$1==t{print $2}' "$mise_pins" "$db_pins")"
  if [ -z "$want" ]; then
    report "$pin" "(unpinned)" "-"
    return 1
  fi
  if ! command -v "$bin" >/dev/null 2>&1; then
    report "$pin" "$want" "not installed"
    return 1
  fi
  # Two components as well as three, because psql reports "PostgreSQL 18.6"
  # and a three component match would silently never fire for it.
  # stderr is dropped on purpose. The node and bun binaries here are mise shims,
  # and a shim prints its own warnings there. Reading 2>&1 let a warning line
  # become the reported version, which is how psql's number ended up reported
  # against node.
  found="$("$bin" $flag 2>/dev/null | head -1 | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1)"
  report "$pin" "$want" "${found:-unknown}"
  [ "$found" = "$want" ]
}

echo "toolchain check (.tool-versions for mise tools, .db-versions for clients)"
if wanted golang; then check_go || status=1; fi
if wanted nodejs; then check_simple nodejs node --version || status=1; fi
if wanted bun; then check_simple bun bun --version || status=1; fi
if wanted sqlite3; then check_simple sqlite3 sqlite3 --version || status=1; fi
if wanted psql; then check_simple psql psql --version || status=1; fi

# The workflow extracts these same two values to install node and bun, so an
# unpinned or misspelled entry would otherwise show up as a confusing failure in
# a setup action rather than here.
node_pin="$(awk '$1=="nodejs"{print $2}' "$mise_pins")"
bun_pin="$(awk '$1=="bun"{print $2}' "$mise_pins")"
if [ -z "$node_pin" ] || [ -z "$bun_pin" ]; then
  echo "error: .tool-versions must pin nodejs and bun, CI installs both from it" >&2
  exit 2
fi

if [ ${#requested[@]} -gt 0 ]; then
  echo "checked: ${requested[*]}"
else
  echo "checked: all pinned tools"
fi

if [ "$status" -eq 0 ]; then
  echo "all tools match .tool-versions"
else
  echo "mismatch: install the pinned versions, or update .tool-versions deliberately" >&2
fi
exit "$status"
