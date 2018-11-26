#!/usr/bin/env bash
set -o errexit
set -o pipefail
set -o nounset

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
readonly SCRIPTS_DIR="${SCRIPT_DIR}/scripts"

if [[ $# -gt 1 ]]; then
  echo "usage: $(basename "$0") [test filter]"  >&2
  exit 1
fi

RUN="${E2E_RUN_FILTER:-}"
if [[ "${RUN}" ]]; then
  RUN="-run ${RUN}"
fi

echo "==> installing dependencies..."
"${SCRIPTS_DIR}/install_deps.sh"

(
  cd "${SCRIPT_DIR}"
  echo "==> running E2E tests..."
  # shellcheck disable=SC2086
  go test -v -timeout 1h -count 1 -tags integration ${RUN} "./..."
)
