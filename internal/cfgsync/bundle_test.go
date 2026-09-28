package cfgsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBundleRoundTrip(t *testing.T) {
	in := []Record{sampleRecord()}
	in[0].Policy = RecordPolicy{Shared: true, MCPApproval: ApprovalBypass}

	data, err := ExportBundle(in, goodPass, "device-a-0000-4000-8000-000000000000")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	// Nothing about the hosts is readable in the file.
	for _, secret := range []string{"prod-web", "10.0.0.5", "deploy", "bastion"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("%q is readable in the backup file", secret)
		}
	}

	out, meta, err := OpenBundle(data, goodPass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(out) != 1 || out[0].ID != testID {
		t.Fatalf("records = %+v", out)
	}
	if out[0].Host.Name != "prod-web" || out[0].Policy.MCPApproval != ApprovalBypass {
		t.Errorf("the record came back changed: %+v", out[0])
	}
	if meta.CreatedAt.IsZero() {
		t.Error("the backup has no date, so a folder full of them cannot be told apart")
	}
	// The file says which machine wrote it, by UUID — not by name. It ends up in
	// somebody's Drive, and a list of their computers is not free to give away.
	if meta.Device != "device-a-0000-4000-8000-000000000000" {
		t.Errorf("device = %q", meta.Device)
	}
	if strings.Contains(string(data), "MacBook") {
		t.Error("a machine name is in the file")
	}
}

func TestBundleRefusesTheWrongPassphraseAndATamperedFile(t *testing.T) {
	data, err := ExportBundle([]Record{sampleRecord()}, goodPass, "d")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, _, err := OpenBundle(data, goodPass+"x"); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("a wrong passphrase gave %v", err)
	}

	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// One byte of the payload. The file is JSON, so somebody will open it in an
	// editor sooner or later; what matters is that a changed file is refused
	// rather than producing records somebody else chose.
	b.Payload.Ciphertext[0] ^= 1
	tampered, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, _, err := OpenBundle(tampered, goodPass); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a changed payload gave %v", err)
	}

	// And a payload lifted from another backup does not open under this one's
	// key, even though both are valid files with valid keys.
	other, err := ExportBundle([]Record{sampleRecord()}, "a different passphrase", "d")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var ob Bundle
	if err := json.Unmarshal(other, &ob); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var mine Bundle
	if err := json.Unmarshal(data, &mine); err != nil {
		t.Fatalf("decode: %v", err)
	}
	mine.Payload = ob.Payload
	spliced, err := json.Marshal(mine)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, _, err := OpenBundle(spliced, goodPass); err == nil {
		t.Error("a payload from another backup opened")
	}
}

func TestBundleRefusesWhatItIsNot(t *testing.T) {
	for name, data := range map[string][]byte{
		"not json":       []byte("hello"),
		"other json":     []byte(`{"hello":"world"}`),
		"vault.json":     mustVaultJSON(t),
		"wrong version":  mustBundleWith(t, func(b *Bundle) { b.Version = 99 }),
		"wrong format":   mustBundleWith(t, func(b *Bundle) { b.Format = "something" }),
		"unknown cipher": mustBundleWith(t, func(b *Bundle) { b.Payload.Alg = "aes-gcm" }),
	} {
		if _, _, err := OpenBundle(data, goodPass); err == nil {
			t.Errorf("%s was accepted as a backup", name)
		}
	}
}

func mustVaultJSON(t *testing.T) []byte {
	t.Helper()
	vf, _, err := CreateVault(goodPass)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	b, err := json.Marshal(vf)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

func mustBundleWith(t *testing.T, mut func(*Bundle)) []byte {
	t.Helper()
	data, err := ExportBundle([]Record{sampleRecord()}, goodPass, "d")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	mut(&b)
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return out
}

// A short passphrase is refused here too.
//
// The file is the path with no server in it: it lands in a cloud folder, which is
// a folder somebody else's software indexes and somebody else's account can
// reach. The floor is not lower because the format is simpler.
func TestBundleHasTheSamePassphraseFloor(t *testing.T) {
	if _, err := ExportBundle([]Record{sampleRecord()}, "short", "d"); !errors.Is(err, ErrPassphraseTooShort) {
		t.Errorf("a short passphrase was accepted: %v", err)
	}
}

// The same hosts export to the same bytes, apart from the parts that must differ.
//
// Somebody keeping weekly backups in a Drive folder should be able to tell which
// ones actually changed. Nonces and salts are random by design, so the check is
// on the plaintext side: the record order does not wander.
func TestBundleOrdersItsRecords(t *testing.T) {
	a := sampleRecord()
	b := sampleRecord()
	b.ID = "11111111-1111-4111-8111-111111111111"

	first, err := ExportBundle([]Record{a, b}, goodPass, "d")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	second, err := ExportBundle([]Record{b, a}, goodPass, "d")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	one, _, err := OpenBundle(first, goodPass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	two, _, err := OpenBundle(second, goodPass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if one[0].ID != two[0].ID || one[1].ID != two[1].ID {
		t.Errorf("the order depends on how they were passed in: %s,%s vs %s,%s",
			one[0].ID, one[1].ID, two[0].ID, two[1].ID)
	}
}

func TestBundleNameIsDatedAndNotJSON(t *testing.T) {
	name := BundleName(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC))
	if name != "litedeck-2026-09-28.ldbackup" {
		t.Errorf("BundleName = %q", name)
	}
	if strings.HasSuffix(name, ".json") {
		t.Error("the name invites somebody to open it in an editor and fix it")
	}
}
