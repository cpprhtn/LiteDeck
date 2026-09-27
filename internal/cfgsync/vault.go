// Package cfgsync carries a user's host list between their own machines through
// a git repository they own, encrypted, with no account anywhere (§1).
//
// # What is in the repository
//
// Ciphertext and nothing else. One file per host, named after its UUID, plus a
// plaintext vault.json holding the key-derivation parameters and the vault key
// wrapped with a passphrase only the user has. Whoever hosts the repository —
// GitHub, or a box in the corner of a room — learns how many hosts there are,
// how big each record is and when it last changed. That list is in the security
// document, because a threat model with an unstated gap is not one.
//
// # Why a passphrase and not a login
//
// LiteDeck asks nobody to sign up for anything (principle 4), and the moment a
// sync service holds the key it is an account. The passphrase never leaves the
// machine; what leaves is the vault key wrapped in it.
//
// # This file
//
// The envelope only: deriving the key, wrapping it, and sealing or opening a
// record. No git, no files, no UI — those are in their own files so this one can
// be read in full by somebody checking the crypto.
package cfgsync

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// Format and Version identify the repository layout (§4.3).
//
// A reader that does not know the version refuses the repository rather than
// guessing at it: the alternative is decrypting with assumptions that were not
// the ones the writer made.
const (
	Format  = "litedeck-sync"
	Version = 1
)

// adPrefix begins every associated data string (§4.4).
//
// The separator is "/" always, and the path comes from the `path` package, never
// `filepath`. On Windows filepath.Join would produce `hosts\3f2a.enc`, the AD
// would differ by one byte from the one macOS built for the same file, and AEAD
// does not forgive a byte — the record simply would not open on the other
// machine. That is the whole hazard: it is invisible on the machine that wrote
// it (§6.7 ③).
const adPrefix = Format + "/v1/"

// vaultAD is the associated data for the wrapped vault key itself.
const vaultAD = adPrefix + "vault"

// MinPassphraseLen is the shortest passphrase accepted.
//
// Twelve characters, and it is a floor rather than a rule set: a strength meter
// that refuses what somebody chose teaches them to append "1!", and the thing
// actually protecting this is Argon2id over a passphrase nobody else has.
// Anything weak but long enough is warned about and accepted.
const MinPassphraseLen = 12

// GitAttributes is committed when the repository is created (§6.7 ④).
//
// The records are JSON holding base64, so a system git with the user's global
// core.autocrlf=true will happily turn every \n into \r\n on a Windows checkout
// and break every ciphertext in the repository. go-git does not translate line
// endings, but nothing stops the user cloning their own repository by hand, and
// they would be right to expect that to be safe.
const GitAttributes = "* -text\n"

// Errors callers distinguish.
var (
	// ErrWrongPassphrase is an unwrap that failed authentication. Deliberately
	// the same answer for a wrong passphrase and for a vault.json somebody
	// edited: both mean "this does not open", and telling them apart would be
	// telling an attacker which half they got right.
	ErrWrongPassphrase = errors.New("cfgsync: wrong passphrase, or the vault file was altered")
	// ErrCorrupt is a record that does not authenticate under the vault key.
	ErrCorrupt = errors.New("cfgsync: the record does not decrypt — it was altered, renamed, or written by another vault")
	// ErrUnsupported is a repository written by a version this build does not
	// know.
	ErrUnsupported = errors.New("cfgsync: unsupported repository format")
	// ErrPassphraseTooShort is a passphrase under MinPassphraseLen.
	ErrPassphraseTooShort = fmt.Errorf("cfgsync: the passphrase must be at least %d characters", MinPassphraseLen)
)

// KDFParams are the Argon2id settings, as the repository states them (§4.3).
//
// Read from vault.json and used as found, never taken from constants in this
// binary. The repository has to open on the weakest machine that will ever join
// — a Raspberry Pi running the Linux client — so the machine that created it
// decides, once, and every later reader obeys (§6.7 ⑧).
type KDFParams struct {
	Alg       string `json:"alg"`
	Salt      []byte `json:"salt"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
}

// Argon2id is the only KDF this format has.
const Argon2id = "argon2id"

// Bounds on what a repository may ask a reader to compute.
//
// The parameters arrive from a file an attacker may have written, and Argon2 does
// exactly what it is told: `memory_kib: 64000000` is a 64 GB allocation on the
// machine that opens it. Reading the parameters from the file (§6.7 ⑧) and
// trusting them without limit are different things.
const (
	maxKDFMemoryKiB = 1 << 20 // 1 GiB
	maxKDFTime      = 16
	maxKDFThreads   = 16
	minKDFSaltLen   = 8
)

// DefaultKDF returns the parameters a new repository is created with (§4.3).
//
// 64 MiB and four threads: enough to make a stolen vault.json expensive to
// attack, and small enough that a Pi can open it.
func DefaultKDF() (KDFParams, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return KDFParams{}, fmt.Errorf("cfgsync: salt: %w", err)
	}
	return KDFParams{
		Alg:       Argon2id,
		Salt:      salt,
		Time:      3,
		MemoryKiB: 64 * 1024,
		Threads:   4,
	}, nil
}

func (p KDFParams) validate() error {
	if p.Alg != Argon2id {
		return fmt.Errorf("%w: key derivation %q", ErrUnsupported, p.Alg)
	}
	if len(p.Salt) < minKDFSaltLen {
		return fmt.Errorf("%w: salt is %d bytes", ErrUnsupported, len(p.Salt))
	}
	if p.Time == 0 || p.Time > maxKDFTime ||
		p.MemoryKiB == 0 || p.MemoryKiB > maxKDFMemoryKiB ||
		p.Threads == 0 || p.Threads > maxKDFThreads {
		return fmt.Errorf("%w: key derivation parameters out of range (time=%d memory=%dKiB threads=%d)",
			ErrUnsupported, p.Time, p.MemoryKiB, p.Threads)
	}
	return nil
}

// kek derives the key-encryption key from a passphrase.
func (p KDFParams) kek(passphrase string) ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	return argon2.IDKey([]byte(passphrase), p.Salt, p.Time, p.MemoryKiB, p.Threads, chacha20poly1305.KeySize), nil
}

// SealedKey is the vault key wrapped with the KEK (§4.3).
type SealedKey struct {
	Alg        string `json:"alg"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// XChaCha20Poly1305 is the only cipher this format has.
const XChaCha20Poly1305 = "xchacha20poly1305"

// VaultFile is vault.json: plaintext, and the one file in the repository that
// is meant to be readable (§4.3).
type VaultFile struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	KDF        KDFParams `json:"kdf"`
	WrappedKey SealedKey `json:"wrapped_key"`
	CreatedAt  time.Time `json:"created_at"`
}

// Vault seals and opens records. It holds the vault key, so it stays in memory
// and is never written anywhere by this package.
type Vault struct {
	key []byte
}

// CreateVault makes a new repository's vault: a fresh random vault key, wrapped
// with a KEK derived from the passphrase.
//
// The returned VaultFile is what gets committed; the Vault is what encrypts.
func CreateVault(passphrase string) (VaultFile, *Vault, error) {
	if err := CheckPassphrase(passphrase); err != nil {
		return VaultFile{}, nil, err
	}
	kdf, err := DefaultKDF()
	if err != nil {
		return VaultFile{}, nil, err
	}
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		return VaultFile{}, nil, fmt.Errorf("cfgsync: vault key: %w", err)
	}
	wrapped, err := wrapKey(key, kdf, passphrase)
	if err != nil {
		return VaultFile{}, nil, err
	}
	return VaultFile{
		Format:     Format,
		Version:    Version,
		KDF:        kdf,
		WrappedKey: wrapped,
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
	}, &Vault{key: key}, nil
}

// Open unwraps the vault key with the passphrase.
//
// The Argon2 parameters come from vf, not from this build (§6.7 ⑧).
func (vf VaultFile) Open(passphrase string) (*Vault, error) {
	if vf.Format != Format {
		return nil, fmt.Errorf("%w: %q is not a LiteDeck sync repository", ErrUnsupported, vf.Format)
	}
	if vf.Version != Version {
		return nil, fmt.Errorf("%w: repository version %d, this build understands %d",
			ErrUnsupported, vf.Version, Version)
	}
	if vf.WrappedKey.Alg != XChaCha20Poly1305 {
		return nil, fmt.Errorf("%w: cipher %q", ErrUnsupported, vf.WrappedKey.Alg)
	}
	kek, err := vf.KDF.kek(passphrase)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, fmt.Errorf("cfgsync: cipher: %w", err)
	}
	if len(vf.WrappedKey.Nonce) != aead.NonceSize() {
		return nil, ErrWrongPassphrase
	}
	key, err := aead.Open(nil, vf.WrappedKey.Nonce, vf.WrappedKey.Ciphertext, []byte(vaultAD))
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	if len(key) != chacha20poly1305.KeySize {
		return nil, ErrWrongPassphrase
	}
	return &Vault{key: key}, nil
}

// ChangePassphrase rewraps the same vault key under a new passphrase.
//
// The records are untouched: they are encrypted with the vault key, and the
// passphrase only ever wrapped that key. Re-encrypting a hundred records to
// change a passphrase would be a migration that can fail halfway, for no gain.
func (vf VaultFile) ChangePassphrase(old, new string) (VaultFile, error) {
	v, err := vf.Open(old)
	if err != nil {
		return VaultFile{}, err
	}
	if err := CheckPassphrase(new); err != nil {
		return VaultFile{}, err
	}
	// A fresh salt as well. Reusing it would let somebody holding both versions
	// of the file attack one KEK and get two.
	kdf, err := DefaultKDF()
	if err != nil {
		return VaultFile{}, err
	}
	wrapped, err := wrapKey(v.key, kdf, new)
	if err != nil {
		return VaultFile{}, err
	}
	out := vf
	out.KDF = kdf
	out.WrappedKey = wrapped
	return out, nil
}

func wrapKey(key []byte, kdf KDFParams, passphrase string) (SealedKey, error) {
	kek, err := kdf.kek(passphrase)
	if err != nil {
		return SealedKey{}, err
	}
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return SealedKey{}, fmt.Errorf("cfgsync: cipher: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return SealedKey{}, fmt.Errorf("cfgsync: nonce: %w", err)
	}
	return SealedKey{
		Alg:        XChaCha20Poly1305,
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, key, []byte(vaultAD)),
	}, nil
}

// sealed is a record file: the whole file is this JSON (§4.4).
type sealed struct {
	V     int    `json:"v"`
	Nonce []byte `json:"nonce"`
	CT    []byte `json:"ct"`
}

// Encrypt seals plaintext for one repository path.
//
// repoPath is the path inside the repository, always with "/" separators — it
// goes into the associated data, so a record moved or renamed no longer opens.
// That is deliberate: without it, somebody with write access to the repository
// could swap one host's record onto another host's file name and change where
// the app connects, without ever holding the key.
func (v *Vault) Encrypt(repoPath string, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(v.key)
	if err != nil {
		return nil, fmt.Errorf("cfgsync: cipher: %w", err)
	}
	ad, err := associatedData(repoPath)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("cfgsync: nonce: %w", err)
	}
	file, err := json.Marshal(sealed{
		V:     Version,
		Nonce: nonce,
		CT:    aead.Seal(nil, nonce, plaintext, ad),
	})
	if err != nil {
		return nil, fmt.Errorf("cfgsync: encode record: %w", err)
	}
	return append(file, '\n'), nil
}

// Decrypt opens a record file that was sealed for repoPath.
func (v *Vault) Decrypt(repoPath string, file []byte) ([]byte, error) {
	var s sealed
	if err := json.Unmarshal(file, &s); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if s.V != Version {
		return nil, fmt.Errorf("%w: record version %d", ErrUnsupported, s.V)
	}
	aead, err := chacha20poly1305.NewX(v.key)
	if err != nil {
		return nil, fmt.Errorf("cfgsync: cipher: %w", err)
	}
	if len(s.Nonce) != aead.NonceSize() {
		return nil, ErrCorrupt
	}
	ad, err := associatedData(repoPath)
	if err != nil {
		return nil, err
	}
	out, err := aead.Open(nil, s.Nonce, s.CT, ad)
	if err != nil {
		return nil, ErrCorrupt
	}
	return out, nil
}

// associatedData binds a record to where it lives (§4.4).
func associatedData(repoPath string) ([]byte, error) {
	if strings.Contains(repoPath, `\`) {
		// Caught rather than accepted, because accepting it produces a record
		// that only opens on the machine that wrote it (§6.7 ③). A backslash is
		// a legal character in a POSIX file name, so this is not merely a
		// separator check — but no path this package builds contains one, and a
		// repository is not the place to find out.
		return nil, fmt.Errorf("cfgsync: repository path %q uses a backslash — "+
			"paths inside the repository are built with `path`, never `filepath`", repoPath)
	}
	clean := path.Clean(repoPath)
	if clean != repoPath || clean == "." || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "..") {
		return nil, fmt.Errorf("cfgsync: %q is not a plain relative path inside the repository", repoPath)
	}
	return []byte(adPrefix + clean), nil
}

// lowercaseUUID matches the only shape a record file is named after (§4.1).
var lowercaseUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// RecordPath is where a host's record lives in the repository (§4.1).
//
// Lowercase, always. macOS and Windows fold case in file names and Linux does
// not, so two records written on Linux under IDs differing only in case would
// arrive on a Mac as one file overwriting the other (§6.7 ⑤). The host's name
// never appears: a file listing is visible to whoever hosts the repository.
func RecordPath(id string) (string, error) {
	if !lowercaseUUID.MatchString(id) {
		return "", fmt.Errorf("cfgsync: %q is not a lowercase hyphenated UUID — "+
			"only migrated hosts are synced", id)
	}
	return path.Join(recordDir, id+recordExt), nil
}

const (
	recordDir = "hosts"
	recordExt = ".enc"
	// VaultPath and AttributesPath are the two plaintext files at the root.
	VaultPath      = "vault.json"
	AttributesPath = ".gitattributes"
	ReadmePath     = "README.md"
)

// RecordID reads the host ID back out of a repository path, or reports that the
// path is not a record.
func RecordID(repoPath string) (string, bool) {
	dir, file := path.Split(repoPath)
	if strings.TrimSuffix(dir, "/") != recordDir || !strings.HasSuffix(file, recordExt) {
		return "", false
	}
	id := strings.TrimSuffix(file, recordExt)
	if !lowercaseUUID.MatchString(id) {
		return "", false
	}
	return id, true
}

// CheckPassphrase refuses one that is too short, and returns nil for everything
// else.
func CheckPassphrase(p string) error {
	if len([]rune(p)) < MinPassphraseLen {
		return ErrPassphraseTooShort
	}
	return nil
}

// PassphraseWarning describes a passphrase that is long enough but weak, or "" if
// there is nothing to say.
//
// A warning and not a refusal. The user is choosing how to protect their own host
// list on their own repository; a rule that rejects what they picked mostly
// produces a passphrase with "1!" on the end, written on something.
func PassphraseWarning(p string) string {
	runes := []rune(p)
	if len(runes) < MinPassphraseLen {
		return ""
	}
	kinds := 0
	for _, class := range []func(rune) bool{unicode.IsLower, unicode.IsUpper, unicode.IsDigit} {
		for _, r := range runes {
			if class(r) {
				kinds++
				break
			}
		}
	}
	punct := false
	for _, r := range runes {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r) {
			punct = true
			break
		}
	}
	if punct {
		kinds++
	}
	if allOneRune(runes) {
		return i18n.T("같은 문자만 반복됩니다")
	}
	if kinds <= 1 && len(runes) < 20 {
		return i18n.T("한 종류의 문자로만 되어 있습니다 — 길게 하거나 섞으세요")
	}
	return ""
}

func allOneRune(rs []rune) bool {
	for _, r := range rs {
		if r != rs[0] {
			return false
		}
	}
	return len(rs) > 0
}
