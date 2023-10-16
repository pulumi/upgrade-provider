package step

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
	t       *testing.T
	pending []struct {
		name   string
		inputs json.RawMessage
		index  int
	}

	pipeline int // The index of the current pipline
	r        *replayV1

	next  int // The index of the next step the replay should contain
	steps []Step
}

func NewReplay(t *testing.T, source []byte) *Replay {
	var s replayV1
	err := json.Unmarshal(source, &s)
	require.NoError(t, err)
	return &Replay{t: t, r: &s}
}

func (r *Replay) setPipeline(name string) {
	if name == "" {
		return
	}
	for i := r.pipeline; i < len(r.r.Pipelines); i++ {
		p := r.r.Pipelines[i]
		if p.Name == name {
			// We need to set up Replay for this pipline.
			if r.pipeline != i || (i == 0 && r.steps == nil) {
				r.steps = make([]Step, len(p.Steps))
				for i, v := range p.Steps {
					r.steps[i] = *v
					r.next = 0
					r.pipeline = i
				}
			}
			return
		}
	}

	r.t.Logf("Failed to find pipline %q", name)
}

func (r *Replay) Enter(info StepInfo) error {
	r.setPipeline(info.Pipeline())
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

		r.t.Logf("Expected no step, found %s", &Step{
			Name:    exiting.name,
			Inputs:  exiting.inputs,
			Outputs: outputBytes,
		})
		r.t.Fail()

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

	return nil
}

func (r *Replay) String() string { return "replaying" }

// A special error type that indicates that a step should be skipped, substituting the
// returned value for the given output.
//
// The value of Out must be convertible to the return value type of the step it is
// replacing.
type ReturnImmediatly struct{ Out []any }

type FinishRecord func() error

// WithRecord embeds a recorder in the context. The recorder will write a re-playable
// context to filePath.
//
// Example usage:
//
//	if file := os.Getenv("STEP_RECORD"); file != "" {
//		var closer io.Closer
//		ctx, closer = WithRecord(ctx, file)
//		defer func() {
//			if err := closer(); err != nil {
//				panic(err)
//			}
//		}
//	}
//
func WithRecord(ctx context.Context, filePath string) (context.Context, io.Closer) {
	record := &record{filePath: filePath}
	ctx = WithEnv(ctx, record)
	ctx = context.WithValue(ctx, recordKey{}, record)
	return ctx, record
}

type record struct {
	filePath  string
	pipelines []*replayPipeline
}

type replayPipeline struct {
	name         string
	steps        []*Step
	partialSteps []*Step
}

func (r *record) Close() error { return os.WriteFile(r.filePath, r.Marshal(), 0600) }

func (r ReturnImmediatly) Error() string {
	return "a signal error for an immediate return"
}

func (r *record) pipeline(name string) *replayPipeline {
	latest := func() *replayPipeline { return r.pipelines[len(r.pipelines)-1] }
	if name == "" {
		return latest()
	}
	if len(r.pipelines) == 0 || latest().name != name {
		p := &replayPipeline{
			name: name,
		}
		r.pipelines = append(r.pipelines, p)
		return p
	}
	return latest()
}

func (r *record) Enter(info StepInfo) error {
	result, err := json.Marshal(info.Inputs())
	if err != nil {
		return fmt.Errorf("cannot record: %w", err)
	}
	s := &Step{
		Name:   info.Name(),
		Inputs: result,
	}
	p := r.pipeline(info.Pipeline())
	p.partialSteps = append(p.partialSteps, s)
	p.steps = append(p.steps, s)
	return nil
}

func (r *record) Exit(output []any) error {
	p := r.pipeline("")
	topInput := p.partialSteps[len(p.partialSteps)-1]
	p.partialSteps = p.partialSteps[:len(p.partialSteps)-1]

	result, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("cannot record: %w", err)
	}
	topInput.Outputs = json.RawMessage(result)

	return nil
}

func (r *record) String() string { return "Recording" }

type replayV1 struct {
	Pipelines []recordV1 `json:"pipelines"`
}

type recordV1 struct {
	Name  string  `json:"name"`
	Steps []*Step `json:"steps"`
}

func (r *record) Marshal() []byte {
	pipelines := make([]recordV1, len(r.pipelines))

	for i, p := range r.pipelines {
		pipelines[i] = recordV1{p.name, p.steps}
	}

	m, err := json.MarshalIndent(replayV1{pipelines}, "", "  ")
	if err != nil {
		panic(err)
	}
	return m
}

type recordKey struct{}

// Mark the calling context as an impure function.
func MarkImpure(ctx context.Context) {
	r, ok := ctx.Value(recordKey{}).(*record)
	if !ok {
		// If we are not run in a record context, we do nothing here.
		return
	}
	p := r.pipeline("")
	current := p.partialSteps[len(p.partialSteps)-1]
	current.Impure = true
}
