"""Malformed review records must fail the offline deployment prerequisite."""
import copy
import importlib.util
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("feature_review", ROOT / "scripts/check_feature_review.py")
CHECKER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECKER)


class FeatureReviewTest(unittest.TestCase):
    def setUp(self):
        self.record = json.loads((ROOT / "docs/reviews/issue-13-stage-a.json").read_text())

    def assert_invalid(self, fragment):
        errors = CHECKER.validate(self.record, ROOT)
        self.assertTrue(any(fragment in error for error in errors), errors)

    def test_committed_record_is_complete(self):
        self.assertEqual(CHECKER.validate(self.record, ROOT), [])

    def test_missing_control_is_rejected(self):
        self.record["controls"].pop()
        self.assert_invalid("complete pinned register")

    def test_duplicate_control_is_rejected(self):
        self.record["controls"].append(copy.deepcopy(self.record["controls"][0]))
        self.assert_invalid("duplicate control")

    def test_unknown_baseline_is_rejected(self):
        self.record["baseline_commit"] = "0" * 40
        self.assert_invalid("baseline commit")

    def test_missing_resource_scope_is_rejected(self):
        self.record["controls"][0]["scope"]["resources"] = []
        self.assert_invalid("resources scope")

    def test_broken_evidence_reference_is_rejected(self):
        self.record["controls"][0]["evidence"] = ["missing-evidence-file.json"]
        self.assert_invalid("missing owner-local evidence")

    def test_unobserved_control_needs_gap(self):
        self.record["controls"][0]["coverage_gaps"] = []
        self.assert_invalid("needs a stated coverage gap")

    def test_not_applicable_needs_reason(self):
        control = next(row for row in self.record["controls"] if row["status"] == "not-applicable")
        del control["not_applicable_reason"]
        self.assert_invalid("not-applicable reason")

    def test_exception_needs_approval_reference(self):
        self.record["exception_references"]["LOG-DET-PREMEASUREMENT"]["approval_reference"] = ""
        self.assert_invalid("missing approval_reference")

    def test_timing_gap_needs_exception_reference(self):
        for control_id in ("LOG-01", "DET-01"):
            with self.subTest(control=control_id):
                control = next(row for row in self.record["controls"] if row["id"] == control_id)
                control["exception_refs"] = []
                self.assert_invalid(f"{control_id}: missing required timing exception")

    def test_unknown_exception_is_rejected(self):
        self.record["controls"][0]["exception_refs"] = ["unrecorded-exception"]
        self.assert_invalid("unknown exception reference")


if __name__ == "__main__":
    unittest.main()
