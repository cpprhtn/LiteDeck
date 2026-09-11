package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "security", name))
	if err != nil {
		t.Fatalf("golden %s: %v", name, err)
	}
	return string(b)
}

// The three states a tool can be in, and why "not installed" is not "off".
//
// Read from a real server: iptables has no unit at all, nftables has one that
// is disabled, ufw has one that is enabled and active. Telling a user their
// firewall is "off" when the package was never installed sends them looking for
// a switch that does not exist.
func TestParseUnitsSeparatesMissingFromDisabled(t *testing.T) {
	got := ParseSecurityUnits(golden(t, "ubuntu-24.04-units.txt"))
	by := map[string]SecurityUnit{}
	for _, u := range got {
		by[u.Name] = u
	}
	if len(by) != 4 {
		t.Fatalf("유닛 %d개, 기대 4개: %+v", len(by), got)
	}
	for _, tc := range []struct {
		name      string
		installed bool
		enabled   bool
		active    bool
	}{
		{"ufw.service", true, true, true},
		{"nftables.service", true, false, false},
		{"iptables.service", false, false, false},
		{"fail2ban.service", true, true, true},
	} {
		u := by[tc.name]
		if u.Installed != tc.installed || u.Enabled != tc.enabled || u.Active != tc.active {
			t.Errorf("%s → 설치=%v 켜짐=%v 활성=%v, 기대 %v/%v/%v",
				tc.name, u.Installed, u.Enabled, u.Active, tc.installed, tc.enabled, tc.active)
		}
	}
}

// ufw is a oneshot: it loads rules at boot and exits. Reading "active" as
// "SubState == running" reports a working firewall as a stopped one.
func TestOneshotFirewallCountsAsActive(t *testing.T) {
	for _, u := range ParseSecurityUnits(golden(t, "ubuntu-24.04-units.txt")) {
		if u.Name != "ufw.service" {
			continue
		}
		if !u.Active {
			t.Error("SubState=exited인 oneshot을 비활성으로 읽었다")
		}
		if u.SubState != "exited" {
			t.Errorf("SubState %q, 기대 exited — 도구설명에 그대로 써야 한다", u.SubState)
		}
	}
}

// The whole reason this feature exists.
//
// On the server this was captured from, ufw.service was enabled and active
// while ufw itself was off. A screen that reads the unit alone puts a green
// light on a firewall that is not running.
func TestUfwConfIsWhatSaysWhetherItIsOn(t *testing.T) {
	on, found := ParseUfwConf(golden(t, "ubuntu-24.04-ufw.conf"))
	if !found {
		t.Fatal("ENABLED 줄을 못 찾았다")
	}
	if on {
		t.Error("ENABLED=no인데 켜졌다고 읽었다")
	}

	yes, found := ParseUfwConf("ENABLED=yes\nLOGLEVEL=low\n")
	if !found || !yes {
		t.Error("ENABLED=yes를 못 읽었다")
	}
	if _, found := ParseUfwConf("LOGLEVEL=low\n"); found {
		t.Error("ENABLED 줄이 없는데 찾았다고 했다")
	}
	// Commented-out lines are not settings.
	if _, found := ParseUfwConf("#ENABLED=yes\n"); found {
		t.Error("주석 처리된 줄을 설정으로 읽었다")
	}
}

// Which jails the file turns on. The effective set needs fail2ban-client and
// root; this is what the file declares, and the screen says so.
func TestParseJailsReadsEnabledSections(t *testing.T) {
	jails := ParseFail2banJails(golden(t, "ubuntu-24.04-jail.local"))
	if len(jails) != 1 || jails[0] != "sshd" {
		t.Errorf("jail %v, 기대 [sshd]", jails)
	}

	multi := ParseFail2banJails(`[DEFAULT]
enabled = true

[sshd]
enabled = true

[nginx-http-auth]
enabled = false

[postfix]
enabled = true
`)
	if len(multi) != 2 || multi[0] != "sshd" || multi[1] != "postfix" {
		t.Errorf("jail %v, 기대 [sshd postfix] — DEFAULT는 jail이 아니고 false는 빠져야 한다", multi)
	}
}

// The script's sections, and what happens when a file is not there.
//
// A missing file writes nothing at all, so without the markers an absent
// ufw.conf and an empty one would look the same — and so would an absent
// ufw.conf and a jail.local that happened to follow it.
func TestSplitSecurityOutputHandlesMissingFiles(t *testing.T) {
	units, ufw, jails, mods := SplitSecurityOutput(
		"#units\nId=ufw.service\nLoadState=loaded\n#ufw\n#jails\n[sshd]\nenabled = true\n" +
			"#modules\nnf_tables 380928 814 - Live 0x0\n#end\n")
	if !strings.Contains(mods, "nf_tables") {
		t.Errorf("모듈 구역이 비었다: %q", mods)
	}
	if !strings.Contains(units, "Id=ufw.service") {
		t.Errorf("유닛 구역이 비었다: %q", units)
	}
	if strings.TrimSpace(ufw) != "" {
		t.Errorf("없는 파일인데 내용이 왔다: %q", ufw)
	}
	if !strings.Contains(jails, "[sshd]") {
		t.Errorf("jail 구역이 비었다: %q", jails)
	}
}

// The script goes to `sh -c` whole, so nothing in it may come from a caller.
func TestSecurityScriptIsAConstantWithNoHoles(t *testing.T) {
	for _, bad := range []string{"%s", "%v", "$1", "${"} {
		if strings.Contains(SecurityScript, bad) {
			t.Errorf("스크립트에 %q가 있다 — 무엇이든 끼워 넣을 자리가 생기면 안 된다", bad)
		}
	}
	for _, u := range SecurityUnits {
		if !strings.Contains(SecurityScript, u) {
			t.Errorf("%s를 묻지 않는다", u)
		}
	}
}

// The rule list a real server produced, and the three things a reader has to
// work out for themselves when it is printed raw.
func TestParseUfwStatus(t *testing.T) {
	s := ParseUfwStatus(golden(t, "ubuntu-24.04-ufw-status.txt"))
	if !s.Active {
		t.Error("Status: active인데 비활성으로 읽었다")
	}
	if s.Incoming != "deny" || s.Outgoing != "allow" {
		t.Errorf("기본 정책 in=%q out=%q, 기대 deny/allow", s.Incoming, s.Outgoing)
	}
	// Fourteen printed lines, seven rules: v4 and v6 say the same thing twice.
	if len(s.Rules) != 7 {
		t.Fatalf("규칙 %d개, 기대 7개 — v6 중복이 합쳐지지 않았다: %+v", len(s.Rules), s.Rules)
	}
	first := s.Rules[0]
	if first.To != "22/tcp" || first.Action != "ALLOW IN" || first.From != "Anywhere" {
		t.Errorf("첫 규칙 %+v", first)
	}
	if !first.V4 || !first.V6 {
		t.Error("22/tcp는 v4·v6 둘 다인데 한쪽만으로 읽었다")
	}
	// A rule can open several ports at once, and the cross-reference below
	// needs each of them, not the string.
	var nginx FirewallRule
	for _, r := range s.Rules {
		if strings.Contains(r.To, ",") {
			nginx = r
		}
	}
	if len(nginx.Ports) != 2 || nginx.Ports[0] != "80" || nginx.Ports[1] != "443" {
		t.Errorf("80,443을 포트 목록으로 못 갈랐다: %+v", nginx)
	}
	if nginx.Comment != "Nginx Full" {
		t.Errorf("프로필 이름 %q, 기대 %q", nginx.Comment, "Nginx Full")
	}
}

// An inactive firewall prints one line and no table.
func TestParseUfwStatusInactive(t *testing.T) {
	s := ParseUfwStatus("Status: inactive\n")
	if s.Active {
		t.Error("inactive를 활성으로 읽었다")
	}
	if len(s.Rules) != 0 {
		t.Errorf("규칙이 없는데 %d개 나왔다", len(s.Rules))
	}
}

// What the kernel is running, read from /proc/modules without root.
//
// The unit state does not answer this. nftables.service is Type=oneshot: it
// loads /etc/nftables.conf at boot and exits, so Ubuntu ships it disabled and
// uses ufw as the front end. A screen that reads `dead` as "no firewall" puts a
// red light on nearly every Ubuntu server — and a panel that cries wolf on a
// healthy machine is worse than no panel, because the colour stops meaning
// anything after the third time.
func TestFirewallModulesReadTheKernelNotTheUnit(t *testing.T) {
	k := ParseFirewallModules(golden(t, "ubuntu-24.04-modules.txt"))
	if !k.NFTables {
		t.Error("nf_tables가 올라와 있는데 못 봤다")
	}
	if k.NFTablesRefs != 814 {
		t.Errorf("nf_tables 참조 %d, 기대 814", k.NFTablesRefs)
	}
	// Loaded with nobody using it. Legacy iptables is present on this box and
	// doing nothing, which is not the same as being in use.
	if !k.IPTables {
		t.Error("ip_tables 모듈이 있는데 없다고 했다")
	}
	if k.IPTablesRefs != 0 {
		t.Errorf("ip_tables 참조 %d, 기대 0 — 올라와 있는 것과 쓰이는 것은 다르다", k.IPTablesRefs)
	}
	if !k.InUse() {
		t.Error("참조 814인데 안 쓰인다고 했다")
	}
}

func TestFirewallModulesOnAKernelWithNone(t *testing.T) {
	k := ParseFirewallModules("overlay 212992 0 - Live 0x0\nbtrfs 2056192 0 - Live 0x0\n")
	if k.NFTables || k.IPTables || k.InUse() {
		t.Errorf("netfilter 모듈이 없는데 있다고 했다: %+v", k)
	}
}

// Loaded but idle is not "in use". A box where the modules came up with the
// kernel and nothing ever added a rule must not read as protected.
func TestLoadedButUnusedIsNotInUse(t *testing.T) {
	k := ParseFirewallModules("nf_tables 380928 0 - Live 0x0\nip_tables 32768 0 - Live 0x0\n")
	if !k.NFTables {
		t.Error("모듈은 올라와 있다")
	}
	if k.InUse() {
		t.Error("참조가 0인데 쓰인다고 했다")
	}
}

// The jail as fail2ban is actually running it, against the file that was meant
// to configure it.
//
// This is the failure a config reader cannot see. `apt install` starts the
// service, so a later `enable --now` changes nothing and the daemon goes on
// running whatever it read first — the file said 20 retries and the jail was
// doing 5. Reading only the file reports the intent as if it were the state.
func TestJailStatusReadsWhatIsRunning(t *testing.T) {
	j := ParseJailStatus(golden(t, "ubuntu-24.04-f2b-effective.txt"))
	if j.MaxRetry != 5 || j.FindTime != 600 || j.BanTime != 600 {
		t.Errorf("유효값 %+v, 기대 5/600/600", j)
	}
	if j.CurrentlyBanned != 9 || j.TotalBanned != 287 {
		t.Errorf("밴 %d/%d, 기대 9/287", j.CurrentlyBanned, j.TotalBanned)
	}
	if j.TotalFailed != 14135 {
		t.Errorf("누적 실패 %d, 기대 14135", j.TotalFailed)
	}
	if len(j.Banned) != 3 || j.Banned[0] != "203.0.113.10" {
		t.Errorf("차단 목록 %v", j.Banned)
	}
}

// The declared value and the running one, side by side.
func TestJailMismatchIsFoundOnlyByComparing(t *testing.T) {
	running := JailStatus{MaxRetry: 5, FindTime: 600, BanTime: 600}
	declared := map[string]string{"maxretry": "20", "findtime": "1d", "bantime": "30m"}

	got := JailMismatches(declared, running)
	if len(got) != 3 {
		t.Fatalf("불일치 %d건, 기대 3건: %+v", len(got), got)
	}
	if got[0].Key != "maxretry" || got[0].Declared != "20" || got[0].Running != "5" {
		t.Errorf("%+v", got[0])
	}

	// Same value written differently is not a mismatch. 30m and 1800 are the
	// same instruction, and reporting them as a disagreement would teach people
	// to ignore this line.
	agreeing := map[string]string{"maxretry": "5", "findtime": "10m", "bantime": "600"}
	if got := JailMismatches(agreeing, running); len(got) != 0 {
		t.Errorf("같은 값인데 %d건 불일치라고 했다: %+v", len(got), got)
	}
}

// The sets a ruleset is blocking with, and who made each of them.
//
// fail2ban writes its own table and takes its entries out again when a ban
// expires; a hand-made table is permanent until somebody removes it. Mixing the
// two loses the only question worth asking about the list — "which of these did
// I put there".
func TestParseNftSetsSeparatesFail2banFromHandMade(t *testing.T) {
	sets := ParseNftSets(golden(t, "ubuntu-24.04-nft-ruleset.txt"))
	if len(sets) != 2 {
		t.Fatalf("집합 %d개, 기대 2개: %+v", len(sets), sets)
	}
	by := map[string]NftSet{}
	for _, s := range sets {
		by[s.Table] = s
	}

	mine := by["blackhole"]
	if mine.Fail2ban {
		t.Error("사람이 만든 테이블을 fail2ban 것이라고 했다")
	}
	// Nine elements across four wrapped lines, two of them ranges.
	if len(mine.Elements) != 9 {
		t.Errorf("원소 %d개, 기대 9개: %v", len(mine.Elements), mine.Elements)
	}
	if mine.Ranges != 2 {
		t.Errorf("대역 %d개, 기대 2개 — /24는 개별 주소와 다르게 세야 한다", mine.Ranges)
	}

	f2b := by["f2b-table"]
	if !f2b.Fail2ban {
		t.Error("f2b-table을 사람이 만든 것으로 봤다")
	}
	if len(f2b.Elements) != 2 {
		t.Errorf("f2b 원소 %d개, 기대 2개", len(f2b.Elements))
	}
}

// The drop counter is the only evidence that any of it is working.
func TestParseNftCountersFindTheDropRule(t *testing.T) {
	cs := ParseNftCounters(golden(t, "ubuntu-24.04-nft-ruleset.txt"))
	if len(cs) != 2 {
		t.Fatalf("카운터 %d개, 기대 2개: %+v", len(cs), cs)
	}
	if cs[0].Packets != 10274 || cs[0].Bytes != 616528 {
		t.Errorf("%+v", cs[0])
	}
	if cs[0].Verdict != "drop" || cs[0].Table != "blackhole" {
		t.Errorf("%+v", cs[0])
	}
	if cs[1].Verdict != "reject" || !cs[1].Fail2ban {
		t.Errorf("%+v", cs[1])
	}
}

// Empty is not the same as "could not read", and the screen must never show one
// as the other — an empty block list reading as "safe" is the worst outcome
// this feature can produce.
func TestParseNftOnEmptyOutput(t *testing.T) {
	if got := ParseNftSets(""); len(got) != 0 {
		t.Errorf("빈 출력에서 집합이 나왔다: %v", got)
	}
	if got := ParseNftCounters(""); len(got) != 0 {
		t.Errorf("빈 출력에서 카운터가 나왔다: %v", got)
	}
}

// Attackers, minus the ones already handled.
//
// A list that includes addresses the firewall is already dropping is not a list
// anybody can act on — and it misleads twice over, because a banned address
// goes on appearing in the log for as long as the window reaches back before
// the ban. Measured: an address topping a one-hour window with 931 lines whose
// most recent line was fifty minutes old, already blocked and quiet since.
func TestTopAttackersDropsWhatIsAlreadyBlocked(t *testing.T) {
	counts := map[string]int{
		"45.128.232.9":  931, // inside a banned /24
		"92.118.39.85":  412, // banned individually by fail2ban
		"203.0.113.77":  388, // not blocked
		"203.0.113.78":  201, // not blocked, same /24
		"198.51.100.31": 12,  // not blocked
	}
	blocked := []string{"45.128.232.0/24", "92.118.39.85"}

	got := TopAttackers(counts, blocked, 10)
	if len(got) != 3 {
		t.Fatalf("남은 %d개, 기대 3개: %+v", len(got), got)
	}
	if got[0].Address != "203.0.113.77" || got[0].Count != 388 {
		t.Errorf("가장 많은 것이 %+v", got[0])
	}
	for _, a := range got {
		if a.Address == "45.128.232.9" {
			t.Error("이미 대역째 막힌 주소가 남았다 — 대역 안에 있는지도 봐야 한다")
		}
	}
}

// Three or more from one /24 is a pattern that only shows when they are put
// together. Measured: seven hosts from 109.160.32.0/24 on one server and eight
// from 213.209.159.0/24 on another, each invisible one address at a time.
func TestSubnetClustersAreFoundOnlyByGrouping(t *testing.T) {
	got := SubnetClusters([]Attacker{
		{Address: "109.160.32.11", Count: 90},
		{Address: "109.160.32.12", Count: 80},
		{Address: "109.160.32.13", Count: 70},
		{Address: "203.0.113.77", Count: 400},
	}, 3)
	if len(got) != 1 {
		t.Fatalf("군집 %d개, 기대 1개: %+v", len(got), got)
	}
	if got[0].CIDR != "109.160.32.0/24" || got[0].Hosts != 3 || got[0].Count != 240 {
		t.Errorf("%+v", got[0])
	}
}

// The ban log, which is the only place two things live: when bans happened,
// and how many distinct addresses they landed on.
//
// The second is what makes a repeat rate mean anything. `fail2ban-client
// status` gives the total and how many are banned right now, and dividing by
// the second produced 143 on a server whose real figure was about four.
func TestParseBanLogCountsDistinctAddresses(t *testing.T) {
	h := ParseBanLog(golden(t, "ubuntu-24.04-fail2ban-bans.txt"))
	if len(h.Bans) != 10 {
		t.Fatalf("밴 %d건, 기대 10건 — Unban은 빼야 한다", len(h.Bans))
	}
	if h.Unique != 5 {
		t.Errorf("고유 주소 %d개, 기대 5개", h.Unique)
	}
	// Newest first: the screen reads downward from now.
	if h.Bans[0].Address != "201.81.240.158" {
		t.Errorf("첫 줄이 %+v — 최신이 위여야 한다", h.Bans[0])
	}
	if h.Bans[0].At.Format("2006-01-02 15:04") != "2026-09-08 23:15" {
		t.Errorf("시각 %v", h.Bans[0].At)
	}
	// Ten bans across five addresses. This is the number the screen could not
	// say before, and the reason 30분 was the right answer for bantime.
	if got := h.Repeats(); got != 2 {
		t.Errorf("재범률 %v, 기대 2", got)
	}
}

func TestParseBanLogOnAnEmptyOrRotatedFile(t *testing.T) {
	h := ParseBanLog("")
	if len(h.Bans) != 0 || h.Unique != 0 || h.Repeats() != 0 {
		t.Errorf("빈 로그에서 값이 나왔다: %+v", h)
	}
}

// Failures over time, bucketed on the server.
//
// The point of the chart is whether a block worked, which is a shape and not a
// total. Counting on the server keeps twenty thousand lines off the wire — the
// screen wants twenty-four numbers.
func TestParseFailureBuckets(t *testing.T) {
	got := ParseFailureBuckets("2026-09-08 20 931\n2026-09-08 21 402\n2026-09-08 22 12\n")
	if len(got) != 3 {
		t.Fatalf("구간 %d개, 기대 3개", len(got))
	}
	if got[0].Count != 931 || got[2].Count != 12 {
		t.Errorf("%+v", got)
	}
	if got[0].At.Hour() != 20 {
		t.Errorf("시각 %v", got[0].At)
	}
	// Oldest first: a chart is read left to right.
	if !got[0].At.Before(got[2].At) {
		t.Error("오래된 것이 먼저여야 한다")
	}
}

func TestParseFailureBucketsIgnoresRubbish(t *testing.T) {
	if got := ParseFailureBuckets("-- No entries --\n\nnonsense\n"); len(got) != 0 {
		t.Errorf("쓰레기에서 구간이 나왔다: %+v", got)
	}
}

// Only a rule that refuses traffic is evidence of protection.
//
// Docker writes NAT and forward chains whose counters run into the hundreds of
// millions — 367,282,813 on one measured server — and they count packets that
// were *carried*. Summing those into "packets dropped" made a box whose
// firewall was switched off look like the busiest one on the screen.
func TestOnlyRefusingVerdictsCountAsDropped(t *testing.T) {
	for _, v := range []string{"drop", "reject", "DROP", " reject "} {
		if !IsBlockingVerdict(v) {
			t.Errorf("%q는 막는 것이다", v)
		}
	}
	for _, v := range []string{"accept", "masquerade", "dnat", "return", "snat", ""} {
		if IsBlockingVerdict(v) {
			t.Errorf("%q를 막는 것으로 셌다", v)
		}
	}
}

// The same ruleset a Docker host produces: a blackhole that drops, and Docker's
// own chains that carry.
func TestCountersFromADockerHostSeparateCarriedFromRefused(t *testing.T) {
	cs := ParseNftCounters(`table inet blackhole {
	chain input {
		ip saddr @banned counter packets 11418 bytes 685080 drop
	}
}
table ip nat {
	chain DOCKER {
		iifname "docker0" counter packets 608133 bytes 36487980 return
	}
	chain POSTROUTING {
		oifname != "docker0" counter packets 367282813 bytes 22036968780 masquerade
	}
}
`)
	var dropped int64
	for _, c := range cs {
		if IsBlockingVerdict(c.Verdict) {
			dropped += c.Packets
		}
	}
	if dropped != 11418 {
		t.Errorf("버린 패킷 %d, 기대 11418 — 도커가 나른 것까지 셌다", dropped)
	}
}
