#!/usr/bin/env bash
set -o errexit
set -o pipefail
set -o nounset

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
readonly SCRIPT_DIR
# shellcheck source=./utils.sh
source "${SCRIPT_DIR}/utils.sh"

ensure_deps
