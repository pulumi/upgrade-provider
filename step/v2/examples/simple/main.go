package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pulumi/upgrade-provider/step/v2"
)

func main() {
	ctx := context.Background()
	if file := os.Getenv("STEP_RECORD"); file != "" {
		var closer io.Closer
		ctx, closer = step.WithRecord(ctx, file)
		defer func() {
			if err := closer.Close(); err != nil {
				panic(err)
			}
		}()
	}

	if err := step.PipelineCtx(ctx, "simple", pipeline); err != nil {
		os.Exit(1)
	}
}

func pipeline(ctx context.Context) {
	bytes := step.ReadFile(ctx, "input.txt")
	process := hide(ctx, bytes)
	writeFile(ctx, process)
}

var hide = step.Func11("hide-secret", func(ctx context.Context, input string) string {
	sleep(step.WithEnv(ctx, &step.EnvVar{
		Key:   "PROCESSING",
		Value: "1",
	}), 3)
	return strings.ReplaceAll(input, "secret", "[SECRET]")
})

var writeFile = step.Func10E("write", func(ctx context.Context, content string) error {
	step.MarkImpure(ctx)
	sleep(step.WithEnv(ctx, &step.EnvVar{
		Key:   "WRITING",
		Value: "1",
	}), 4)
	err := os.WriteFile("output.txt", []byte(content), 0600)
	step.SetLabel(ctx, fmt.Sprintf("%d bytes written", len(content)))
	return err
})

var sleep = step.Func10("sleep", func(ctx context.Context, seconds int) {
	for i := seconds; i > 0; i-- {
		step.SetLabel(ctx, fmt.Sprintf("%d seconds remaining", i))
		time.Sleep(time.Second * 1)
	}
	step.SetLabel(ctx, "done")
})
