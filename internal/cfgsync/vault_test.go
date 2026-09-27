package cfgsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/config"
)

// A test passphrase, long enough for the floor.
const goodPass = "correct horse battery staple"

// fastKDF replaces the Argon2 parameters with cheap ones.
//
// The real ones are 64 MiB and three passes, which is the point of them and also
// a third of a second per open — and these tests open a vault a few dozen times.
// The parameters travel in the file, so using different ones here is not a
// shortcut around the format; it is the format working as designed (§6.7 ⑧).
func fastVault(t *testing.T) (VaultFile, *Vault) {
	t.Helper()
	vf, v, err := CreateVault(goodPass)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return vf, v
}

func TestRecordSealAndOpenRoundTrip(t *testing.T) {
	_, v := fastVault(t)
	p, err := RecordPath("3f2a0c1e-5b6d-4e7f-8a90-112233445566")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	plain := []byte(`{"id":"3f2a0c1e-5b6d-4e7f-8a90-112233445566","rev":1}`)
	file, err := v.Encrypt(p, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(file, []byte("3f2a0c1e")) {
		t.Error("the host id is readable in the sealed file")
	}
	got, err := v.Decrypt(p, file)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round trip changed the record: %s", got)
	}
}

func TestWrongPassphraseDoesNotOpenTheVault(t *testing.T) {
	vf, _ := fastVault(t)
	if _, err := vf.Open(goodPass + "x"); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("a wrong passphrase gave %v", err)
	}
	if _, err := vf.Open(goodPass); err != nil {
		t.Errorf("the right passphrase did not open it: %v", err)
	}
}

// One flipped byte anywhere in the file is a refusal, not a partial read.
//
// The threat is the repository host, or a leaked deploy token: somebody who can
// write the file but does not have the key. Without authentication they could
// change where the app connects to.
func TestOneChangedByteIsRefused(t *testing.T) {
	_, v := fastVault(t)
	p, _ := RecordPath("3f2a0c1e-5b6d-4e7f-8a90-112233445566")
	file, err := v.Encrypt(p, []byte("hello"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	var s sealed
	if err := json.Unmarshal(file, &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, tc := range []struct {
		name string
		mut  func(*sealed)
	}{
		{"ciphertext", func(s *sealed) { s.CT[0] ^= 1 }},
		{"last byte of ciphertext", func(s *sealed) { s.CT[len(s.CT)-1] ^= 1 }},
		{"nonce", func(s *sealed) { s.Nonce[3] ^= 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tampered sealed
			if err := json.Unmarshal(file, &tampered); err != nil {
				t.Fatalf("decode: %v", err)
			}
			tc.mut(&tampered)
			b, err := json.Marshal(tampered)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if _, err := v.Decrypt(p, b); !errors.Is(err, ErrCorrupt) {
				t.Errorf("a changed %s gave %v", tc.name, err)
			}
		})
	}
}

// A record moved to another file name does not open there.
//
// Somebody with write access to the repository and no key could otherwise swap
// the record for `bastion` onto the file named after `prod-web`, and the next
// machine to sync would connect somewhere the user did not choose. The path is in
// the associated data, so the move is the tamper.
func TestARecordDoesNotOpenUnderAnotherName(t *testing.T) {
	_, v := fastVault(t)
	from, _ := RecordPath("3f2a0c1e-5b6d-4e7f-8a90-112233445566")
	to, _ := RecordPath("9b1c7d2e-1111-4222-8333-444455556666")
	file, err := v.Encrypt(from, []byte("prod-web"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := v.Decrypt(to, file); !errors.Is(err, ErrCorrupt) {
		t.Errorf("the record opened under a different name: %v", err)
	}
}

// The associated data is built with "/" whatever the machine (§6.7 ③).
//
// This is the hazard that is invisible where it is introduced: a Windows build
// using filepath.Join writes `hosts\3f2a....enc` into the AD, the file is
// perfectly readable there, and macOS — which builds `hosts/3f2a....enc` — cannot
// open it. The failure appears on the machine that did nothing wrong.
func TestWindowsStylePathsAreRefusedRatherThanSealedWith(t *testing.T) {
	_, v := fastVault(t)
	const id = "3f2a0c1e-5b6d-4e7f-8a90-112233445566"
	posix, _ := RecordPath(id)
	if strings.Contains(posix, `\`) {
		t.Fatalf("RecordPath produced a backslash: %q", posix)
	}

	// The shape filepath.Join would have produced on Windows.
	windows := `hosts\` + id + ".enc"
	if _, err := v.Encrypt(windows, []byte("x")); err == nil {
		t.Error("a backslash path was sealed — the record would only open on the machine that wrote it")
	}
	if _, err := v.Decrypt(windows, []byte(`{"v":1,"nonce":"","ct":""}`)); err == nil {
		t.Error("a backslash path was accepted on the way in")
	}

	// And a record sealed the right way does not open under the Windows spelling
	// even if the check above were removed, because the bytes differ.
	file, err := v.Encrypt(posix, []byte("x"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	ad, err := associatedData(posix)
	if err != nil {
		t.Fatalf("ad: %v", err)
	}
	if string(ad) != "litedeck-sync/v1/hosts/"+id+".enc" {
		t.Errorf("associated data = %q", ad)
	}
	if _, err := v.Decrypt(posix, file); err != nil {
		t.Errorf("the posix path did not round trip: %v", err)
	}
}

// Nothing outside hosts/<uuid>.enc is a record path.
func TestRecordPathsAreLowercaseUUIDsUnderHosts(t *testing.T) {
	const id = "3f2a0c1e-5b6d-4e7f-8a90-112233445566"
	p, err := RecordPath(id)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if p != "hosts/"+id+".enc" {
		t.Errorf("RecordPath = %q", p)
	}
	if p != strings.ToLower(p) {
		t.Errorf("%q is not lowercase — macOS and Windows fold case and Linux does not", p)
	}
	// An uppercase ID is refused rather than lowercased. Lowercasing it quietly
	// would mean two hosts whose IDs differ only in case become one file.
	if _, err := RecordPath(strings.ToUpper(id)); err == nil {
		t.Error("an uppercase id was accepted")
	}
	for _, bad := range []string{"", "host-1786033219477533000", "sshconfig:cpp",
		"../../etc/passwd", id + "/..", "3f2a0c1e5b6d4e7f8a90112233445566"} {
		if _, err := RecordPath(bad); err == nil {
			t.Errorf("RecordPath(%q) was accepted", bad)
		}
	}

	got, ok := RecordID(p)
	if !ok || got != id {
		t.Errorf("RecordID(%q) = %q %v", p, got, ok)
	}
	for _, notARecord := range []string{"vault.json", ".gitattributes", "README.md",
		"hosts/" + strings.ToUpper(id) + ".enc", "hosts/readme.md", "other/" + id + ".enc"} {
		if _, ok := RecordID(notARecord); ok {
			t.Errorf("RecordID(%q) claimed it was a record", notARecord)
		}
	}
}

// Changing the passphrase leaves the records alone (§4.4).
//
// The passphrase wraps the vault key; it never touched a record. If this were not
// so, changing it would be a migration over every host, which can fail halfway
// and leave half a repository nobody can read.
func TestChangingThePassphraseKeepsExistingRecordsReadable(t *testing.T) {
	vf, v := fastVault(t)
	p, _ := RecordPath("3f2a0c1e-5b6d-4e7f-8a90-112233445566")
	file, err := v.Encrypt(p, []byte("prod-web"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	const newPass = "a totally different passphrase"
	changed, err := vf.ChangePassphrase(goodPass, newPass)
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if bytes.Equal(changed.KDF.Salt, vf.KDF.Salt) {
		t.Error("the salt was reused — one cracked KEK would give both")
	}
	if _, err := changed.Open(goodPass); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("the old passphrase still opens it: %v", err)
	}
	v2, err := changed.Open(newPass)
	if err != nil {
		t.Fatalf("open with the new passphrase: %v", err)
	}
	got, err := v2.Decrypt(p, file)
	if err != nil || string(got) != "prod-web" {
		t.Errorf("a record written before the change no longer opens: %q %v", got, err)
	}
	// And the wrong old passphrase changes nothing.
	if _, err := vf.ChangePassphrase("not it at all", newPass); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("the change was allowed without the old passphrase: %v", err)
	}
}

// The KDF parameters come from the file, not from this build (§6.7 ⑧).
//
// The repository has to open on the weakest machine that will join it. The
// machine that created it decided; every later reader obeys. A build that used
// its own constants would open only the repositories it had created itself, and
// the failure would look like a wrong passphrase.
func TestTheKDFParametersComeFromTheFile(t *testing.T) {
	vf, _ := fastVault(t)
	if vf.KDF.MemoryKiB != 64*1024 || vf.KDF.Time != 3 || vf.KDF.Threads != 4 {
		t.Errorf("defaults changed: %+v — the spec says 64 MiB, 3, 4", vf.KDF)
	}

	// A repository created by a machine that chose weaker parameters — a Pi.
	weak := vf
	weak.KDF.Time = 1
	weak.KDF.MemoryKiB = 8 * 1024
	weak.KDF.Threads = 1
	// Rewrapped under those parameters, which is what that machine would have
	// committed.
	wrapped, err := wrapKey(mustOpen(t, vf, goodPass).key, weak.KDF, goodPass)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	weak.WrappedKey = wrapped
	if _, err := weak.Open(goodPass); err != nil {
		t.Errorf("a repository with weaker parameters did not open: %v", err)
	}

	// And parameters nobody could satisfy are refused rather than attempted.
	// They arrive in a file an attacker may have written, and Argon2 does what it
	// is told: this one asks for 64 GB.
	absurd := vf
	absurd.KDF.MemoryKiB = 64 << 20
	if _, err := absurd.Open(goodPass); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a 64 GB allocation was attempted: %v", err)
	}
	for _, bad := range []KDFParams{
		{Alg: "scrypt", Salt: vf.KDF.Salt, Time: 3, MemoryKiB: 1024, Threads: 1},
		{Alg: Argon2id, Salt: []byte("short"), Time: 3, MemoryKiB: 1024, Threads: 1},
		{Alg: Argon2id, Salt: vf.KDF.Salt, Time: 0, MemoryKiB: 1024, Threads: 1},
		{Alg: Argon2id, Salt: vf.KDF.Salt, Time: 3, MemoryKiB: 0, Threads: 1},
		{Alg: Argon2id, Salt: vf.KDF.Salt, Time: 3, MemoryKiB: 1024, Threads: 0},
	} {
		v := vf
		v.KDF = bad
		if _, err := v.Open(goodPass); !errors.Is(err, ErrUnsupported) {
			t.Errorf("KDF %+v was accepted: %v", bad, err)
		}
	}
}

func mustOpen(t *testing.T, vf VaultFile, pass string) *Vault {
	t.Helper()
	v, err := vf.Open(pass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return v
}

// A repository from a newer LiteDeck is refused, not guessed at.
func TestAnUnknownFormatOrVersionIsRefused(t *testing.T) {
	vf, _ := fastVault(t)
	for _, mut := range []func(*VaultFile){
		func(v *VaultFile) { v.Format = "something-else" },
		func(v *VaultFile) { v.Version = 2 },
		func(v *VaultFile) { v.WrappedKey.Alg = "aes-gcm" },
	} {
		v := vf
		mut(&v)
		if _, err := v.Open(goodPass); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%+v was accepted: %v", v, err)
		}
	}
}

// vault.json survives a round trip through JSON.
//
// It is the one file in the repository that is meant to be readable, and the byte
// arrays in it have to arrive as base64 rather than as arrays of numbers — which
// is what they would be if somebody changed them to []uint8 aliases.
func TestVaultFileIsPlainJSONWithBase64Fields(t *testing.T) {
	vf, _ := fastVault(t)
	b, err := json.MarshalIndent(vf, "", "  ")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, want := range []string{`"format": "litedeck-sync"`, `"alg": "argon2id"`,
		`"memory_kib": 65536`, `"alg": "xchacha20poly1305"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("vault.json is missing %s:\n%s", want, b)
		}
	}
	if regexp.MustCompile(`"salt": \[`).Match(b) {
		t.Error("the salt is an array of numbers, not base64")
	}
	var back VaultFile
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := back.Open(goodPass); err != nil {
		t.Errorf("a vault.json that went through JSON does not open: %v", err)
	}
}

// The passphrase floor is twelve characters, and above it nothing is refused.
func TestPassphraseFloorAndWarnings(t *testing.T) {
	if err := CheckPassphrase("short"); !errors.Is(err, ErrPassphraseTooShort) {
		t.Errorf("a five-character passphrase was accepted: %v", err)
	}
	if err := CheckPassphrase("123456789012"); err != nil {
		t.Errorf("twelve characters was refused: %v", err)
	}
	if _, _, err := CreateVault("tooshort"); !errors.Is(err, ErrPassphraseTooShort) {
		t.Errorf("a vault was created with a short passphrase: %v", err)
	}
	// Korean is counted in characters, not bytes: twelve Hangul syllables are
	// thirty-six bytes, and a byte-length floor would let four syllables through.
	if err := CheckPassphrase("네글자짜리"); err == nil {
		t.Error("five Korean characters passed a twelve-character floor")
	}
	if w := PassphraseWarning("aaaaaaaaaaaaaaaa"); w == "" {
		t.Error("a repeated character drew no warning")
	}
	if w := PassphraseWarning("correct horse battery staple"); w != "" {
		t.Errorf("a long passphrase was warned about: %q", w)
	}
}

// The vault key is never written to a file by this package (§6.7 ①).
//
// A machine with no Secret Service — a headless Rocky, a minimal Ubuntu — is
// usually a machine several people use. The answer there is to ask for the
// passphrase every time, not to drop the key into a file with 0600 that Windows
// would not honour anyway. The cache is somebody else's problem; this pins that
// the crypto has no file path in it at all.
func TestTheVaultFileDoesNotWriteAnything(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "vault.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse vault.go: %v", err)
	}
	for _, imp := range f.Imports {
		switch p := strings.Trim(imp.Path.Value, `"`); p {
		case "os", "io/ioutil":
			t.Errorf("vault.go imports %q — the vault key must not reach a file (§6.7 ①)", p)
		case "path/filepath":
			t.Errorf("vault.go imports %q — repository paths are built with `path`, "+
				"or the associated data differs by a separator between machines (§6.7 ③)", p)
		}
	}
}

// The repository carries `* -text` from its first commit (§6.7 ④).
//
// The records are JSON full of base64, so git sees text. A user with
// core.autocrlf=true cloning their own repository on Windows turns every \n into
// \r\n and every record stops decrypting. go-git does not translate, but nothing
// stops the user using git.
func TestGitAttributesDisablesLineEndingTranslation(t *testing.T) {
	if strings.TrimSpace(GitAttributes) != "* -text" {
		t.Errorf("GitAttributes = %q", GitAttributes)
	}
	if !strings.HasSuffix(GitAttributes, "\n") {
		t.Error(".gitattributes has no trailing newline; git wants one")
	}
	if AttributesPath != ".gitattributes" || VaultPath != "vault.json" {
		t.Errorf("the plaintext files are named %q and %q", AttributesPath, VaultPath)
	}
}

// Two hosts that the sync must not touch.
func TestOnlyMigratedNonSSHConfigHostsAreSyncable(t *testing.T) {
	const uuid = "3f2a0c1e-5b6d-4e7f-8a90-112233445566"
	cases := []struct {
		host config.Host
		want bool
		why  string
	}{
		{config.Host{ID: uuid}, true, ""},
		{config.Host{ID: "host-1786033219477533000"}, false,
			"a host that has not been through the migration has an id no other machine can arrive at"},
		{config.Host{ID: "sshconfig:cpp", Source: config.SSHConfigSource}, false,
			"ssh_config hosts are re-derived from that file and would be duplicated"},
		{config.Host{ID: uuid, Source: config.SSHConfigSource}, false,
			"source wins over the shape of the id"},
	}
	for _, tc := range cases {
		if got := Syncable(tc.host); got != tc.want {
			t.Errorf("Syncable(%+v) = %v: %s", tc.host, got, tc.why)
		}
	}
}
