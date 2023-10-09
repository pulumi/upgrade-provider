package main

import (
	"context"
	"fmt"
	"os"
	"strings"

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
	return os.WriteFile("output.txt", []byte(content), 0600)
}

func sleep(ctx context.Context, seconds int) {
	step.Cmd(ctx, "sleep", fmt.Sprintf("%d", seconds))
}
