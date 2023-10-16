package main

import (
	"context"
	"testing"

	"github.com/pulumi/upgrade-provider/step/v2"
	"github.com/stretchr/testify/require"
)

var runWithFile = []byte(`{
  "pipelines": [
    {
      "name": "simple",
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
          "name": "hide-secret",
          "inputs": [
            "foo secret bar\n"
          ],
          "outputs": [
            "foo [SECRET] bar\n",
            null
          ]
        },
        {
          "name": "sleep",
          "inputs": [
            3
          ],
          "outputs": [
            null
          ]
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
        },
        {
          "name": "sleep",
          "inputs": [
            4
          ],
          "outputs": [
            null
          ]
        }
      ]
    }
  ]
}`)

func TestSimple(t *testing.T) {
	replay := step.NewReplay(t, runWithFile)

	ctx := context.Background()
	err := step.PipelineCtx(step.WithEnv(ctx, replay), "simple", pipeline)
	require.NoError(t, err)
}
