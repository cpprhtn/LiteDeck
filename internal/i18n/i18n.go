// Package i18n translates the messages Go produces for the screen.
//
// Only user-facing text goes through here. An error a user reads is UI; an
// error only a developer reads is not, and wrapping those would double the
// catalogue for no one's benefit.
//
// # Why Korean is the key
//
// The catalogue is keyed by the Korean text itself rather than by an invented
// identifier. Two reasons: the code already reads as Korean at every one of
// these sites, so wrapping is a change of one line rather than a rename of a
// hundred; and a missing translation falls back to Korean, which is the right
// answer for this project's primary audience rather than a bare key on screen.
//
// The cost is that editing the Korean silently orphans its translation.
// TestEveryMessageIsTranslated closes that: it reads the source, finds every
// call, and fails on one the catalogue does not know.
package i18n

import (
	"fmt"
	"strings"
	"sync"
)

// Language is a supported UI language.
type Language string

const (
	Korean  Language = "ko"
	English Language = "en"
)

// Supported lists the languages the app ships, in the order a picker shows them.
var Supported = []Language{Korean, English}

var (
	mu      sync.RWMutex
	current = Korean
)

// SetLanguage switches the language for everything produced afterwards.
//
// Messages already handed to the frontend are not retranslated — an error
// banner raised before the switch keeps the wording it was raised with. That is
// accepted rather than solved: retranslating in place would mean keeping every
// message unformatted until render, and the alternative costs one stale banner.
func SetLanguage(l Language) {
	mu.Lock()
	defer mu.Unlock()
	if l == English {
		current = English
		return
	}
	current = Korean
}

// Current reports the active language.
func Current() Language {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Parse turns a BCP 47 tag into a supported language.
//
// Only the primary subtag is read: "en-US", "en_GB" and "en" are one language
// here. Anything unrecognised is Korean, which is this project's source
// language and the safer default for a tag nobody anticipated.
func Parse(tag string) Language {
	tag = strings.TrimSpace(strings.ToLower(tag))
	for _, sep := range []string{"-", "_", "."} {
		if i := strings.Index(tag, sep); i >= 0 {
			tag = tag[:i]
		}
	}
	if tag == string(English) {
		return English
	}
	return Korean
}

// T translates and formats a message.
//
// The format verbs are part of the key, so a translation must carry the same
// verbs in the same order. Where English wants a different order it uses
// explicit argument indexes (%[1]s), which is why the arguments are passed
// through rather than substituted before lookup.
func T(format string, args ...any) string {
	return fmt.Sprintf(lookup(format), args...)
}

// S translates a message that takes no arguments.
func S(text string) string { return lookup(text) }

// Errorf is fmt.Errorf with the format translated first.
//
// T would do for most of these, but not for the ones that wrap: %w is fmt's
// verb, not Sprintf's, and a message built with T and handed to errors.New
// loses the error underneath it — which §8 requires be preserved so the
// original text survives to whoever has to diagnose it.
func Errorf(format string, args ...any) error {
	return fmt.Errorf(lookup(format), args...)
}

func lookup(text string) string {
	mu.RLock()
	lang := current
	mu.RUnlock()
	if lang == Korean {
		return text
	}
	if t, ok := english[text]; ok {
		return t
	}
	// An untranslated string is shown in Korean rather than as a key. The test
	// is what keeps this from happening; this is the behaviour if it ever does.
	return text
}

// Missing reports keys the catalogue does not cover, for the test that guards
// against a Korean string being edited without its translation.
func Missing(keys []string) []string {
	var out []string
	for _, k := range keys {
		if _, ok := english[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}
