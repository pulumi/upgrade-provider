package step

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Step struct {
	Name    string          `json:"name"`
	Inputs  json.RawMessage `json:"inputs"`
	Outputs json.RawMessage `json:"outputs"`
	Impure  bool            `json:"impure,omitempty"`
}

func (s *Step) String() string {
	if s == nil {
		return "step.Step(nil)"
	}
	type Step struct {
		Name    string
		Inputs  string
		Outputs string
		Impure  bool
	}
	return fmt.Sprintf("%#v", Step{
		Name:    s.Name,
		Inputs:  string(s.Inputs),
		Outputs: string(s.Outputs),
		Impure:  s.Impure,
	})
}

type Replay struct {
	Violations []Violation
	t          *testing.T
	pending    []struct {
		name   string
		inputs json.RawMessage
		index  int
	}
	next  int // The index of the next step the replay should contain
	steps []Step
}

type Violation struct {
	Expected *Step
	Found    *Step
}

func (v Violation) String() string {
	return fmt.Sprintf("step.Violation{ Expected: %s, Found: %s }", v.Expected, v.Found)
}

func NewReplay(t *testing.T, source []byte) *Replay {
	var s struct {
		Steps []Step `json:"steps"`
	}
	err := json.Unmarshal(source, &s)
	require.NoError(t, err)
	return &Replay{steps: s.Steps, t: t}
}

func (r *Replay) Enter(info StepInfo) error {
	current := r.next
	r.t.Logf("Searching for step: %q (from step %d)", info.Name(), current)
	for {
		// We are attempting to find the next step, but there is no next step.
		if len(r.steps) <= current {
			current = -1 // -1 indicates not found
			break
		}
		// We have found what looks like the correct step
		if r.steps[current].Name == info.Name() {
			r.t.Logf("Found expected next step: %q", info.Name())
			break
		}
		current++
	}
	inputBytes, err := json.Marshal(info.Inputs())
	if err != nil {
		return err
	}
	r.pending = append(r.pending, struct {
		name   string
		inputs json.RawMessage
		index  int
	}{info.Name(), inputBytes, current})

	if current != -1 {
		// We have found a step, so move on to the next step
		r.next = current + 1

		// If a step is impure, we can't test it, so just have it return what is
		// expected.
		if r.steps[current].Impure {
			var out []any
			err := json.Unmarshal(r.steps[current].Outputs, &out)
			if err != nil {
				return fmt.Errorf("failed to unmarshal replay outputs: %w", err)
			}
			return ReturnImmediatly{Out: out}
		}
	}

	return nil
}

func (r *Replay) Exit(output []any) error {
	if len(r.pending) == 0 {
		return fmt.Errorf("internal: exit without entry")
	}
	exiting := r.pending[len(r.pending)-1]
	r.pending = r.pending[:len(r.pending)-1]

	if exiting.index == -1 {
		outputBytes, err := json.Marshal(output)
		if err != nil {
			return err
		}

		// This is a violations because we have an additional step.
		r.Violations = append(r.Violations, Violation{
			Expected: nil,
			Found: &Step{
				Name:    exiting.name,
				Inputs:  exiting.inputs,
				Outputs: outputBytes,
			},
		})
		return nil
	}

	outputBytes, err := json.Marshal(output)
	if err != nil {
		return err
	}

	expected := r.steps[exiting.index]
	if assert.JSONEqf(r.t, string(expected.Inputs), string(exiting.inputs), "%s: inputs", exiting.name) &&
		assert.JSONEqf(r.t, string(expected.Outputs), string(outputBytes), "%s: outputs", exiting.name) {
		// Inputs and outputs match: no violation
		return nil
	}

	r.Violations = append(r.Violations, Violation{
		Expected: &expected,
		Found: &Step{
			Name:    exiting.name,
			Inputs:  exiting.inputs,
			Outputs: outputBytes,
		},
	})
	return nil
}

func (r *Replay) String() string { return "replaying" }

// A special error type that indicates that a step should be skipped, substituting the
// returned value for the given output.
//
// The value of Out must be convertible to the return value type of the step it is
// replacing.
type ReturnImmediatly struct{ Out []any }

type Record struct {
	steps        []*Step
	partialSteps []*Step
}

func (r ReturnImmediatly) Error() string {
	return "a signal error for an immediate return"
}

func (r *Record) Enter(info StepInfo) error {
	result, err := json.Marshal(info.Inputs())
	if err != nil {
		return fmt.Errorf("cannot record: %w", err)
	}
	s := &Step{
		Name:   info.Name(),
		Inputs: result,
	}
	r.partialSteps = append(r.partialSteps, s)
	r.steps = append(r.steps, s)
	return nil
}

func (r *Record) Exit(output []any) error {
	topInput := r.partialSteps[len(r.partialSteps)-1]
	r.partialSteps = r.partialSteps[:len(r.partialSteps)-1]

	result, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("cannot record: %w", err)
	}
	topInput.Outputs = json.RawMessage(result)

	return nil
}

func (r *Record) String() string { return "Recording" }

func (r *Record) Marshal() []byte {
	m, err := json.MarshalIndent(struct {
		Steps []*Step `json:"steps"`
	}{Steps: r.steps}, "", "  ")
	if err != nil {
		panic(err)
	}
	return m
}

type recordKey struct{}

// Mark the calling context as an impure function.
func MarkImpure(ctx context.Context) {
	r, ok := ctx.Value(recordKey{}).(*Record)
	if !ok {
		// If we are not run in a record context, we do nothing here.
		return
	}
	current := r.partialSteps[len(r.partialSteps)-1]
	current.Impure = true
}
