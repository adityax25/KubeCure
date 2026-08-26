#!/usr/bin/env bash
#
# Applies, removes, and inspects the deliberate failure scenarios used to exercise detection.
#
# Each scenario is a workload engineered to fail in exactly one way, so that detection can be
# demonstrated live and real pod statuses can be captured as test fixtures.

set -euo pipefail

NAMESPACE="kubecure-demo"
SCENARIO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURE_DIR="${SCENARIO_DIR}/../fixtures"
KUBECTL="${KUBECTL:-kubectl}"

scenarios() {
    find "${SCENARIO_DIR}" -maxdepth 1 -name '*.yaml' ! -name '00-namespace.yaml' \
        -exec basename {} .yaml \; | sort
}

usage() {
    cat <<USAGE
Usage: inject.sh <command> [scenario]

Commands:
  apply [scenario|all]    Create the namespace and apply one scenario, or all of them
  delete [scenario|all]   Remove one scenario, or the entire namespace
  status                  Show pod state for every applied scenario
  capture <scenario>      Write the failing pod's status to a JSON test fixture
  list                    List available scenarios

Available scenarios:
$(scenarios | sed 's/^/  /')
USAGE
}

require_scenario() {
    local name="$1"
    if [[ ! -f "${SCENARIO_DIR}/${name}.yaml" ]]; then
        echo "unknown scenario: ${name}" >&2
        echo "available: $(scenarios | tr '\n' ' ')" >&2
        exit 1
    fi
}

cmd_apply() {
    local target="${1:-all}"
    "${KUBECTL}" apply -f "${SCENARIO_DIR}/00-namespace.yaml"

    if [[ "${target}" == "all" ]]; then
        while read -r name; do
            "${KUBECTL}" apply -f "${SCENARIO_DIR}/${name}.yaml"
        done < <(scenarios)
    else
        require_scenario "${target}"
        "${KUBECTL}" apply -f "${SCENARIO_DIR}/${target}.yaml"
    fi

    echo
    echo "Applied. Failures take up to 60 seconds to surface; probe and scheduling failures are"
    echo "reported only after their grace period elapses."
}

cmd_delete() {
    local target="${1:-all}"
    if [[ "${target}" == "all" ]]; then
        "${KUBECTL}" delete namespace "${NAMESPACE}" --ignore-not-found
    else
        require_scenario "${target}"
        "${KUBECTL}" delete -f "${SCENARIO_DIR}/${target}.yaml" --ignore-not-found
    fi
}

# expected_for names the classification the detector should assign to a scenario. It is a static
# label for comparison by eye, not a computed result: reimplementing classification here would
# create a second source of truth that could drift from the detector itself.
expected_for() {
    case "$1" in
        crashloopbackoff)           echo "CrashLoopBackOff" ;;
        oomkilled)                  echo "OOMKilled" ;;
        imagepullbackoff)           echo "ImagePullBackOff" ;;
        createcontainerconfigerror) echo "CreateContainerConfigError" ;;
        unschedulable)              echo "Unschedulable" ;;
        probefailure)               echo "ProbeFailure" ;;
        *)                          echo "-" ;;
    esac
}

# shorten trims a pod name to fit the table, keeping the trailing identifier that distinguishes
# replicas of the same workload.
shorten() {
    local name="$1" limit=32
    if [[ ${#name} -le ${limit} ]]; then
        echo "${name}"
    else
        echo "${name:0:$((limit - 9))}...${name: -6}"
    fi
}

cmd_status() {
    if ! "${KUBECTL}" get namespace "${NAMESPACE}" >/dev/null 2>&1; then
        echo "namespace ${NAMESPACE} does not exist; run 'make scenarios-apply' first"
        return
    fi

    local rows
    rows="$("${KUBECTL}" get pods -n "${NAMESPACE}" -o json | jq -r '
        def cs: ((.status.containerStatuses // [])[0] // {});
        def scheduling:
            ((.status.conditions // [])
             | map(select(.type == "PodScheduled" and .status == "False"))
             | first // {}).reason // "-";
        .items[] | [
            (.metadata.labels["kubecure.io/scenario"] // "-"),
            .metadata.name,
            (.status.phase // "-"),
            ( cs.state.waiting.reason
              // cs.state.terminated.reason
              // (if cs.state.running then "Running" else null end)
              // scheduling ),
            (cs.lastState.terminated.reason // "-"),
            ((cs.restartCount // 0) | tostring),
            ((cs.ready // false) | tostring)
        ] | @tsv')"

    printf "%-28s %-32s %-10s %-27s %-17s %5s %6s %s\n" \
        "SCENARIO" "POD" "PHASE" "DISPLAYED NOW" "PREVIOUS EXIT" "RST" "READY" "EXPECTED"
    printf "%-28s %-32s %-10s %-27s %-17s %5s %6s %s\n" \
        "----------------------------" "--------------------------------" "----------" \
        "---------------------------" "-----------------" "-----" "------" "------------------"

    while IFS=$'\t' read -r scenario pod phase displayed previous restarts ready; do
        [[ -n "${scenario}" ]] || continue
        printf "%-28s %-32s %-10s %-27s %-17s %5s %6s %s\n" \
            "${scenario}" "$(shorten "${pod}")" "${phase}" "${displayed}" \
            "${previous}" "${restarts}" "${ready}" "$(expected_for "${scenario}")"
    done <<< "${rows}"

    cat <<'LEGEND'

DISPLAYED NOW   what kubectl reports at this instant. It oscillates for restarting containers,
                because the kubelet cycles them between terminated and waiting.
PREVIOUS EXIT   the reason the previous instance died. Stable, and the only place a memory kill
                survives once the container has restarted.
EXPECTED        the classification the detector should assign. For the oomkilled scenario this
                deliberately differs from DISPLAYED NOW, which is the behaviour ADR-013 requires.
LEGEND
}

# cmd_capture records a real pod status as a test fixture. Hand written fixtures prove the
# classification logic is self consistent; captured ones prove it matches what the cluster emits.
cmd_capture() {
    local name="${1:-}"
    [[ -n "${name}" ]] || { echo "capture requires a scenario name" >&2; exit 1; }
    require_scenario "${name}"
    mkdir -p "${FIXTURE_DIR}"

    local pod
    pod="$("${KUBECTL}" get pods -n "${NAMESPACE}" \
        -l "kubecure.io/scenario=${name}" \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"

    if [[ -z "${pod}" ]]; then
        echo "no pod found for scenario ${name}; apply it first" >&2
        exit 1
    fi

    "${KUBECTL}" get pod "${pod}" -n "${NAMESPACE}" -o json \
        | jq 'del(.metadata.managedFields)' > "${FIXTURE_DIR}/${name}.json"

    echo "captured ${pod} to ${FIXTURE_DIR}/${name}.json"
}

main() {
    local command="${1:-}"
    shift || true

    case "${command}" in
        apply)   cmd_apply "${1:-all}" ;;
        delete)  cmd_delete "${1:-all}" ;;
        status)  cmd_status ;;
        capture) cmd_capture "${1:-}" ;;
        list)    scenarios ;;
        *)       usage; [[ -n "${command}" ]] && exit 1 || exit 0 ;;
    esac
}

main "$@"
