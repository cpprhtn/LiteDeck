package app

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// Downloading and installing a release (T-43).
//
// # What this does and does not promise
//
// It fetches the asset for this build, checks it against the SHA256SUMS.txt
// published beside it, unpacks it, and — when the user presses the second
// button — swaps it in and restarts.
//
// The checksum comes from the same host as the binary. That catches a truncated
// download and a corrupted mirror; it does not catch a compromised release,
// because whoever could replace the asset could replace the sums file with it.
// Releases are unsigned today (notarisation and Authenticode both cost money,
// and release.yml says so). The honest next step is a minisign signature with
// the public key compiled into this binary — free, and the only thing that
// would make an unattended update defensible. Until then the last step is a
// button somebody presses.
//
// # Why it never installs by itself
//
// Silently replacing an unsigned binary is the shape of a supply-chain attack.
// The download is automatic once asked for; the swap is not.

// UpdateStage is where the update got to, for the one button that drives it.
type UpdateStage string

const (
	StageIdle        UpdateStage = "idle"
	StageDownloading UpdateStage = "downloading"
	StageReady       UpdateStage = "ready"
	StageFailed      UpdateStage = "failed"
)

// UpdateState is the whole of what the button needs to know.
type UpdateState struct {
	Stage UpdateStage `json:"stage"`
	// Percent is 0-100 while downloading, and -1 where the server sent no
	// length — a progress bar that invents its own numbers is worse than none.
	Percent int `json:"percent"`
	// Error is a sentence for the user, already translated.
	Error string `json:"error,omitempty"`
	// Version is what was staged, so the button can name it.
	Version string `json:"version,omitempty"`
}

// updateEventName is what the frontend listens on for progress.
const updateEventName = "update:state"

type updateInstaller struct {
	mu     sync.Mutex
	state  UpdateState
	staged string // path to the unpacked replacement, empty until ready
	busy   bool
}

// UpdateStatus reports where the installer is, for a UI that just mounted.
func (a *App) UpdateStatus() UpdateState {
	a.installer.mu.Lock()
	defer a.installer.mu.Unlock()
	if a.installer.state.Stage == "" {
		return UpdateState{Stage: StageIdle}
	}
	return a.installer.state
}

func (a *App) setUpdateState(s UpdateState) {
	a.installer.mu.Lock()
	a.installer.state = s
	a.installer.mu.Unlock()
	if a.emit != nil {
		a.emit(updateEventName, s)
	}
}

// DownloadUpdate fetches and verifies the newest release, ready to be applied.
//
// Returns as soon as the work is started; everything after that arrives on
// updateEventName. A download that reported its result only at the end would
// leave the button dead for however long the network takes.
func (a *App) DownloadUpdate() error {
	a.installer.mu.Lock()
	if a.installer.busy {
		a.installer.mu.Unlock()
		return nil // already running; the events say where it is
	}
	a.installer.busy = true
	a.installer.mu.Unlock()

	go func() {
		defer func() {
			a.installer.mu.Lock()
			a.installer.busy = false
			a.installer.mu.Unlock()
		}()
		path, version, err := a.fetchRelease()
		if err != nil {
			a.setUpdateState(UpdateState{Stage: StageFailed, Error: err.Error()})
			return
		}
		a.installer.mu.Lock()
		a.installer.staged = path
		a.installer.mu.Unlock()
		a.setUpdateState(UpdateState{Stage: StageReady, Percent: 100, Version: version})
	}()
	return nil
}

func (a *App) fetchRelease() (staged, version string, err error) {
	a.setUpdateState(UpdateState{Stage: StageDownloading, Percent: -1})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	rel, err := latestRelease(ctx)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", i18n.T("릴리스 정보를 읽지 못했습니다"), err)
	}
	wanted, err := assetName()
	if err != nil {
		return "", "", err
	}
	assetURL, sumsURL := rel.find(wanted), rel.find("SHA256SUMS.txt")
	if assetURL == "" {
		return "", "", fmt.Errorf(i18n.T("이 릴리스에 %s가 없습니다"), wanted)
	}
	if sumsURL == "" {
		return "", "", errors.New(i18n.T("체크섬 파일이 없어 내려받은 것을 검증할 수 없습니다"))
	}

	want, err := checksumFor(ctx, sumsURL, wanted)
	if err != nil {
		return "", "", err
	}

	dir, err := os.MkdirTemp("", "litedeck-update-")
	if err != nil {
		return "", "", err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()

	archive := filepath.Join(dir, wanted)
	sum, err := a.download(ctx, assetURL, archive)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", i18n.T("내려받지 못했습니다"), err)
	}
	if !strings.EqualFold(sum, want) {
		// Not a retry. A file that does not match its published sum is either a
		// broken transfer or something worse, and both mean "stop".
		return "", "", errors.New(i18n.T("체크섬이 맞지 않습니다 — 내려받은 파일을 버렸습니다"))
	}

	out := filepath.Join(dir, "staged")
	if err := unpack(archive, out); err != nil {
		return "", "", fmt.Errorf("%s: %w", i18n.T("압축을 풀지 못했습니다"), err)
	}
	os.Remove(archive)

	target, err := installedPath()
	if err != nil {
		return "", "", err
	}
	replacement := filepath.Join(out, filepath.Base(target))
	if _, err := os.Stat(replacement); err != nil {
		return "", "", fmt.Errorf("%s: %s", i18n.T("받은 압축 안에 기대한 파일이 없습니다"), filepath.Base(target))
	}
	ok = true
	return replacement, rel.Tag(), nil
}

// download writes the body to path and returns its SHA-256, reporting progress
// as it goes.
func (a *App) download(ctx context.Context, url, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "LiteDeck/"+Version)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", res.Status)
	}

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	total := res.ContentLength
	var done int64
	last := -1
	buf := make([]byte, 256<<10)
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return "", werr
			}
			h.Write(buf[:n])
			done += int64(n)
			pct := -1
			if total > 0 {
				pct = int(done * 100 / total)
			}
			// Only on a change: at 256KB a chunk this fires a few hundred times
			// for a 14MB build, and every one of them crosses to the webview.
			if pct != last {
				last = pct
				a.setUpdateState(UpdateState{Stage: StageDownloading, Percent: pct})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ApplyUpdate swaps the staged copy in and restarts.
//
// The running program cannot replace itself while it is running — on Windows
// the file is locked, and on macOS the bundle is open — so a small helper does
// it: it waits for this process to exit, moves the old copy aside, moves the
// new one in, starts it, and removes the leftovers. If anything fails before
// the move, the old copy is still there and untouched.
func (a *App) ApplyUpdate() error {
	a.installer.mu.Lock()
	staged := a.installer.staged
	a.installer.mu.Unlock()
	if staged == "" {
		return errors.New(i18n.T("아직 받아 둔 업데이트가 없습니다"))
	}
	target, err := installedPath()
	if err != nil {
		return err
	}
	script, err := writeSwapHelper(staged)
	if err != nil {
		return err
	}

	cmd := helperCommand(script, swapArgs(os.Getpid(), target, staged))
	// Detached: it has to outlive this process, which is the whole point.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()

	a.setUpdateState(UpdateState{Stage: StageIdle})
	if a.quitFn != nil {
		a.quitFn()
	}
	return nil
}

// installedPath is what gets replaced: the .app bundle on macOS, the executable
// everywhere else.
func installedPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		// .../LiteDeck.app/Contents/MacOS/litedeck → .../LiteDeck.app
		dir := filepath.Dir(exe)
		if filepath.Base(dir) == "MacOS" {
			contents := filepath.Dir(dir)
			if filepath.Base(contents) == "Contents" {
				return filepath.Dir(contents), nil
			}
		}
	}
	return exe, nil
}

// assetName is the release file for this build.
func assetName() (string, error) {
	switch {
	case runtime.GOOS == "darwin":
		// One universal bundle for both architectures.
		return "litedeck-desktop-macos.zip", nil
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		return "litedeck-desktop-windows-amd64.zip", nil
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "litedeck-desktop-linux-amd64.tar.gz", nil
	}
	return "", fmt.Errorf("%s: %s/%s", i18n.T("이 플랫폼용 자동 업데이트는 아직 없습니다"), runtime.GOOS, runtime.GOARCH)
}

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (r *release) Tag() string { return strings.TrimPrefix(strings.TrimSpace(r.TagName), "v") }

func (r *release) find(name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL
		}
	}
	return ""
}

func latestRelease(ctx context.Context) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "LiteDeck/"+Version)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", res.Status)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// checksumFor reads SHA256SUMS.txt and returns the hash recorded for name.
func checksumFor(ctx context.Context, url, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "LiteDeck/"+Version)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("SHA256SUMS.txt: %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		// "<64 hex>  <name>", the format both sha256sum and shasum print.
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("%s: %s", i18n.T("체크섬 목록에 없습니다"), name)
}

// unpack extracts archive into dir, which it creates.
func unpack(archive, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(archive, ".zip") {
		return unzip(archive, dir)
	}
	return untargz(archive, dir)
}

// safeJoin resolves an archive entry inside dir, or refuses it.
//
// The same guard as the transfer path: an archive is a list of names somebody
// else wrote, and "../../.ssh/authorized_keys" is a valid entry name.
//
// It refuses rather than clamping. Folding "../../x" down to "<dir>/x" is safe
// in the sense that nothing lands outside — but this archive is supposed to be
// our own release, so an entry that climbs is not a path to repair, it is a
// reason to stop before replacing the application with whatever else is in
// there.
func safeJoin(dir, name string) (string, error) {
	bad := func() (string, error) {
		return "", fmt.Errorf("app: archive entry escapes its directory: %q", name)
	}
	// Zip stores "/" on every platform; filepath handles both on Windows.
	clean := filepath.Clean(strings.ReplaceAll(name, "\\", "/"))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") {
		return bad()
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || strings.HasPrefix(clean, "../") {
		return bad()
	}
	p := filepath.Join(dir, clean)
	if p != dir && !strings.HasPrefix(p, dir+string(os.PathSeparator)) {
		return bad()
	}
	return p, nil
}

func unzip(archive, dir string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		p, err := safeJoin(dir, f.Name)
		if err != nil {
			return err
		}
		info := f.FileInfo()
		switch {
		case info.IsDir():
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			if err := writeSymlink(f.Open, p); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			// The mode carries the executable bit, and on macOS the bundle is
			// dead without it.
			err = writeFile(p, rc, info.Mode().Perm())
			rc.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func untargz(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := safeJoin(dir, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			os.Remove(p)
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := writeFile(p, tr, os.FileMode(h.Mode).Perm()); err != nil {
				return err
			}
		}
	}
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	// Bounded: a release asset this app builds is tens of megabytes, and an
	// archive claiming otherwise is not one of ours.
	if _, err := io.Copy(f, io.LimitReader(r, 512<<20)); err != nil {
		return err
	}
	return f.Close()
}

func writeSymlink(open func() (io.ReadCloser, error), path string) error {
	rc, err := open()
	if err != nil {
		return err
	}
	defer rc.Close()
	target, err := io.ReadAll(io.LimitReader(rc, 4<<10))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	os.Remove(path)
	return os.Symlink(string(target), path)
}

// helperCommand runs the swap script, without a console window on Windows.
func helperCommand(script string, args []string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", append([]string{"/c", "start", "/min", "", script}, args...)...)
	}
	return exec.Command("/bin/sh", append([]string{script}, args...)...)
}
