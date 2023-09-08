package colorize

import "fmt"

const (
	esc   = "\u001B["
	bold  = esc + "1m"
	warn  = bold + esc + "33m"
	reset = esc + "m"
)

func Bold(s string) string { return bold + s + reset }
func Warn(s string) string { return warn + s + reset }

func Boldf(msg string, a ...any) string { return Bold(fmt.Sprintf(msg, a...)) }
func Warnf(msg string, a ...any) string { return Warn(fmt.Sprintf(msg, a...)) }
