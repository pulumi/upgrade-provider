package main

import (
	"context"
	"errors"
	"fmt"
	"go/build"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "upgrade-provider",
	Short: "upgrade-provider automatics the process of upgrading a TF-bridged provider",
	Args:  cobra.ExactArgs(1),
	Run: func(_ *cobra.Command, args []string) {
		gopath, ok := os.LookupEnv("GOPATH")
		if !ok {
			gopath = build.Default.GOPATH
		}
		context := Context{
			Context: context.Background(),
			GoPath:  gopath,
		}

		err := UpgradeProvider(context, args[0])
		if errors.Is(err, ErrHandled) {
			os.Exit(1)
		}
		if err != nil {
			fmt.Printf("error: %s\n", err.Error())
			os.Exit(1)
		}
	},
}
