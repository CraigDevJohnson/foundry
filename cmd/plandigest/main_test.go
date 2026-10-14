package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// A small `tofu show -json` plan: the controller's code changes, the host is
// untouched, and one output changes.
const basePlan = `{
  "format_version": "1.2",
  "resource_changes": [
    {
      "address": "aws_lambda_function.controller[\"prod\"]",
      "change": {
        "actions": ["update"],
        "before": {"id": "foundry-prod-controller", "arn": "arn:aws:lambda:us-west-2:111111111111:function:foundry-prod-controller", "source_code_hash": "old", "last_modified": "2026-10-13"},
        "after": {"id": "foundry-prod-controller", "source_code_hash": "new", "memory_size": 128},
        "after_unknown": {"last_modified": true}
      }
    },
    {
      "address": "aws_instance.host",
      "change": {
        "actions": ["no-op"],
        "before": {"id": "i-0123", "instance_state": "running"},
        "after": {"id": "i-0123", "instance_state": "running"},
        "after_unknown": {}
      }
    }
  ],
  "output_changes": {
    "controller_url": {"actions": ["update"], "after": "https://new.example", "after_unknown": false},
    "host_id": {"actions": ["no-op"], "after": "i-0123", "after_unknown": false}
  }
}`

func digestOf(t *testing.T, plan string) string {
	t.Helper()
	d, err := digest(strings.NewReader(plan))
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return d
}

func TestDigestIsSixteenHexCharacters(t *testing.T) {
	d := digestOf(t, basePlan)
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(d) {
		t.Fatalf("digest %q, want 16 lowercase hex characters", d)
	}
	if again := digestOf(t, basePlan); again != d {
		t.Fatalf("same plan gave %q then %q", d, again)
	}
}

func TestDigestIgnoresWhatDescribesThePresent(t *testing.T) {
	base := digestOf(t, basePlan)
	for name, edit := range map[string][2]string{
		"an unchanged host stops":          {`"instance_state": "running"}`, `"instance_state": "stopped"}`},
		"a changed resource's other state": {`"last_modified": "2026-10-13"`, `"last_modified": "2026-10-14"`},
		"an unchanged output":              {`"after": "i-0123", "after_unknown": false`, `"after": "i-9999", "after_unknown": false`},
		"whitespace":                       {"\n    {\n      \"address\": \"aws_instance.host\"", "{\"address\":\"aws_instance.host\""},
	} {
		t.Run(name, func(t *testing.T) {
			edited := strings.Replace(basePlan, edit[0], edit[1], 1)
			if edited == basePlan {
				t.Fatalf("the edit %q didn't apply", edit[0])
			}
			if got := digestOf(t, edited); got != base {
				t.Errorf("digest changed to %q, want %q", got, base)
			}
		})
	}
}

func TestDigestCoversWhatTheApplyDoes(t *testing.T) {
	base := digestOf(t, basePlan)
	for name, edit := range map[string][2]string{
		"another action":               {`"actions": ["update"],`, `"actions": ["delete", "create"],`},
		"another planned value":        {`"memory_size": 128`, `"memory_size": 256`},
		"another unknown value":        {`"after_unknown": {"last_modified": true}`, `"after_unknown": {"last_modified": true, "version": true}`},
		"another object":               {`"before": {"id": "foundry-prod-controller"`, `"before": {"id": "foundry-dev-controller"`},
		"another ARN":                  {`function:foundry-prod-controller", "source_code_hash": "old"`, `function:foundry-dev-controller", "source_code_hash": "old"`},
		"another address":              {`"address": "aws_lambda_function.controller[\"prod\"]"`, `"address": "aws_lambda_function.controller[\"dev\"]"`},
		"another output value":         {`"after": "https://new.example"`, `"after": "https://other.example"`},
		"a resource starts changing":   {`"actions": ["no-op"],` + "\n        \"before\": {\"id\": \"i-0123\"", `"actions": ["update"],` + "\n        \"before\": {\"id\": \"i-0123\""},
		"a number written another way": {`"memory_size": 128`, `"memory_size": 128.0`},
	} {
		t.Run(name, func(t *testing.T) {
			edited := strings.Replace(basePlan, edit[0], edit[1], 1)
			if edited == basePlan {
				t.Fatalf("the edit %q didn't apply", edit[0])
			}
			if got := digestOf(t, edited); got == base {
				t.Errorf("digest stayed %q", got)
			}
		})
	}
}

func TestDigestDoesNotDependOnResourceOrder(t *testing.T) {
	a := `{"format_version":"1.2","resource_changes":[
	  {"address":"a","change":{"actions":["create"],"before":null,"after":{"x":1},"after_unknown":{}}},
	  {"address":"b","change":{"actions":["create"],"before":null,"after":{"x":2},"after_unknown":{}}}]}`
	b := `{"format_version":"1.2","resource_changes":[
	  {"address":"b","change":{"actions":["create"],"before":null,"after":{"x":2},"after_unknown":{}}},
	  {"address":"a","change":{"actions":["create"],"before":null,"after":{"x":1},"after_unknown":{}}}]}`
	if digestOf(t, a) != digestOf(t, b) {
		t.Fatal("the same changes in another order gave another digest")
	}
}

func TestNoChangesHasItsOwnDigest(t *testing.T) {
	none := digestOf(t, `{"format_version":"1.2"}`)
	if none == digestOf(t, basePlan) {
		t.Fatal("a plan with no changes has the same digest as one with changes")
	}
	if got := digestOf(t, `{"format_version":"1.2","resource_changes":[{"address":"a","change":{"actions":["no-op"]}}]}`); got != none {
		t.Errorf("only no-ops gave %q, want the no-changes digest %q", got, none)
	}
}

func TestRefusesWhatIsNotAPlan(t *testing.T) {
	for name, input := range map[string]string{
		"empty":           "",
		"not JSON":        "Error: no plan",
		"an array":        "[]",
		"an empty object": "{}",
		"two values":      `{"format_version":"1.2"} {"format_version":"1.2"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(strings.NewReader(input), &stdout, &stderr); code != 1 {
				t.Fatalf("exit %d, want 1", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout %q, want nothing", stdout.String())
			}
			if !strings.Contains(stderr.String(), "could not be read") {
				t.Errorf("stderr %q", stderr.String())
			}
		})
	}
}

func TestRunPrintsOnlyTheDigest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(strings.NewReader(basePlan), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if want := digestOf(t, basePlan) + "\n"; stdout.String() != want {
		t.Errorf("stdout %q, want %q", stdout.String(), want)
	}
	if strings.Contains(stdout.String(), "foundry-prod-controller") || stderr.Len() != 0 {
		t.Errorf("plan values reached the output: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}
