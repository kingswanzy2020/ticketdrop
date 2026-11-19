#!/usr/bin/env bash
# Prints, as a one-line JSON list, the services whose image a change needs
# rebuilt.
#
#   changed-services.sh <base> <head>
#
# Each Dockerfile copies go.mod, go.sum, pkg/ and its own services/<name>/,
# so those are the only paths that change an image:
#   services/<name>/...                      that service
#   pkg/, go.mod, go.sum, .dockerignore      all of them
#   .github/                                 all of them, so a change to CI is
#                                            tested on every image
#   anything else                            none
#
# With no usable base (a new branch pushes an all-zero "before", a force push
# can leave it out of the clone) it builds all of them.
set -euo pipefail

base=${1:-}
head=${2:?usage: changed-services.sh <base> <head>}

# The services that exist at head: a directory under services/ with a
# Dockerfile. A new service is picked up and a deleted one drops out.
services_at_head() {
  local dir
  git ls-tree -d --name-only "$head" services/ | while read -r dir; do
    if git cat-file -e "$head:$dir/Dockerfile" 2>/dev/null; then
      echo "${dir#services/}"
    fi
  done
}

emit() {
  jq -cn '$ARGS.positional' --args "$@"
}

mapfile -t all < <(services_at_head | sort)

if [[ -z $base || $base =~ ^0+$ ]] || ! git cat-file -e "$base^{commit}" 2>/dev/null; then
  echo "no usable base commit '$base': building all" >&2
  emit "${all[@]}"
  exit 0
fi

# Three dots: what head changed since it left base. --no-renames reports a
# moved file under both its old and its new path.
if ! changed=$(git diff --no-renames --name-only "$base...$head"); then
  echo "no merge base between $base and $head: building all" >&2
  emit "${all[@]}"
  exit 0
fi

declare -A wanted=()
while read -r path; do
  case $path in
    pkg/* | go.mod | go.sum | .dockerignore | .github/*)
      echo "$path changes every image: building all" >&2
      emit "${all[@]}"
      exit 0
      ;;
    services/*/*)
      name=${path#services/}
      wanted[${name%%/*}]=1
      ;;
  esac
done <<<"$changed"

build=()
for name in "${all[@]}"; do
  if [[ -n ${wanted[$name]:-} ]]; then
    build+=("$name")
  fi
done
emit "${build[@]}"
