package cfgsync

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// The whole host list in one encrypted file.
//
// # Why this exists beside the git sync
//
// A git repository is a fine place to keep settings if you already have one.
// Most people do not, and "make a private repository, register a deploy key with
// write access" is a sentence written for somebody who has done it before. The
// second most universal thing anybody has is a folder that syncs itself — Google
// Drive, Dropbox, iCloud, a USB stick, an email to yourself.
//
// So: one file. Encrypted with the same passphrase-derived key as the repository
// (§4.4), self-contained, and readable by any LiteDeck with the passphrase. Put it
// where you like; LiteDeck neither knows nor cares where it went.
//
// # What it is not
//
// Not a sync. There is no merge history, no revisions moving between machines,
// nothing that notices two people editing at once. It is a snapshot: made here,
// opened there. Where the two overlap — what happens to a policy that arrives
// looser than the one on this machine — the file goes through the same gate the
// repository does (§6.2), because that rule protects against the file as much as
// against the repository: whoever wrote it decided what an AI client may do to
// your servers, and they were not sitting at this desk.

// BundleFormat and BundleVersion identify the file.
const (
	BundleFormat  = "litedeck-backup"
	BundleVersion = 1
	// BundleExt is the file name extension. Deliberately not .json: it holds
	// ciphertext, and a name that invites somebody to open it in an editor and
	// "fix" it is a name that produces a file that no longer decrypts.
	BundleExt = ".ldbackup"
	bundleAD  = adPrefix + "bundle"
)

// Bundle is the file, as it sits on disk.
//
// The envelope is the repository's: the same Argon2id parameters in the file
// rather than in the code (§6.7 ⑧), the same wrapped key, the same cipher. A
// second crypto format for the same job would be a second thing to get wrong.
type Bundle struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	KDF        KDFParams `json:"kdf"`
	WrappedKey SealedKey `json:"wrapped_key"`
	// Payload is the records, encrypted with the bundle key.
	Payload   SealedKey `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
	// Device is the UUID of the machine that wrote it — not its name. This file
	// ends up in somebody's Drive, and a list of the user's computers is not
	// something to put there for free.
	Device string `json:"device,omitempty"`
}

// bundlePayload is what the ciphertext holds.
type bundlePayload struct {
	Records []Record `json:"records"`
}

// ExportBundle encrypts the records into one file's contents.
func ExportBundle(records []Record, passphrase, deviceID string) ([]byte, error) {
	if err := CheckPassphrase(passphrase); err != nil {
		return nil, err
	}
	// Sorted, so exporting the same hosts twice gives two files that differ only
	// where the contents differ. Somebody diffing two backups should see their
	// own changes, not a reshuffle.
	sorted := make([]Record, len(records))
	copy(sorted, records)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for i := range sorted {
		sorted[i].normalise()
	}

	plain, err := json.Marshal(bundlePayload{Records: sorted})
	if err != nil {
		return nil, fmt.Errorf("cfgsync: encode backup: %w", err)
	}

	vf, vault, err := CreateVault(passphrase)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(vault.key)
	if err != nil {
		return nil, fmt.Errorf("cfgsync: cipher: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := randRead(nonce); err != nil {
		return nil, fmt.Errorf("cfgsync: nonce: %w", err)
	}

	b := Bundle{
		Format:     BundleFormat,
		Version:    BundleVersion,
		KDF:        vf.KDF,
		WrappedKey: vf.WrappedKey,
		Payload: SealedKey{
			Alg:        XChaCha20Poly1305,
			Nonce:      nonce,
			Ciphertext: aead.Seal(nil, nonce, plain, []byte(bundleAD)),
		},
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Device:    deviceID,
	}
	out, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cfgsync: encode backup: %w", err)
	}
	return append(out, '\n'), nil
}

// OpenBundle decrypts a file's contents.
//
// The same two answers as the vault: a wrong passphrase and an altered file are
// both ErrWrongPassphrase, because telling them apart tells an attacker which
// half they got right.
func OpenBundle(data []byte, passphrase string) ([]Record, Bundle, error) {
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, b, fmt.Errorf("%w: this is not a LiteDeck backup file", ErrUnsupported)
	}
	if b.Format != BundleFormat {
		return nil, b, fmt.Errorf("%w: %q is not a LiteDeck backup", ErrUnsupported, b.Format)
	}
	if b.Version != BundleVersion {
		return nil, b, fmt.Errorf("%w: backup version %d, this build understands %d",
			ErrUnsupported, b.Version, BundleVersion)
	}
	if b.WrappedKey.Alg != XChaCha20Poly1305 || b.Payload.Alg != XChaCha20Poly1305 {
		return nil, b, fmt.Errorf("%w: cipher %q", ErrUnsupported, b.Payload.Alg)
	}

	// Unwrapping the key is the passphrase check; the payload is opened with what
	// comes out. Both are authenticated, so an edited file fails here rather than
	// producing records somebody else chose.
	vf := VaultFile{Format: Format, Version: Version, KDF: b.KDF, WrappedKey: b.WrappedKey}
	vault, err := vf.Open(passphrase)
	if err != nil {
		return nil, b, err
	}
	aead, err := chacha20poly1305.NewX(vault.key)
	if err != nil {
		return nil, b, fmt.Errorf("cfgsync: cipher: %w", err)
	}
	if len(b.Payload.Nonce) != aead.NonceSize() {
		return nil, b, ErrCorrupt
	}
	plain, err := aead.Open(nil, b.Payload.Nonce, b.Payload.Ciphertext, []byte(bundleAD))
	if err != nil {
		return nil, b, ErrCorrupt
	}

	var payload bundlePayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return nil, b, fmt.Errorf("cfgsync: decode backup: %w", err)
	}
	for i := range payload.Records {
		if !lowercaseUUID.MatchString(payload.Records[i].ID) {
			return nil, b, fmt.Errorf("cfgsync: the backup holds a record with id %q",
				payload.Records[i].ID)
		}
		payload.Records[i].normalise()
	}
	return payload.Records, b, nil
}

// BundleName is the file name to suggest, given the date.
//
// Dated, because the thing people do with these is keep several and then have to
// work out which is which — in a folder listing, with no preview, because the
// contents are ciphertext.
func BundleName(at time.Time) string {
	return fmt.Sprintf("litedeck-%s%s", at.Format("2006-01-02"), BundleExt)
}
