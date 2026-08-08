package windowspowershell

// PowerShell 5.1 does not write plain text to stderr when its output is not a
// console — which over SSH it never is. It writes CLIXML:
//
//	#< CLIXML
//	<Objs Version="1.1.0.1" xmlns="..."><S S="Error">Get-NoSuchCmdlet : ...
//	_x000D__x000A_</S><S S="Error"> ... </S></Objs>
//
// LiteDeck shows remote stderr to the user verbatim, on the grounds that the
// original text is more useful than a paraphrase. Verbatim CLIXML is not: it is
// XML tags, a schema URL and _x000D__x000A_ escapes wrapped around the one
// sentence the user needs. So the sentence is extracted and the envelope thrown
// away — the text itself is still never rewritten.
//
// Only the pieces that appear in real captures are handled, and testdata/windows
// holds the captures they were written against.

import (
	"encoding/xml"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// clixmlMarker is the sentinel PowerShell writes before the XML document.
const clixmlMarker = "#< CLIXML"

// IsCLIXML reports whether output is a CLIXML envelope rather than plain text.
func IsCLIXML(b []byte) bool {
	return strings.Contains(string(b), clixmlMarker)
}

// escapedChar matches PowerShell's _xHHHH_ escapes. CLIXML encodes control
// characters this way, so a multi-line error arrives as a single line littered
// with _x000D__x000A_ where the newlines were.
var escapedChar = regexp.MustCompile(`_x([0-9A-Fa-f]{4})_`)

// DecodeCLIXML extracts the human-readable text from a CLIXML stream.
//
// Input that is not CLIXML is returned unchanged, so callers can pass any stderr
// through this without checking first. Malformed XML is also returned unchanged
// rather than dropped: a stream we cannot parse is exactly when the raw bytes are
// worth showing.
func DecodeCLIXML(b []byte) string {
	raw := string(b)
	if !strings.Contains(raw, clixmlMarker) {
		return raw
	}

	// Anything before the marker is another program's output on the same stream
	// and is kept — an ssh advisory, for instance.
	prefix, doc, _ := strings.Cut(raw, clixmlMarker)

	type entry struct {
		Stream string `xml:"S,attr"`
		Text   string `xml:",chardata"`
	}
	var objs struct {
		Strings []entry `xml:"S"`
	}

	// The document is UTF-8 in practice, but a capture taken without the
	// encoding prelude arrives in the OEM codepage and will not parse. Returning
	// the original bytes beats returning nothing.
	if err := xml.Unmarshal([]byte(doc), &objs); err != nil || !utf8.ValidString(doc) {
		return raw
	}

	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(prefix))
	if sb.Len() > 0 {
		sb.WriteByte('\n')
	}
	for _, e := range objs.Strings {
		// Progress and verbose records also travel here. Only Error and Warning
		// are worth surfacing; progress records are UI chrome for a console that
		// does not exist on this end.
		if e.Stream != "" && e.Stream != "Error" && e.Stream != "Warning" {
			continue
		}
		sb.WriteString(unescapeCLIXML(e.Text))
	}

	out := strings.TrimSpace(sb.String())
	if out == "" {
		// Nothing extractable — a stream of progress records, say. The caller
		// needs to be able to tell "no error text" from "an error we mangled",
		// and an empty string says the first.
		return ""
	}
	return out
}

// unescapeCLIXML turns _xHHHH_ back into the characters it stands for.
func unescapeCLIXML(s string) string {
	return escapedChar.ReplaceAllStringFunc(s, func(m string) string {
		n, err := strconv.ParseUint(m[2:len(m)-1], 16, 32)
		if err != nil {
			return m
		}
		return string(rune(n))
	})
}

// ErrorText returns stderr as something worth putting on screen: CLIXML decoded
// when it is CLIXML, the bytes as they came otherwise.
//
// PowerShell repeats the failing command, a caret ruler under it, and the
// CategoryInfo/FullyQualifiedErrorId pair after the message. The first line is
// the message; the rest is context that is only useful in a bug report, so the
// leading line is returned first and the remainder kept behind it.
func ErrorText(stderr []byte) string {
	decoded := DecodeCLIXML(stderr)
	if decoded == "" {
		return ""
	}
	lines := strings.Split(decoded, "\n")
	var kept []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			continue
		// The caret ruler marks a column in a command line the user never typed
		// — ours, with the prelude in front of it — so its position is
		// meaningless here.
		case strings.HasPrefix(t, "+") && strings.Trim(t, "+~ ") == "":
			continue
		// "위치 줄:1 문자:117" / "At line:1 char:117" — the offset is into the
		// generated script, not anything the user can look at.
		case strings.HasPrefix(t, "위치 줄:"), strings.HasPrefix(t, "At line:"):
			continue
		}
		kept = append(kept, t)
	}
	return strings.Join(kept, "\n")
}
