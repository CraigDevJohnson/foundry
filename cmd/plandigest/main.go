// Command plandigest prints a saved OpenTofu plan's digest, read from
// `tofu show -json` on stdin.
//
// The digest is the plan's identity for the Deploy workflow (ADR 0007): the
// plan run posts it, and an apply run applies only a fresh plan with the
// digest Craig copied from the plan he read. It covers what the apply would
// do: each changed resource's actions, the object it acts on (its id and ARN
// before the change, so a delete or update can't land on a different object
// than the one planned), its planned values, any import or move into its
// address, and each changed output. A resource whose only change is an import
// or a move counts as changed: either one changes the state. It
// leaves out the rest of what describes the present, such as unchanged
// resources and the other values a change starts from (among them whether
// the host runs), so starting or stopping Foundry between the two runs
// doesn't change it unless the plan changes the host itself. Only the digest
// leaves this command; the plan's values never reach the log.
package main

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

const digestLength = 16

// The attributes that name the object a change acts on.
var identity = []string{"id", "arn"}

type savedPlan struct {
	FormatVersion   string            `json:"format_version"`
	ResourceChanges []resourceChange  `json:"resource_changes"`
	OutputChanges   map[string]change `json:"output_changes"`
}

type resourceChange struct {
	Address         string `json:"address"`
	PreviousAddress string `json:"previous_address"`
	Deposed         string `json:"deposed"`
	Change          change `json:"change"`
}

// changesState says whether applying this resource's change does anything:
// an action, or an import or move that leaves the object as it is.
func (rc resourceChange) changesState() bool {
	return !rc.Change.noOp() || rc.Change.Importing != nil || (rc.PreviousAddress != "" && rc.PreviousAddress != rc.Address)
}

type change struct {
	Actions      []string `json:"actions"`
	Before       any      `json:"before"`
	After        any      `json:"after"`
	AfterUnknown any      `json:"after_unknown"`
	ReplacePaths any      `json:"replace_paths"`
	Importing    any      `json:"importing"`
}

func (c change) noOp() bool { return slices.Equal(c.Actions, []string{"no-op"}) }

type digestResource struct {
	Address         string         `json:"address"`
	PreviousAddress string         `json:"previous_address,omitempty"`
	Deposed         string         `json:"deposed,omitempty"`
	Actions         []string       `json:"actions"`
	ActsOn          map[string]any `json:"acts_on"`
	After           any            `json:"after"`
	AfterUnknown    any            `json:"after_unknown"`
	ReplacePaths    any            `json:"replace_paths"`
	Importing       any            `json:"importing,omitempty"`
}

type digestOutput struct {
	Actions      []string `json:"actions"`
	After        any      `json:"after"`
	AfterUnknown any      `json:"after_unknown"`
}

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

func run(stdin io.Reader, stdout, stderr io.Writer) int {
	d, err := digest(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "the saved plan could not be read:", err)
		return 1
	}
	fmt.Fprintln(stdout, d)
	return 0
}

func digest(r io.Reader) (string, error) {
	dec := json.NewDecoder(r)
	// Numbers stay as written, so a large one can't round to another.
	dec.UseNumber()
	var plan savedPlan
	if err := dec.Decode(&plan); err != nil {
		return "", err
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", errors.New("more than one JSON value")
	}
	// An empty object, or anything but `tofu show -json` output, has no
	// format version; refusing it keeps a failed show from digesting as
	// "no changes".
	if plan.FormatVersion == "" {
		return "", errors.New("no format_version: not a plan")
	}

	resources := []digestResource{}
	for _, rc := range plan.ResourceChanges {
		if !rc.changesState() {
			continue
		}
		var actsOn map[string]any
		if before, ok := rc.Change.Before.(map[string]any); ok {
			actsOn = make(map[string]any, len(identity))
			for _, key := range identity {
				actsOn[key] = before[key]
			}
		}
		resources = append(resources, digestResource{
			Address:         rc.Address,
			PreviousAddress: rc.PreviousAddress,
			Deposed:         rc.Deposed,
			Actions:         rc.Change.Actions,
			ActsOn:          actsOn,
			After:           rc.Change.After,
			AfterUnknown:    rc.Change.AfterUnknown,
			ReplacePaths:    rc.Change.ReplacePaths,
			Importing:       rc.Change.Importing,
		})
	}
	// A deposed object shares its address with the current one.
	slices.SortStableFunc(resources, func(a, b digestResource) int {
		return cmp.Or(strings.Compare(a.Address, b.Address), strings.Compare(a.Deposed, b.Deposed))
	})

	outputs := map[string]digestOutput{}
	for name, oc := range plan.OutputChanges {
		if oc.noOp() {
			continue
		}
		outputs[name] = digestOutput{Actions: oc.Actions, After: oc.After, AfterUnknown: oc.AfterUnknown}
	}

	// json.Marshal writes map keys in order, so the same plan always gives
	// the same bytes.
	var canonical bytes.Buffer
	if err := json.NewEncoder(&canonical).Encode(struct {
		Resources []digestResource        `json:"resources"`
		Outputs   map[string]digestOutput `json:"outputs"`
	}{resources, outputs}); err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical.Bytes())
	return hex.EncodeToString(sum[:])[:digestLength], nil
}
