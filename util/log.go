package util

import (
	"fmt"
	"os"
	"strings"

	"github.com/samber/lo"
)

func LogInfo(format string, a ...any) (n int, err error) {
	return fmt.Fprintf(os.Stderr, format, a...)
}

func LogDebug(format string, a ...any) (n int, err error) {
	if isDebug() {
		return fmt.Fprintf(os.Stderr, format, a...)
	}
	return 0, nil
}

var cacheIsDebug *bool

func isDebug() bool {
	if cacheIsDebug == nil {
		_, ok := lo.Find(os.Environ(), func(s string) bool {
			split := strings.Split(s, "=")
			return split[0] == "LSCBUILD_DEBUG" && split[1] != "" && split[1] != "0"
		})
		cacheIsDebug = &ok
	}

	return *cacheIsDebug
}
