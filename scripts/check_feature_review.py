#!/usr/bin/env python3
"""Check review completeness offline. This does not verify AWS or authorize changes."""
import argparse
import datetime
import json
from pathlib import Path
import sys

BASELINE_COMMIT = "207d57c1148e0744da2bca660a16b7d43262ef71"
CONTROL_IDS = frozenset("FND-01 IAM-01 IAM-02 ORG-01 PUB-01 PUB-02 ENC-01 LOG-01 LOG-02 OPS-01 REC-01 REC-02 DET-01 EVD-01 EVD-02 FIN-01".split())
STATUSES = {"pass", "fail", "not-applicable", "not-tested"}
OWNER = "CraigDevJohnson/foundry"


def validate(record, root):
    errors = []

    def require(condition, message):
        if not condition:
            errors.append(message)

    def text(value):
        return isinstance(value, str) and bool(value.strip())

    def strings(value):
        return isinstance(value, list) and bool(value) and all(text(item) for item in value)

    def scope(value, label):
        require(isinstance(value, dict), f"{label}: scope must be an object")
        if not isinstance(value, dict):
            return
        for field in ("accounts", "regions", "resources"):
            require(strings(value.get(field)), f"{label}: missing {field} scope")

    def timestamp(value, label):
        try:
            parsed = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
            require(parsed.tzinfo is not None, f"{label}: timestamp needs a timezone")
        except (AttributeError, TypeError, ValueError):
            require(False, f"{label}: invalid timestamp")

    if not isinstance(record, dict):
        return ["review must be an object"]
    require(record.get("schema_version") == 1, "unsupported schema_version")
    require(record.get("owner") == OWNER, "review owner does not match this repository")
    require(record.get("baseline_commit") == BASELINE_COMMIT, "baseline commit does not match the adopted register")
    require(record.get("baseline_register") == f"https://github.com/CraigDevJohnson/aws-setup/blob/{BASELINE_COMMIT}/docs/planning/initial-baseline-controls.md", "baseline_register must identify the pinned adopted register")
    require(text(record.get("feature")), "missing feature")
    require(record.get("execution_status") in {"not-authorized", "authorized", "completed"}, "missing execution_status")
    timestamp(record.get("reviewed_at"), "reviewed_at")
    scope(record.get("scope"), "review")
    methods = record.get("methods", {})
    require(isinstance(methods, dict) and bool(methods), "missing methods")
    if not isinstance(methods, dict):
        methods = {}
    for name, method in methods.items():
        require(isinstance(method, dict) and all(text(method.get(field)) for field in ("tool", "version", "method")), f"method {name}: missing tool, version, or method")
    references = record.get("exception_references", {})
    require(isinstance(references, dict), "exception_references must be an object")
    if not isinstance(references, dict):
        references = {}
    for name, reference in references.items():
        if not isinstance(reference, dict):
            require(False, f"exception {name}: must be an object")
            continue
        for field in ("owner", "owner_record", "approval_reference", "execution_reference", "review_trigger"):
            require(text(reference.get(field)), f"exception {name}: missing {field}")
        require(reference.get("status") in {"planning-accepted-execution-pending", "approved"}, f"exception {name}: invalid status")
    controls = record.get("controls", [])
    require(isinstance(controls, list), "controls must be an array")
    if not isinstance(controls, list):
        return errors
    ids = [control.get("id") for control in controls if isinstance(control, dict)]
    require(len(ids) == len(controls) and all(isinstance(item, str) for item in ids), "every control needs a string ID")
    if not all(isinstance(item, str) for item in ids):
        return errors
    require(set(ids) == CONTROL_IDS, "control IDs must cover the complete pinned register")
    require(len(ids) == len(set(ids)), "duplicate control ID")
    for control in controls:
        if not isinstance(control, dict):
            continue
        label = control.get("id", "unknown control")
        require(control.get("owner") == OWNER, f"{label}: missing owner-local review responsibility")
        require(control.get("status") in STATUSES, f"{label}: invalid status")
        require(text(control.get("assessment")), f"{label}: missing assessment")
        scope(control.get("scope"), label)
        observed = control.get("observed_at")
        require("observed_at" in control, f"{label}: missing observed_at (use null if unobserved)")
        if observed is not None:
            timestamp(observed, f"{label} observed_at")
        refs = control.get("method_refs")
        require(strings(refs) and all(ref in methods for ref in refs), f"{label}: missing or unknown method reference")
        evidence = control.get("evidence")
        require(strings(evidence), f"{label}: missing redacted evidence reference")
        if isinstance(evidence, list):
            for reference in evidence:
                if not text(reference):
                    continue
                if reference.startswith("https://"):
                    continue
                path = (root / reference.split("#", 1)[0]).resolve()
                require(path.is_relative_to(root.resolve()) and path.is_file(), f"{label}: missing owner-local evidence file {reference}")
        gaps = control.get("coverage_gaps")
        require(isinstance(gaps, list) and all(text(gap) for gap in gaps), f"{label}: missing coverage_gaps list")
        if control.get("status") in {"fail", "not-tested"}:
            require(strings(gaps), f"{label}: fail/not-tested needs a stated coverage gap")
        if control.get("status") == "not-applicable":
            require(text(control.get("not_applicable_reason")), f"{label}: missing not-applicable reason")
        exceptions = control.get("exception_refs")
        if label in {"LOG-01", "DET-01"}:
            require(isinstance(exceptions, list) and "LOG-DET-PREMEASUREMENT" in exceptions, f"{label}: missing required timing exception reference")
        require(isinstance(exceptions, list) and all(text(ref) and ref in references for ref in exceptions), f"{label}: missing or unknown exception reference")
    return errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("record", nargs="?", default="docs/reviews/issue-13-stage-a.json")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    try:
        record = json.loads((root / args.record).read_text())
    except (OSError, ValueError) as error:
        print(f"Invalid review: {error}", file=sys.stderr)
        return 1
    errors = validate(record, root)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print("Feature review complete: 16 control records. AWS behavior and execution approval are not verified.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
