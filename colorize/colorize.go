package colorize

const (
	esc   = "\u001B["
	bold  = esc + "1m"
	warn  = bold + esc + "33m"
	reset = esc + "m"
)

func Bold(s string) string {
	return bold + s + reset
}

func Warn(s string) string {
	return warn + s + reset
}
