package app

import (
	"os"
	"strings"
)

// systemLanguage reports the OS UI language as a BCP 47-ish tag, or "" when the
// environment does not say (§8.1).
//
// This is the backstop, not the answer. The webview knows better: it reports
// the language the user actually sees in this window, where the environment
// reports what the process was launched with. A GUI app on macOS is routinely
// launched with no LANG at all, and on Windows these variables are simply not
// a thing — which is why the frontend asks navigator first and only falls back
// here.
//
// "C" and "POSIX" are not languages. They mean "no locale", and treating them
// as a tag would resolve to Korean by the same rule that catches "de" — right
// by accident, and wrong the moment the default changes.
func systemLanguage() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		v := strings.TrimSpace(os.Getenv(key))
		// The codeset is not part of the language: "C.UTF-8" is still "C", and
		// comparing the whole string would let it through as a tag.
		if base, _, ok := strings.Cut(v, "."); ok {
			v = base
		}
		if v == "" || v == "C" || v == "POSIX" {
			continue
		}
		return v
	}
	return ""
}
