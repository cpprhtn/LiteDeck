// Command winscripts prints the Windows adapter's PowerShell scripts.
//
// It exists so testdata/windows/capture.sh can record what those scripts
// actually return from a real server without a second copy of them living in a
// shell file. A golden taken from a copy is a golden for a different program —
// which is exactly how the four Windows scripts ended up with parser goldens
// and no output goldens at all.
//
//	go run ./internal/adapter/cmd/winscripts shells
package main

import (
	"fmt"
	"os"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: winscripts shells|sessions|logins|security|wslprobe")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "shells":
		fmt.Print(adapter.WindowsShellsScript())
	case "sessions":
		fmt.Print(adapter.WindowsSessionsScript())
	case "logins":
		fmt.Print(adapter.WindowsLoginsScript())
	case "security":
		fmt.Print(adapter.WindowsSecurityScript())
	case "wslprobe":
		// A distribution that cannot exist, so the capture records the "not
		// installed" answer rather than waking somebody's virtual machine.
		fmt.Print(adapter.WSLProbeScript("litedeck-capture-no-such-distro", 5))
	default:
		fmt.Fprintf(os.Stderr, "unknown script %q\n", os.Args[1])
		os.Exit(2)
	}
}
