package console

import (
	"fmt"
	"os"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

func Info(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("\n%s%s%s%s\n", colorBold, colorCyan, msg, colorReset)
}

func Step(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("    %s\n", msg)
}

func Pass(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("  %s%s PASS %s %s\n", colorBold, colorGreen, colorReset, msg)
}

func Fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("  %s%s FAIL %s %s\n", colorBold, colorRed, colorReset, msg)
}

func Warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("  %s%s WARN %s %s\n", colorBold, colorYellow, colorReset, msg)
}

func Completed(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

func Fatal(err error) {
	fmt.Fprintf(os.Stderr, "%s%sError: %v%s\n", colorBold, colorRed, err, colorReset)
	os.Exit(1)
}
