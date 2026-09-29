package apphost

import (
	"fmt"
	"slices"
	"strings"
)

// Container arguments (EXC-526): what the image's entrypoint is started with,
// replacing its CMD. An argument may name one of the app's variables as
// $(NAME), which the cluster fills in from the container's environment, so a
// secret passed on the command line never appears in the workload's spec.
const (
	MaxArgs          = 32
	MaxArgLength     = 1024
	MaxTotalArgBytes = 8 * 1024
)

func validateArgs(args []string) error {
	if len(args) > MaxArgs {
		return fmt.Errorf("an app may be started with at most %d arguments", MaxArgs)
	}
	total := 0
	for i, arg := range args {
		switch {
		case arg == "":
			return fmt.Errorf("argument %d is empty", i+1)
		case len(arg) > MaxArgLength:
			return fmt.Errorf("argument %d exceeds %d bytes", i+1, MaxArgLength)
		case strings.ContainsFunc(arg, isControl):
			return fmt.Errorf("argument %d contains a control character", i+1)
		}
		total += len(arg)
	}
	if total > MaxTotalArgBytes {
		return fmt.Errorf("the arguments exceed %d bytes in total", MaxTotalArgBytes)
	}
	return nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

func cloneArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	return slices.Clone(args)
}
