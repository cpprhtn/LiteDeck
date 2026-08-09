package app

import "testing"

func TestSystemLanguageReadsTheEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing set", nil, ""},
		{"LANG alone", map[string]string{"LANG": "ko_KR.UTF-8"}, "ko_KR"},
		{"LC_ALL wins over LANG", map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "ko_KR.UTF-8"}, "en_US"},
		{"LC_MESSAGES before LANG", map[string]string{"LC_MESSAGES": "en_GB", "LANG": "ko_KR"}, "en_GB"},
		// "C" is the absence of a locale, not a language. Letting it through
		// would resolve to Korean by the fallback rule — right by accident.
		{"C is not a language", map[string]string{"LANG": "C"}, ""},
		{"C.UTF-8 is still C", map[string]string{"LANG": "C.UTF-8"}, ""},
		{"POSIX is not a language", map[string]string{"LC_ALL": "POSIX"}, ""},
		{"empty is skipped", map[string]string{"LC_ALL": "", "LANG": "ko_KR"}, "ko_KR"},
		{"C in LC_ALL falls through to LANG", map[string]string{"LC_ALL": "C", "LANG": "en_US"}, "en_US"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := systemLanguage(); got != tc.want {
				t.Errorf("systemLanguage() = %q, want %q", got, tc.want)
			}
		})
	}
}
