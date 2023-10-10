package step

import (
	"encoding/json"
	"fmt"
)

type Replay struct{ steps []Step }

type Record struct {
	steps        []*Step
	partialSteps []*Step
}

type Step struct {
	Name    string          `json:"name"`
	Inputs  json.RawMessage `json:"inputs"`
	Outputs json.RawMessage `json:"outputs"`
}

type ReturnImmediatly struct{ out []any }

func (r ReturnImmediatly) Error() string {
	return "a signal error for an immediate return"
}

func (r *Record) Enter(info StepInfo) error {
	result, err := json.Marshal(info.Inputs())
	if err != nil {
		return fmt.Errorf("cannot record: %w", err)
	}
	r.partialSteps = append(r.partialSteps, &Step{
		Name:   info.Name(),
		Inputs: result,
	})
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

	r.steps = append(r.steps, topInput)
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
