package adapter

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// No real address may sit in testdata/.
//
// Every fixture in this tree came off somebody's machine, and the interesting
// ones came off a machine on the open internet — a fail2ban ban list, an nft
// blackhole set, an OpenSSH log with three hours of a password attack in it.
// What is interesting about them is their *shape*: the map syntax, the multi-
// line element list, the pairing of a login with a disconnect. None of it needs
// a real address, and this is a public repository, so none of it keeps one.
//
// The check is here rather than in a shell script because a shell script is
// something you have to remember to run. This fails on `go test`, which is what
// runs before every commit. It has already been needed twice: the Windows script
// goldens arrived carrying a real account and a real client address, and the
// anonymiser in capture.sh only looked at quoted values so it walked past them.
//
// Allowed: the private, loopback, link-local, multicast and reserved ranges,
// which are the same on every machine; RFC 5737's three documentation blocks;
// and RFC 2544's 198.18.0.0/15, which is reserved for benchmarking and which the
// POSIX security fixtures use because they need twelve distinct /24s and the
// documentation blocks supply three.
func TestFixturesCarryNoRealAddress(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	dotted := regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

	// A dotted quad is not always an address. Both of these are version
	// strings, and both are load-bearing: the CLIXML schema version is what the
	// stderr decoder keys off, and the service description is one of the
	// records the JSON parser is written against. Listed rather than pattern-
	// matched, so a *new* one has to be looked at by a person.
	notAddresses := map[string]bool{
		"1.1.0.1":   true, // <Objs Version="1.1.0.1"> — the CLIXML envelope
		"1.71.99.0": true, // "Description":"Version: 1.71.99.0" in win32-service.out
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Not a fixture and not committed: testdata/e2e installs
			// playwright-core here, and a minified bundle is full of version
			// numbers that read as addresses.
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, m := range dotted.FindAllString(string(b), -1) {
			if seen[m] || notAddresses[m] {
				continue
			}
			seen[m] = true
			if reservedAddress(m) {
				continue
			}
			rel, _ := filepath.Rel(root, path)
			t.Errorf("testdata/%s holds %s, which is a routable address.\n"+
				"Anonymise it. testdata/windows/capture.sh does this for the Windows "+
				"captures; testdata/golden/security/provenance.txt records how the POSIX "+
				"ones were done. If it is not an address, add it to notAddresses above.", rel, m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk testdata: %v", err)
	}
}

// reservedAddress reports whether an address can never appear on the internet.
//
// Written out rather than taken from net/netip so the reason for each range is
// on the line with it — the point of the list is that somebody reading a failure
// can tell which ranges are acceptable and why.
func reservedAddress(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	var o [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 || (len(p) > 1 && p[0] == '0') {
			// Not a well-formed address at all.
			return true
		}
		o[i] = n
	}
	switch {
	case o[0] == 0: // "this network"
		return true
	case o[0] == 10: // RFC 1918
		return true
	case o[0] == 127: // loopback
		return true
	case o[0] == 100 && o[1] >= 64 && o[1] <= 127: // RFC 6598 carrier-grade NAT
		return true
	case o[0] == 169 && o[1] == 254: // link-local
		return true
	case o[0] == 172 && o[1] >= 16 && o[1] <= 31: // RFC 1918
		return true
	case o[0] == 192 && o[1] == 168: // RFC 1918
		return true
	case o[0] == 192 && o[1] == 0 && o[2] == 0: // IETF protocol assignments
		return true
	case o[0] == 192 && o[1] == 0 && o[2] == 2: // RFC 5737 TEST-NET-1
		return true
	case o[0] == 198 && (o[1] == 18 || o[1] == 19): // RFC 2544 benchmarking
		return true
	case o[0] == 198 && o[1] == 51 && o[2] == 100: // RFC 5737 TEST-NET-2
		return true
	case o[0] == 203 && o[1] == 0 && o[2] == 113: // RFC 5737 TEST-NET-3
		return true
	case o[0] >= 224: // multicast, reserved, broadcast
		return true
	}
	return false
}
