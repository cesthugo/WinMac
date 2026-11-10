//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "cefwrapper is a Windows program: it runs inside Wine, next to steamwebhelper.exe")
	os.Exit(1)
}
