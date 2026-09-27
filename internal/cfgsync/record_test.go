package cfgsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
)

const testID = "3f2a0c1e-5b6d-4e7f-8a90-112233445566"

func sampleRecord() Record {
	return Record{
		ID:        testID,
		Rev:       7,
		UpdatedAt: time.Date(2026, 9, 24, 12, 34, 56, 0, time.UTC),
		UpdatedBy: "device-a",
		Host: RecordHost{
			Name: "prod-web", Group: "prod", Hostname: "10.0.0.5", Port: 22,
			User: "deploy", ProxyJump: "bastion@10.0.0.1:22",
		},
		Auth: RecordAuth{
			Methods: []string{"agent", "key"}, KeyFingerprint: "SHA256:abc",
			KeyLabel: "work-ed25519",
		},
		HostKeys: []RecordHostKey{{
			Alg: "ssh-ed25519", Key: "AAAA", Fingerprint: "SHA256:def",
			TrustedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), TrustedBy: "device-a",
		}},
		Policy: RecordPolicy{Shared: true, MCPApproval: ApprovalAsk},
	}
}

func TestRecordRoundTrip(t *testing.T) {
	want := sampleRecord()
	b, err := want.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := UnmarshalRecord(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.SameContent(want) || got.Rev != want.Rev || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("round trip changed the record:\n got %+v\nwant %+v", got, want)
	}
	// The field names in the file are the ones in the spec, because another
	// LiteDeck build reads them.
	for _, want := range []string{`"updated_at"`, `"updated_by"`, `"proxy_jump"`,
		`"key_fingerprint"`, `"host_keys"`, `"mcp_approval"`, `"exec_enabled"`, `"delete_enabled"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the record has no %s field:\n%s", want, b)
		}
	}
}

// The expiry of a relaxed approval mode is not in the record (§6.2).
//
// Two machines whose clocks differ by minutes would disagree about whether an
// eight-hour window is still open, and the disagreement resolves towards more
// permission. The mode travels; the countdown starts on the machine where
// somebody presses the button.
func TestTheApprovalExpiryIsNotInTheRecord(t *testing.T) {
	b, err := sampleRecord().Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if regexp.MustCompile(`(?i)"[^"]*until[^"]*"`).Match(b) {
		t.Errorf("the record carries an expiry:\n%s", b)
	}
	// And the type itself has no such field, so one cannot be added without
	// somebody reading this. Read off the struct rather than off encoded output:
	// a field with omitempty and a zero value leaves no trace in the JSON, which
	// is exactly how an expiry could be added and this test still pass.
	var fields []string
	rt := reflect.TypeOf(RecordPolicy{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		fields = append(fields, f.Name, f.Tag.Get("json"))
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), "until") || strings.Contains(strings.ToLower(f), "expir") {
			t.Errorf("RecordPolicy has a %q field — the expiry does not travel (§6.2)", f)
		}
	}
}

// Stricter is a bigger number, and an unknown mode is the strictest of all.
//
// A repository written by a newer LiteDeck can name a mode this build has never
// heard of. Treating that as permissive would be opening a door because the label
// was unfamiliar.
func TestStrictnessOrdersTheModesAndDistrustsUnknownOnes(t *testing.T) {
	if !(Strictness(ApprovalBypass) < Strictness(ApprovalAsk) &&
		Strictness(ApprovalAsk) < Strictness(ApprovalStrict)) {
		t.Errorf("bypass=%d ask=%d strict=%d", Strictness(ApprovalBypass),
			Strictness(ApprovalAsk), Strictness(ApprovalStrict))
	}
	if Strictness("") != Strictness(ApprovalAsk) {
		t.Error(`an empty mode is "ask" everywhere else in the app`)
	}
	if Strictness("something-new") <= Strictness(ApprovalStrict) {
		t.Error("an unknown mode was treated as looser than strict")
	}
}

// The approval modes are the app's, spelled the same way.
//
// This package deliberately does not import internal/app — that would be a cycle
// waiting to happen — so the three strings are written out again here. A mode
// renamed in one place and not the other would arrive on another machine as an
// unknown string. Strictness treats that as the strictest, so nothing opens that
// should not; the host simply becomes un-loosenable and nobody is told why.
func TestApprovalModesMatchTheApp(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "app", "mcp_approval.go"))
	if err != nil {
		t.Fatalf("read mcp_approval.go: %v", err)
	}
	src := string(b)
	for _, want := range []struct{ konst, value string }{
		{"WriteAsk", ApprovalAsk},
		{"WriteStrict", ApprovalStrict},
		{"WriteBypass", ApprovalBypass},
	} {
		if !strings.Contains(src, want.konst+` = "`+want.value+`"`) {
			t.Errorf("internal/app no longer spells %s as %q — a record written here would "+
				"name a mode the app does not know", want.konst, want.value)
		}
	}
}

// A host arriving for the first time starts at the strictest the app has (§6.2).
func TestANewHostStartsAtTheDefaults(t *testing.T) {
	p := StrictestPolicy()
	if p.Shared {
		t.Error("a host arriving from the repository was handed to AI clients")
	}
	if p.ExecEnabled || p.DeleteEnabled {
		t.Errorf("command execution or file deletion started on: %+v", p)
	}
	// The app's default, not the strictest mode there is: see StrictestPolicy.
	if Strictness(p.MCPApproval) < Strictness(ApprovalAsk) {
		t.Errorf("approval mode %q is looser than the app's default", p.MCPApproval)
	}
	if p.MCPApproval != ApprovalAsk {
		t.Errorf("approval mode %q is not the app's default, so every arriving host "+
			"raises a question and pushes this value back to the others", p.MCPApproval)
	}
}

// Host keys are ordered before they are sealed.
//
// Otherwise the same knowledge produces different bytes, which is a different
// ciphertext, which is a commit, which the other machine pulls and answers in
// kind. That is a sync loop between two machines that agree about everything.
func TestRecordsWithTheSameContentEncodeTheSameWay(t *testing.T) {
	a := sampleRecord()
	a.HostKeys = append(a.HostKeys, RecordHostKey{Alg: "rsa-sha2-512", Key: "BBBB"})
	b := sampleRecord()
	b.HostKeys = []RecordHostKey{{Alg: "rsa-sha2-512", Key: "BBBB"}, a.HostKeys[0]}

	ab, err := a.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	bb, err := b.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(ab) != string(bb) {
		t.Errorf("the same host keys in a different order gave different bytes:\n%s\n%s", ab, bb)
	}

	// SameContent ignores the bookkeeping, which is what decides whether there is
	// anything to push at all.
	later := sampleRecord()
	later.Rev = 99
	later.UpdatedAt = time.Now()
	later.UpdatedBy = "device-b"
	if !sampleRecord().SameContent(later) {
		t.Error("a record differing only in rev and timestamps counted as changed")
	}
	changed := sampleRecord()
	changed.Host.User = "root"
	if sampleRecord().SameContent(changed) {
		t.Error("a changed user counted as unchanged")
	}
}

// What cannot be filed is refused at the boundary.
func TestARecordThatCannotBeFiledIsRefused(t *testing.T) {
	good, err := sampleRecord().Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := UnmarshalRecord(good); err != nil {
		t.Fatalf("a good record was refused: %v", err)
	}
	for name, mut := range map[string]func(*Record){
		"no id":          func(r *Record) { r.ID = "" },
		"legacy id":      func(r *Record) { r.ID = "host-1786033219477533000" },
		"uppercase id":   func(r *Record) { r.ID = strings.ToUpper(testID) },
		"rev zero":       func(r *Record) { r.Rev = 0 },
		"negative rev":   func(r *Record) { r.Rev = -1 },
		"path in the id": func(r *Record) { r.ID = "../../vault" },
	} {
		r := sampleRecord()
		mut(&r)
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := UnmarshalRecord(b); err == nil {
			t.Errorf("a record with %s was accepted", name)
		}
	}
	if _, err := UnmarshalRecord([]byte("not json")); err == nil {
		t.Error("a file that is not JSON was accepted")
	}
}

// The local host entry survives the trip, minus what belongs to one machine.
func TestHostConversionKeepsWhatIsLocalAndCarriesWhatIsNot(t *testing.T) {
	local := config.Host{
		ID: testID, Name: "prod-web", Group: "prod", Hostname: "10.0.0.5", Port: 22,
		User: "deploy", Auth: []config.AuthMethod{config.AuthAgent, config.AuthKey},
		IdentityFile: "/Users/me/.ssh/id_ed25519", ProxyJump: "bastion@10.0.0.1:22",
		LegacyID: "host-1786033219477533000",
	}
	host, auth := FromHost(local, "SHA256:abc", FingerprintLabel(local.IdentityFile))

	b, err := json.Marshal(struct {
		H RecordHost
		A RecordAuth
	}{host, auth})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The private key's path is this machine's business (§3.3) — the label is
	// not, and is the whole point: a prompt on the next machine has to be
	// answerable. So is the ID this host used to have: nothing outside this
	// machine has any use for it.
	for _, forbidden := range []string{"/Users/me", "/.ssh/", "host-1786033219477533000"} {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("%q travelled: %s", forbidden, b)
		}
	}
	if auth.KeyLabel != "id_ed25519" {
		t.Errorf("key label = %q — the name is what makes a prompt answerable", auth.KeyLabel)
	}
	if len(auth.Methods) != 2 || auth.Methods[0] != "agent" || auth.Methods[1] != "key" {
		t.Errorf("auth methods = %v — the order is the user's decision", auth.Methods)
	}

	// Coming back the other way: the record's fields win, this machine's stay.
	r := sampleRecord()
	r.Host.Name = "renamed"
	back := r.ToHost(local)
	if back.Name != "renamed" {
		t.Errorf("the new name did not arrive: %q", back.Name)
	}
	if back.IdentityFile != local.IdentityFile {
		t.Errorf("the local key path was overwritten with %q", back.IdentityFile)
	}
	if back.LegacyID != local.LegacyID {
		t.Errorf("the local legacy id was lost: %q", back.LegacyID)
	}
	if back.ID != testID {
		t.Errorf("id = %q", back.ID)
	}

	// A record cannot claim to have come from ssh_config, which would make the
	// next import overwrite a host it did not create.
	r.Host.Name = "x"
	sneaky := r
	if got := sneaky.ToHost(config.Host{}).Source; got != "" {
		t.Errorf("source = %q for a host arriving from the repository", got)
	}
	if got := sneaky.ToHost(config.Host{Source: config.SSHConfigSource}).Source; got != config.SSHConfigSource {
		t.Errorf("a local ssh_config host lost its source: %q", got)
	}

	// An auth method this build does not know is dropped, not written into
	// hosts.json for every later read to cope with.
	r.Auth.Methods = []string{"agent", "quantum"}
	if got := r.ToHost(config.Host{}).Auth; len(got) != 1 || got[0] != config.AuthAgent {
		t.Errorf("auth = %v", got)
	}
}

// The policy is read out of settings.json as stored, not as currently in effect.
func TestPolicyComesFromSettings(t *testing.T) {
	s := config.Settings{MCP: config.MCPSettings{
		Hosts:  map[string]bool{testID: true},
		Write:  map[string]config.MCPWritePolicy{testID: {Mode: ApprovalBypass, Until: 1789000000}},
		Exec:   map[string]bool{testID: true},
		Delete: map[string]bool{},
	}}
	p := PolicyFromSettings(s, testID)
	if !p.Shared || p.MCPApproval != ApprovalBypass || !p.ExecEnabled || p.DeleteEnabled {
		t.Errorf("policy = %+v", p)
	}
	// A host with nothing stored is "ask", which is what the app does.
	if got := PolicyFromSettings(s, "9b1c7d2e-1111-4222-8333-444455556666"); got.MCPApproval != ApprovalAsk {
		t.Errorf("an unconfigured host = %+v", got)
	}
}
