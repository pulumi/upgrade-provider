package main

import (
	"context"
	"testing"

	"github.com/pulumi/upgrade-provider/step/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var runWithFile = []byte(`{
  "steps": [
    {
      "name": "input.txt",
      "inputs": [
        "input.txt"
      ],
      "outputs": [
        "foo secret bar\n",
        null
      ],
      "impure": true
    },
    {
      "name": "sleep",
      "inputs": [
        3
      ],
      "outputs": [
        null
      ],
      "impure": false
    },
    {
      "name": "hide-secret",
      "inputs": [
        "foo secret bar\n"
      ],
      "outputs": [
        "foo [SECRET] bar\n",
        null
      ],
      "impure": false
    },
    {
      "name": "sleep",
      "inputs": [
        4
      ],
      "outputs": [
        null
      ],
      "impure": false
    },
    {
      "name": "write",
      "inputs": [
        "foo [SECRET] bar\n"
      ],
      "outputs": [
        null
      ],
      "impure": true
    }
  ]
}`)

func TestSimple(t *testing.T) {
	replay, err := step.NewReplay(runWithFile)
	require.NoError(t, err)

	ctx := context.Background()
	err = step.PipelineCtx(step.WithEnv(ctx, replay), "test", pipeline)
	require.NoError(t, err)

	assert.Empty(t, replay.Violations)
}
