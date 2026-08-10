// Command litedeck-probe exercises the LiteDeck core against a real server
// without any GUI: connect, detect, list a directory over SFTP, read the
// service table, and fan out concurrent commands.
//
// It stands in for "the app" in the M0 completion criteria (§12) while the
// frontend framework is still undecided, and stays useful afterwards as the
// tool that answers "what does LiteDeck actually see on this host?" — the
// output is exactly what the bug report template asks for (§11).
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter/linuxsystemd"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
	"golang.org/x/crypto/ssh"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr        = flag.String("addr", "", "server address as host:port (required)")
		user        = flag.String("user", os.Getenv("USER"), "login user")
		keyPath     = flag.String("key", "", "private key file; falls back to ssh-agent, then password")
		password    = flag.String("password", "", "password (testing only — visible in your shell history)")
		listPath    = flag.String("path", "/etc", "remote directory to list over SFTP")
		concurrency = flag.Int("concurrency", 12, "how many commands to fan out at once")
		knownHosts  = flag.String("known-hosts", defaultKnownHosts(), "known_hosts file")
		insecure    = flag.Bool("insecure", false, "skip host key verification (never use against a real server)")
		verbose     = flag.Bool("v", false, "print every command as it runs (the Command Log)")
	)
	flag.Parse()

	if *addr == "" {
		flag.Usage()
		return fmt.Errorf("-addr is required")
	}
	if !strings.Contains(*addr, ":") {
		*addr += ":22"
	}

	auth, err := authMethods(*keyPath, *password)
	if err != nil {
		return err
	}
	hostKey, err := hostKeyCallback(*knownHosts, *insecure)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const hostID = "probe"
	mgr := sshcore.NewManager(sshcore.ManagerOptions{}, func(_ string, s sshcore.State, err error) {
		if err != nil {
			fmt.Printf("  [state] %s: %v\n", s, err)
			return
		}
		fmt.Printf("  [state] %s\n", s)
	})
	defer mgr.Close()

	var log sshcore.Observer
	if *verbose {
		log = &commandLog{}
	}

	section("1. 연결")
	start := time.Now()
	if err := mgr.Connect(ctx, sshcore.HostConfig{
		ID: hostID, Addr: *addr, User: *user, Auth: auth, HostKeyCallback: hostKey,
	}, log); err != nil {
		return err
	}
	fmt.Printf("  %s@%s — %v\n", *user, *addr, time.Since(start).Round(time.Millisecond))

	section("2. 탐지 (Detect)")
	info, err := detect(ctx, mgr, hostID)
	if err != nil {
		return err
	}
	fmt.Printf("  배포판       : %s\n", info.pretty)
	fmt.Printf("  systemd      : %d%s\n", info.systemdVersion, jsonNote(info.systemdVersion))
	fmt.Printf("  docker       : %s\n", yesNo(info.hasDocker))
	fmt.Printf("  sudo NOPASSWD: %s\n", yesNo(info.sudoNoPasswd))

	section("3. SFTP 디렉터리 목록")
	if err := listDir(mgr, hostID, *listPath); err != nil {
		return err
	}

	section("4. 서비스 목록")
	if info.systemdVersion == 0 {
		fmt.Println("  systemd 없음 — 서비스 탭은 비활성화됨 (§3.3)")
	} else if err := listServices(ctx, mgr, hostID, info.systemdVersion); err != nil {
		return err
	}

	section("5. 멀티세션 동시성")
	if err := concurrencyCheck(ctx, mgr, hostID, *concurrency); err != nil {
		return err
	}

	fmt.Println()
	return nil
}

type serverInfo struct {
	pretty         string
	systemdVersion int
	hasDocker      bool
	sudoNoPasswd   bool
}

// detect mirrors what ServerAdapter.Detect does on connect (§3.3).
func detect(ctx context.Context, mgr *sshcore.Manager, hostID string) (serverInfo, error) {
	var info serverInfo

	res, err := mgr.Exec(ctx, hostID, "cat", "/etc/os-release")
	if err != nil {
		return info, fmt.Errorf("read /etc/os-release: %w", err)
	}
	info.pretty = osReleaseField(string(res.Stdout), "PRETTY_NAME")

	if res, err := mgr.Exec(ctx, hostID, "systemctl", "--version"); err == nil && res.OK() {
		line, _, _ := strings.Cut(string(res.Stdout), "\n")
		if v, err := linuxsystemd.ParseSystemdVersion(line); err == nil {
			info.systemdVersion = v
		}
	}

	if res, err := mgr.Exec(ctx, hostID, "command", "-v", "docker"); err == nil {
		info.hasDocker = res.OK()
	}
	if res, err := mgr.Exec(ctx, hostID, "sudo", "-n", "true"); err == nil {
		info.sudoNoPasswd = res.OK()
	}
	return info, nil
}

func listDir(mgr *sshcore.Manager, hostID, path string) error {
	client, err := mgr.SFTP(hostID)
	if err != nil {
		return err
	}
	start := time.Now()
	entries, err := client.ReadDir(path)
	if err != nil {
		return fmt.Errorf("ReadDir %s: %w", path, err)
	}
	elapsed := time.Since(start)

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	fmt.Printf("  %s — %d개 항목, %v\n", path, len(entries), elapsed.Round(time.Millisecond))
	for i, e := range entries {
		if i >= 8 {
			fmt.Printf("  ... 그 외 %d개\n", len(entries)-8)
			break
		}
		kind := "-"
		if e.IsDir() {
			kind = "d"
		}
		fmt.Printf("  %s %s %9d  %s\n", kind, e.Mode().Perm(), e.Size(), e.Name())
	}
	return nil
}

func listServices(ctx context.Context, mgr *sshcore.Manager, hostID string, systemdVersion int) error {
	useJSON := linuxsystemd.SupportsJSONOutput(systemdVersion)

	unitArgs := []string{"list-units", "--type=service", "--all"}
	fileArgs := []string{"list-unit-files", "--type=service"}
	if useJSON {
		unitArgs = append(unitArgs, "--output=json")
		fileArgs = append(fileArgs, "--output=json")
	} else {
		unitArgs = append(unitArgs, "--plain", "--no-legend")
		fileArgs = append(fileArgs, "--plain", "--no-legend")
	}

	start := time.Now()
	unitsRes, err := mgr.Exec(ctx, hostID, "systemctl", unitArgs...)
	if err != nil {
		return err
	}
	if err := unitsRes.Err(); err != nil {
		return err
	}
	filesRes, err := mgr.Exec(ctx, hostID, "systemctl", fileArgs...)
	if err != nil {
		return err
	}
	if err := filesRes.Err(); err != nil {
		return err
	}
	elapsed := time.Since(start)

	var loaded []linuxsystemd.ServiceUnit
	var files map[string]linuxsystemd.ServiceUnit
	if useJSON {
		if loaded, err = linuxsystemd.ParseListUnits(unitsRes.Stdout); err != nil {
			return err
		}
		if files, err = linuxsystemd.ParseUnitFiles(filesRes.Stdout); err != nil {
			return err
		}
	} else {
		if loaded, err = linuxsystemd.ParseListUnitsTable(unitsRes.Stdout); err != nil {
			return err
		}
		if files, err = linuxsystemd.ParseUnitFilesTable(filesRes.Stdout); err != nil {
			return err
		}
	}

	merged := linuxsystemd.MergeServices(loaded, files)
	var active, failed, enabled int
	for _, u := range merged {
		switch {
		case u.Failed():
			failed++
		case u.Active == "active":
			active++
		}
		if u.Enabled == "enabled" {
			enabled++
		}
	}

	format := "표(폴백)"
	if useJSON {
		format = "JSON"
	}
	fmt.Printf("  포맷: %s — 로드됨 %d + 유닛파일 %d → 병합 %d개, %v\n",
		format, len(loaded), len(files), len(merged), elapsed.Round(time.Millisecond))
	fmt.Printf("  active %d · failed %d · enabled %d\n", active, failed, enabled)

	shown := 0
	for _, u := range merged {
		if u.Active != "active" {
			continue
		}
		fmt.Printf("  ● %-42s %-8s %-9s %s\n", u.Name, u.Active, u.Enabled, u.Description)
		if shown++; shown >= 6 {
			break
		}
	}
	return nil
}

// concurrencyCheck is the M0 multiplexing question: does one TCP connection
// really carry many commands at once, and does the session limit hold?
func concurrencyCheck(ctx context.Context, mgr *sshcore.Manager, hostID string, n int) error {
	seq := time.Now()
	if _, err := mgr.Exec(ctx, hostID, "true"); err != nil {
		return err
	}
	oneCmd := time.Since(seq)

	var wg sync.WaitGroup
	errs := make([]error, n)
	start := time.Now()
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = mgr.Exec(ctx, hostID, "cat", "/proc/loadavg")
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	failed := 0
	var firstErr error
	for _, err := range errs {
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	fmt.Printf("  명령 1회      : %v\n", oneCmd.Round(time.Millisecond))
	fmt.Printf("  동시 %-2d회    : %v (순차 예상 %v)\n", n, elapsed.Round(time.Millisecond),
		(time.Duration(n) * oneCmd).Round(time.Millisecond))
	fmt.Printf("  세션 상한     : %d (초과분은 로컬 대기)\n", sshcore.DefaultMaxSessions)
	if failed > 0 {
		fmt.Printf("  실패 %d건: %v\n", failed, firstErr)
		return fmt.Errorf("%d개 동시 명령이 실패했다", failed)
	}
	fmt.Printf("  실패          : 없음\n")
	return nil
}

// commandLog prints what the GUI's Command Log panel would show (§4.6).
type commandLog struct{ mu sync.Mutex }

func (l *commandLog) CommandStarted(sshcore.CommandInfo) {}

func (l *commandLog) CommandFinished(info sshcore.CommandInfo, res *sshcore.Result, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tag := ""
	if info.Kind != sshcore.CommandAction {
		tag = " (" + string(info.Kind) + ")"
	}
	switch {
	case err != nil:
		fmt.Printf("  $ %-56s ERROR %v%s\n", info.Line, err, tag)
	case res != nil:
		fmt.Printf("  $ %-56s exit=%d %v%s\n", info.Line, res.ExitCode, res.Duration.Round(time.Millisecond), tag)
	}
}

func authMethods(keyPath, password string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if keyPath != "" {
		m, err := sshcore.PublicKeyFile(keyPath, func() (string, error) {
			return promptSecret(fmt.Sprintf("passphrase for %s: ", keyPath))
		})
		if err != nil {
			return nil, err
		}
		methods = append(methods, m)
	}
	if m, err := sshcore.Agent(); err == nil {
		methods = append(methods, m)
	}
	if password != "" {
		methods = append(methods, sshcore.Password(func() (string, error) { return password, nil }))
	} else if keyPath == "" {
		methods = append(methods, sshcore.Password(func() (string, error) {
			return promptSecret("password: ")
		}))
	}
	methods = append(methods, sshcore.KeyboardInteractive(
		func(name, instruction string, prompts []sshcore.Prompt) ([]string, error) {
			if instruction != "" {
				fmt.Println(instruction)
			}
			answers := make([]string, len(prompts))
			for i, p := range prompts {
				a, err := promptSecret("  " + p.Question)
				if err != nil {
					return nil, err
				}
				answers[i] = a
			}
			return answers, nil
		}))
	if len(methods) == 0 {
		return nil, fmt.Errorf("no authentication method available")
	}
	return methods, nil
}

func hostKeyCallback(path string, insecure bool) (ssh.HostKeyCallback, error) {
	if insecure {
		fmt.Fprintln(os.Stderr, "warning: -insecure disables host key verification")
		return ssh.InsecureIgnoreHostKey(), nil
	}
	kh, err := sshcore.NewKnownHosts(path, stdinPrompter{})
	if err != nil {
		return nil, err
	}
	return kh.Callback(), nil
}

// stdinPrompter implements the trust-on-first-use dialog from §7.1 on a terminal.
type stdinPrompter struct{}

func (stdinPrompter) ConfirmNewHost(k sshcore.KeyInfo) (sshcore.TrustDecision, error) {
	fmt.Printf("\n처음 접속하는 호스트입니다: %s\n", k.Address)
	fmt.Printf("  키 종류 : %s\n", k.Type)
	fmt.Printf("  지문    : %s\n", k.Fingerprint)
	fmt.Print("신뢰하시겠습니까? [y=항상 / o=이번만 / N=거부]: ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return sshcore.TrustReject, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return sshcore.TrustAlways, nil
	case "o", "once":
		return sshcore.TrustOnce, nil
	default:
		return sshcore.TrustReject, nil
	}
}

func promptSecret(label string) (string, error) {
	// Not using a no-echo terminal read yet: the probe is a developer tool and
	// the GUI has its own password field. Noted so it is not mistaken for the
	// production path.
	fmt.Print(label)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line), err
}

func defaultKnownHosts() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "known_hosts"
	}
	return filepath.Join(dir, "litedeck", "known_hosts")
}

func osReleaseField(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && k == key {
			return strings.Trim(v, `"`)
		}
	}
	return "unknown"
}

func jsonNote(version int) string {
	switch {
	case version == 0:
		return ""
	case linuxsystemd.SupportsJSONOutput(version):
		return " (JSON 출력 지원)"
	default:
		return fmt.Sprintf(" (JSON 미지원 — %d 미만, 표 파싱으로 폴백)", linuxsystemd.MinJSONOutputVersion)
	}
}

func yesNo(b bool) string {
	if b {
		return "예"
	}
	return "아니오"
}

func section(title string) {
	fmt.Printf("\n\033[1m%s\033[0m\n", title)
}
