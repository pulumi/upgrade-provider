package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pulumi/upgrade-provider/step/v2"
)

func main() {
	if err := step.Pipeline("simple", pipeline); err != nil {
		os.Exit(1)
	}
}

func pipeline(ctx context.Context) {
	bytes := step.ReadFile(ctx, "input.txt")
	process := step.Call11(ctx, "hide-secret", hide, bytes)
	step.Call10E(ctx, "write", writeFile, process)
}

func hide(ctx context.Context, input string) string {
	step.Call10(ctx, "sleep", sleep, 3)
	return strings.ReplaceAll(input, "secret", "[SECRET]")
}

func writeFile(ctx context.Context, content string) error {
	step.Call10(ctx, "sleep", sleep, 4)
	step.MarkImpure(ctx)
	err := os.WriteFile("output.txt", []byte(content), 0600)
	step.SetLabel(ctx, fmt.Sprintf("%d bytes written", len(content)))
	return err
}

func sleep(ctx context.Context, seconds int) {
	for i := seconds; i > 0; i-- {
		step.SetLabel(ctx, fmt.Sprintf("%d seconds remaining", i))
		time.Sleep(time.Second * 1)
	}
	step.SetLabel(ctx, "done")
}
