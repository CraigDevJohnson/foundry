#!/usr/bin/env bash
# Usage: [APPROVED_PLAN=<digest>] [REBUILD_HOST=true] [TOFU=tofu] scripts/deploy.sh plan|apply
#
# Runs the infra root the way the Deploy workflow (.github/workflows/deploy.yml)
# does, with the AWS credentials in the environment and infra/deploy.tfvars,
# after `task build` has written dist/bootstrap.zip. The repository is public,
# so in GitHub Actions the full plan and apply output goes to a file, not the
# log: only the resource headers, the progress lines and the totals reach the
# log and the job summary, and the progress lines lose their "[id=...]" suffix.
# OpenTofu's own errors still reach the log, because they're written to
# stderr. Run anywhere else, such as on Craig's computer, it also prints the
# full output, so he can read the values a Deploy run leaves out
# (infra/README.md).
#
# Every plan ends with its digest (cmd/plandigest), which covers the plan and
# the commit it was made from. An apply run plans again and applies only when
# that plan's digest is APPROVED_PLAN, the digest Craig copied from the plan
# run he read, so what applies is exactly what he approved, from the code he
# approved. If main or the state changed since, it stops before applying and
# posts the new plan and its digest instead (ADR 0007).
#
# REBUILD_HOST=true plans the host's replacement (-replace=aws_instance.host),
# which keeps the data volume and the Elastic IP. The digest covers it, so the
# apply run must ask for it too. A plan that stops the host applies only while
# the host is stopped, so Foundry has shut down cleanly first: the stop
# OpenTofu makes doesn't run Foundry's own shutdown. That is a plan that
# replaces or destroys the host, detaches its data volume, or changes a
# setting the AWS provider changes only on a stopped instance, such as its
# type.
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
tofu="${TOFU:-tofu}"
mode="${1:-}"
case "$mode" in
plan | apply) ;;
*)
	printf 'usage: [APPROVED_PLAN=<digest>] %s plan|apply\n' "${0##*/}" >&2
	exit 2
	;;
esac
approved="${APPROVED_PLAN:-}"
if [[ "$mode" == apply && ! "$approved" =~ ^[0-9a-f]{16}$ ]]; then
	printf '::error::An apply run needs the plan digest from the summary of the plan run you read: run Deploy with mode apply and that digest as plan.\n' >&2
	exit 1
fi
plan_args=()
case "${REBUILD_HOST:-false}" in
true) plan_args+=(-replace=aws_instance.host) ;;
false | '') ;;
*)
	printf '::error::REBUILD_HOST must be true or false\n' >&2
	exit 1
	;;
esac
if [[ ! -f "$root_dir/dist/bootstrap.zip" ]]; then
	printf '::error::dist/bootstrap.zip is missing: run task build first\n' >&2
	exit 1
fi

commit="$(git -C "$root_dir" rev-parse HEAD)"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
out="$work/output"

# deploy.tfvars sets no named profile, so the backend and the provider use the
# credentials in the environment. The backend reads it at init too. The deploy
# role can write the state, so init installs only the providers in the lock
# file, never one that only the state names (infra/README.md).
"$tofu" -chdir="$root_dir/infra" init -input=false -no-color -lockfile=readonly \
	-var-file=deploy.tfvars

# report STEP: writes the trimmed output of the last step to the log and the
# job summary. It keeps the "# address will be ..." headers, the progress lines
# and the totals, drops everything indented deeper, which is where the
# attribute values are, and strips the "[id=...]" suffix from the progress
# lines.
report() {
	local trimmed
	trimmed="$(grep -E '^(Plan:|Apply complete!|No changes\.|Changes to Outputs:|  # |[^ ]+: (Creating|Modifying|Destroying|Refreshing state|Reading|Read complete|Still |Creation complete|Modifications complete|Destruction complete|Importing|Import complete))' "$out" |
		sed -E 's/ \[id=[^]]*\]//' || true)"
	{
		printf '## OpenTofu %s\n\n' "$1"
		printf '%s\n' "\`\`\`text" "${trimmed:-(nothing to report)}" "\`\`\`"
	} | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
	if [[ "${GITHUB_ACTIONS:-}" != true ]]; then
		printf '\n## Full OpenTofu %s (not in GitHub Actions)\n\n' "$1"
		cat "$out"
	fi
}

status=0
"$tofu" -chdir="$root_dir/infra" plan -input=false -no-color -lock-timeout=5m \
	-var-file=deploy.tfvars ${plan_args[@]+"${plan_args[@]}"} -out="$work/tfplan" >"$out" || status=$?
report plan
if [[ "$status" -ne 0 ]]; then
	printf '::error::tofu plan failed (exit %s); see the messages above\n' "$status" >&2
	exit "$status"
fi
if ! "$tofu" -chdir="$root_dir/infra" show -json "$work/tfplan" >"$work/plan.json" ||
	! digest="$(cd "$root_dir" && go run ./cmd/plandigest -commit "$commit" <"$work/plan.json")"; then
	printf '::error::the saved plan could not be read for its digest\n' >&2
	exit 1
fi
# What EC2 reported for the host at the plan's refresh, when applying the plan
# stops it; empty when it doesn't, and "unknown" when the refresh didn't find
# it. The plan stops the host when it replaces or destroys it; when it removes
# the data volume's attachment, which stops the host before detaching
# (stop_instance_before_detaching); and when it changes one of the host's
# settings that the AWS provider (6.66.0) changes only on a stopped instance,
# stopping it and starting it again around the change: its type, user data,
# CPU options or capacity reservation. The host is found by type, in any
# module and under whatever address the plan moves it to.
host_state_filter='
def changed($names): .change as $change | any($names[]; $change.before[.] != $change.after[.]);
def modules: ., (.child_modules[]? | modules);
[.prior_state.values.root_module? // empty | modules | .resources[]?] as $refreshed |
def state(f): first($refreshed[] | select(.type == "aws_instance" and f) | .values.instance_state) // "unknown";
[.resource_changes[]? |
	if .type == "aws_instance" and
		((.change.actions | index("delete")) or
			(.change.actions == ["update"] and
				changed(["instance_type", "user_data", "user_data_base64", "cpu_options", "capacity_reservation_specification"]))) then
		.address as $address | state(.address == $address)
	elif .type == "aws_volume_attachment" and (.change.actions | index("delete")) then
		.change.before.instance_id as $id | state(.values.id == $id)
	else empty end] |
if length == 0 then empty else first(.[] | select(. != "stopped")) // "stopped" end'
if ! host_state="$(jq -r "$host_state_filter" "$work/plan.json")"; then
	printf '::error::the saved plan could not be read for the host'"'"'s state\n' >&2
	exit 1
fi
printf "\nPlan digest: \`%s\` (commit \`%s\`)\n" "$digest" "${commit:0:12}" | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
rebuild_note=""
if [[ ${#plan_args[@]} -gt 0 ]]; then
	rebuild_note=", with rebuild host checked"
fi
host_note=""
if [[ -n "$host_state" && "$host_state" != stopped ]]; then
	# Stopping the host can change the plan's digest, so the plan to apply is
	# the one made once it is stopped.
	host_note="$(printf "This plan stops the host, which is \`%s\`: it replaces or removes it, detaches its data volume, or changes a setting EC2 changes only on a stopped host. Until the host is \`stopped\`, an apply applies nothing. Stop it first with Host control's stop, which shuts Foundry down cleanly, then run Deploy again with mode \`plan\` and apply the digest that run posts." "$host_state")"
fi
if [[ "$mode" == plan ]]; then
	if [[ -n "$host_note" ]]; then
		printf '\n%s\n' "$host_note" | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
		exit 0
	fi
	printf "\nTo apply exactly this plan, run Deploy again with mode \`apply\` and plan \`%s\`%s.\n" "$digest" "$rebuild_note" |
		tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
	exit 0
fi
if [[ -n "$host_note" ]]; then
	printf '\nNothing was applied. %s\n' "$host_note" | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
	printf '::error::the plan stops a host that is not stopped; nothing was applied\n' >&2
	exit 1
fi
if [[ "$digest" != "$approved" ]]; then
	printf "\nNothing was applied: this plan (digest \`%s\`) is not the plan you approved (\`%s\`), because main or the state changed since. Read the plan above; to apply it, run Deploy with mode \`apply\` and plan \`%s\`%s.\n" \
		"$digest" "$approved" "$digest" "$rebuild_note" | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
	printf '::error::the plan differs from the approved plan; nothing was applied\n' >&2
	exit 1
fi

"$tofu" -chdir="$root_dir/infra" apply -input=false -no-color -lock-timeout=5m \
	"$work/tfplan" >"$out" || status=$?
report apply
if [[ "$status" -ne 0 ]]; then
	printf '::error::tofu apply failed (exit %s); see the messages above\n' "$status" >&2
fi
exit "$status"
