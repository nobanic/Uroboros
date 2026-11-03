#!/usr/bin/env bash
# Remove a target container from the sealed Uroboros lab network.
#   ./detach-target.sh <container-name-or-id>
set -euo pipefail
LAB="uroboros_sandbox_lab"
TARGET="${1:?usage: ./detach-target.sh <container-name-or-id>}"
docker network disconnect "$LAB" "$TARGET" && echo "'$TARGET' detached from '$LAB'."
