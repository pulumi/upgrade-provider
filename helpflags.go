package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// installDefaultValueHelp customizes cmd's usage template so that `--help`
// always shows a flag's default value inline (e.g. `--dry-run=false`),
// including for flags whose default is the "zero value" of their type
// (such as boolean flags defaulting to false). By default, pflag/cobra
// suppress the default value annotation for zero values, which hides
// useful information like whether a boolean flag defaults to true or
// false.
func installDefaultValueHelp(cmd *cobra.Command) {
	cobra.AddTemplateFunc("flagUsages", flagUsagesWithDefaults)

	usageTemplate := strings.NewReplacer(
		"{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}",
		"{{flagUsages .LocalFlags | trimTrailingWhitespaces}}",
		"{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}",
		"{{flagUsages .InheritedFlags | trimTrailingWhitespaces}}",
	).Replace(cmd.UsageTemplate())
	cmd.SetUsageTemplate(usageTemplate)
}

// isCobraOwnedFlag reports whether flag was implicitly added by cobra itself
// (namely `--help`/`-h` and `--version`), as opposed to a flag declared by
// upgrade-provider. Cobra's own flags are left in their standard format,
// since their default value is never interesting.
func isCobraOwnedFlag(flag *pflag.Flag) bool {
	return len(flag.Annotations[cobra.FlagSetByCobraAnnotation]) > 0
}

// boolFlagValue matches pflag's own (unexported) interface for detecting
// boolean flags, letting us identify them the same way pflag does instead of
// comparing flag.Value.Type() to the magic string "bool".
type boolFlagValue interface {
	IsBoolFlag() bool
}

// flagDefaultIsZero reports whether flag's default value is its type's zero
// value. Unlike pflag's internal (unexported) equivalent, boolean flags are
// never considered zero here so that their default is always displayed.
func flagDefaultIsZero(flag *pflag.Flag) bool {
	switch {
	case flag.Value.Type() == "duration":
		// Beginning in Go 1.7, duration zero values are "0s" rather than "0".
		return flag.DefValue == "0" || flag.DefValue == "0s"
	case flag.DefValue == "[]":
		// Every pflag slice type (stringSlice, intSlice, etc.) implements
		// this public interface and renders its default as "[]" when empty.
		if _, ok := flag.Value.(pflag.SliceValue); ok {
			return true
		}
	}

	if _, ok := flag.Value.(boolFlagValue); ok {
		return false
	}

	// Every other pflag type (string, numeric, ip, etc.) renders its zero
	// value using one of these strings.
	switch flag.DefValue {
	case "false", "<nil>", "", "0":
		return true
	}
	return false
}

// formatFlagDefault returns the default value to display for flag, along
// with whether it should be displayed at all.
func formatFlagDefault(flag *pflag.Flag) (string, bool) {
	if flagDefaultIsZero(flag) {
		return "", false
	}
	def := flag.DefValue
	if _, ok := flag.Value.(pflag.SliceValue); ok {
		def = strings.TrimSuffix(strings.TrimPrefix(def, "["), "]")
	}
	if def == "" {
		return "", false
	}
	return def, true
}

// flagUsagesWithDefaults renders flags similarly to
// (*pflag.FlagSet).FlagUsages, except that default values are inlined
// directly after the flag name (and type, if any) using `=`, e.g.
// `--dry-run=false`, rather than appended to the end of the usage text.
// Default values are shown even when they are the type's zero value
// (in particular, for boolean flags).
func flagUsagesWithDefaults(flags *pflag.FlagSet) string {
	var buf strings.Builder

	lines := make([]string, 0)
	maxlen := 0
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden {
			return
		}

		var line string
		if flag.Shorthand != "" && flag.ShorthandDeprecated == "" {
			line = fmt.Sprintf("  -%s, --%s", flag.Shorthand, flag.Name)
		} else {
			line = fmt.Sprintf("      --%s", flag.Name)
		}

		varname, usage := pflag.UnquoteUsage(flag)
		if varname != "" {
			line += " " + varname
		}

		if !isCobraOwnedFlag(flag) {
			if def, ok := formatFlagDefault(flag); ok {
				line += "=" + def
			}
		}

		if flag.NoOptDefVal != "" {
			switch flag.Value.Type() {
			case "string":
				line += fmt.Sprintf("[=%q]", flag.NoOptDefVal)
			case "bool":
				if flag.NoOptDefVal != "true" {
					line += fmt.Sprintf("[=%s]", flag.NoOptDefVal)
				}
			case "count":
				if flag.NoOptDefVal != "+1" {
					line += fmt.Sprintf("[=%s]", flag.NoOptDefVal)
				}
			default:
				line += fmt.Sprintf("[=%s]", flag.NoOptDefVal)
			}
		}

		// This special character will be replaced with spacing once the
		// correct alignment is calculated.
		line += "\x00"
		if len(line) > maxlen {
			maxlen = len(line)
		}

		line += usage
		if len(flag.Deprecated) != 0 {
			line += fmt.Sprintf(" (DEPRECATED: %s)", flag.Deprecated)
		}

		lines = append(lines, line)
	})

	for _, line := range lines {
		sidx := strings.Index(line, "\x00")
		spacing := strings.Repeat(" ", maxlen-sidx)
		// maxlen + 2 comes from + 1 for the \x00 and + 1 for the
		// (deliberate) off-by-one in maxlen-sidx.
		indent := strings.Repeat(" ", maxlen+2)
		rest := strings.ReplaceAll(line[sidx+1:], "\n", "\n"+indent)
		fmt.Fprintln(&buf, line[:sidx], spacing, rest)
	}

	return buf.String()
}
