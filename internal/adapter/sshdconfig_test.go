package adapter

import "testing"

func TestParseSSHDConfig(t *testing.T) {
	const body = `# Ubuntu's stock file, near enough
Include /etc/ssh/sshd_config.d/*.conf
Include relative.d/*.conf

Port 2222
PermitRootLogin=prohibit-password
	PasswordAuthentication   no
AuthorizedKeysCommand /usr/bin/keys --user=%u --key=%k

Match Address 10.0.0.0/8
    PermitRootLogin yes
    PasswordAuthentication yes
`

	got, includes := ParseSSHDConfig("/etc/ssh/sshd_config", []byte(body))

	wantIncludes := []string{"/etc/ssh/sshd_config.d/*.conf", "/etc/ssh/relative.d/*.conf"}
	var gotPatterns []string
	for _, inc := range includes {
		gotPatterns = append(gotPatterns, inc.Patterns...)
		// Both Includes are above every directive in this file, which is where
		// a distribution puts them and the reason a drop-in wins.
		if inc.After != 0 {
			t.Errorf("Include recorded at position %d, want 0 — an Include on line 2 is expanded before the directives below it", inc.After)
		}
	}
	if len(gotPatterns) != len(wantIncludes) {
		t.Fatalf("includes = %q, want %q", gotPatterns, wantIncludes)
	}
	for i, w := range wantIncludes {
		if gotPatterns[i] != w {
			t.Errorf("includes[%d] = %q, want %q — a relative Include resolves against sshd_config's own directory", i, gotPatterns[i], w)
		}
	}

	want := []struct {
		keyword, value string
		conditional    bool
	}{
		{"Port", "2222", false},
		{"PermitRootLogin", "prohibit-password", false},
		{"PasswordAuthentication", "no", false},
		// The `=` inside the value must survive: it is an argument, not a
		// keyword separator.
		{"AuthorizedKeysCommand", "/usr/bin/keys --user=%u --key=%k", false},
		{"Match", "Address 10.0.0.0/8", true},
		{"PermitRootLogin", "yes", true},
		{"PasswordAuthentication", "yes", true},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d directives, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Keyword != w.keyword || got[i].Value != w.value || got[i].Conditional != w.conditional {
			t.Errorf("directive %d = %+v, want %s=%q conditional=%v",
				i, got[i], w.keyword, w.value, w.conditional)
		}
	}
}

// TestSSHDFirstValueWins pins the rule that makes sshd different from almost
// every other config format, and the reason Include sits at the top of the file.
func TestSSHDFirstValueWins(t *testing.T) {
	dropIn, _ := ParseSSHDConfig("/etc/ssh/sshd_config.d/50-cloud.conf",
		[]byte("PasswordAuthentication yes\n"))
	main, _ := ParseSSHDConfig("/etc/ssh/sshd_config",
		[]byte("PasswordAuthentication no\nPort 22\n"))

	// The Include is at the top, so the drop-in is read first.
	rep := BuildSSHDReport(
		[]string{"/etc/ssh/sshd_config.d/50-cloud.conf", "/etc/ssh/sshd_config"},
		append(dropIn, main...), nil)

	var got string
	for _, d := range rep.Declared {
		if d.Keyword == "PasswordAuthentication" {
			got = d.Value
		}
	}
	if got != "yes" {
		t.Errorf("PasswordAuthentication = %q, want yes — sshd keeps the first value it reads, so a drop-in included at the top beats the main file", got)
	}
	if len(rep.Declared) != 2 {
		t.Errorf("declared %d keywords, want 2 (the duplicate must collapse): %+v", len(rep.Declared), rep.Declared)
	}
}

// TestSSHDMatchBlocksAreNotGlobal: a value under Match applies to the clients
// that block names. Reporting it as the server's setting would be a lie, and
// the alarming direction of lie — "PermitRootLogin yes" is the example that
// turns up in real configs, restricted to an internal subnet.
func TestSSHDMatchBlocksAreNotGlobal(t *testing.T) {
	parsed, _ := ParseSSHDConfig("/etc/ssh/sshd_config", []byte(
		"PermitRootLogin no\nMatch Address 10.0.0.0/8\n  PermitRootLogin yes\n"))
	rep := BuildSSHDReport([]string{"/etc/ssh/sshd_config"}, parsed, nil)

	for _, d := range rep.Declared {
		if d.Keyword == "PermitRootLogin" && d.Value != "no" {
			t.Errorf("PermitRootLogin declared as %q, want no", d.Value)
		}
	}
	for _, n := range rep.Notes {
		if n.Code == "permit-root-login" {
			t.Error("a Match-scoped PermitRootLogin yes was reported as a server-wide warning")
		}
	}
	if len(rep.Matches) != 1 || rep.Matches[0].Value != "Address 10.0.0.0/8" {
		t.Errorf("Matches = %+v, want the one block to be listed", rep.Matches)
	}
}

func TestSSHDNotes(t *testing.T) {
	parsed, _ := ParseSSHDConfig("/etc/ssh/sshd_config", []byte(
		"Port 2222\nPermitRootLogin yes\nPermitEmptyPasswords yes\nMaxSessions 4\nX11Forwarding no\n"))
	rep := BuildSSHDReport([]string{"/etc/ssh/sshd_config"}, parsed, nil)

	byCode := map[string]SSHDNote{}
	for _, n := range rep.Notes {
		byCode[n.Code] = n
	}
	for _, code := range []string{"permit-root-login", "permit-empty-passwords", "max-sessions-low"} {
		if byCode[code].Level != SSHDWarn {
			t.Errorf("%s = %+v, want a warning", code, byCode[code])
		}
	}
	if byCode["port"].Level != SSHDInfo || byCode["port"].Value != "2222" {
		t.Errorf("port note = %+v, want info 2222", byCode["port"])
	}
	// X11Forwarding is off, so there is nothing to say about it.
	if _, ok := byCode["x11-forwarding"]; ok {
		t.Error("X11Forwarding no produced a note")
	}
	// Warnings come first, so the serious thing is not below a port number.
	if rep.Notes[0].Level != SSHDWarn {
		t.Errorf("notes lead with %+v, want a warning first", rep.Notes[0])
	}
	// Nothing is said about keywords the file never mentions: the built-in
	// default differs between distributions and this cannot know which applies.
	if _, ok := byCode["password-authentication"]; ok {
		t.Error("a note was produced for PasswordAuthentication, which the file does not set")
	}
}

// TestSSHDIncludePositionIsRecorded: the caller splices the included files in
// at this index. Get it wrong and a keyword the parent sets *below* its Include
// looks like the winner, when sshd would have taken the drop-in's.
func TestSSHDIncludePositionIsRecorded(t *testing.T) {
	parsed, includes := ParseSSHDConfig("/etc/ssh/sshd_config", []byte(
		"Port 22\nInclude /etc/ssh/sshd_config.d/*.conf\nPasswordAuthentication no\n"))

	if len(includes) != 1 {
		t.Fatalf("includes = %+v, want one", includes)
	}
	if includes[0].After != 1 {
		t.Fatalf("Include.After = %d, want 1 — one directive (Port) precedes it", includes[0].After)
	}
	// Splicing at that index is what the app does; the result has to be the
	// order sshd reads in.
	dropIn, _ := ParseSSHDConfig("/etc/ssh/sshd_config.d/50-cloud.conf",
		[]byte("PasswordAuthentication yes\n"))

	var merged []SSHDDirective
	merged = append(merged, parsed[:includes[0].After]...)
	merged = append(merged, dropIn...)
	merged = append(merged, parsed[includes[0].After:]...)

	rep := BuildSSHDReport(nil, merged, nil)
	for _, d := range rep.Declared {
		if d.Keyword == "PasswordAuthentication" && d.Value != "yes" {
			t.Errorf("PasswordAuthentication = %q from %s, want yes from the drop-in — it is included above the parent's own setting",
				d.Value, d.File)
		}
	}
}

func TestSSHDReportKeepsUnreadableFiles(t *testing.T) {
	rep := BuildSSHDReport([]string{"/etc/ssh/sshd_config"}, nil,
		[]string{"/etc/ssh/sshd_config.d/99-secret.conf"})
	if len(rep.Unreadable) != 1 {
		t.Errorf("Unreadable = %+v, want the file sshd reads and this could not", rep.Unreadable)
	}
}
