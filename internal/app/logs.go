package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/secret"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// Live log following (§4.3's detail panel, §4.5's container logs).
//
// The alternative — polling `journalctl -n 500` every few seconds — re-reads
// and re-transfers the same lines forever and still shows them late. A follow
// costs one channel and delivers each line once, as it happens.

// LogStream identifies an open follow to the frontend.
type LogStream struct {
	ID     string `json:"id"`
	HostID string `json:"hostId"`
	Title  string `json:"title"`
}

// LogLine is the payload of a log:data:<id> event.
type LogLine struct {
	Text   string `json:"text"`
	Stderr bool   `json:"stderr"`
}

type logRegistry struct {
	app *App
	mu  sync.Mutex
	seq int
	all map[string]*sshcore.Stream
}

func newLogRegistry(a *App) *logRegistry {
	return &logRegistry{app: a, all: make(map[string]*sshcore.Stream)}
}

func (r *logRegistry) drop(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.all, id)
}

func (r *logRegistry) closeAll() {
	r.mu.Lock()
	streams := make([]*sshcore.Stream, 0, len(r.all))
	for _, s := range r.all {
		streams = append(streams, s)
	}
	r.all = make(map[string]*sshcore.Stream)
	r.mu.Unlock()
	for _, s := range streams {
		_ = s.Close()
	}
}

// start is the shared body behind the unit and container variants.
func (a *App) startLogStream(hostID, title string, stdin io.Reader, cmd string, args ...string) (LogStream, error) {
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return LogStream{}, err
	}

	a.logs.mu.Lock()
	a.logs.seq++
	id := "log" + strconv.Itoa(a.logs.seq)
	a.logs.mu.Unlock()

	// Background, not the dial context: a follow outlives the call that opened
	// it and ends when the user closes the panel.
	ctx, cancel := context.WithCancel(context.Background())

	stream, err := conn.OpenStreamOpts(ctx, sshcore.StreamOptions{Stdin: stdin},
		func(line string, isStderr bool) {
			a.emit("log:data:"+id, LogLine{Text: line, Stderr: isStderr})
		},
		func(err error) {
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			a.logs.drop(id)
			cancel()
			a.emit("log:exit:"+id, msg)
		},
		cmd, args...,
	)
	if err != nil {
		cancel()
		return LogStream{}, err
	}

	a.logs.mu.Lock()
	a.logs.all[id] = stream
	a.logs.mu.Unlock()

	return LogStream{ID: id, HostID: hostID, Title: title}, nil
}

// ErrJournalUnreadable reports that this user cannot see the system journal.
var ErrJournalUnreadable = errors.New("app: journal not readable by this user")

// FollowServiceLog streams a systemd unit's journal (§4.3).
//
// elevate runs it through sudo. It is needed more often than one would expect:
// a user outside systemd-journal/adm sees only their *own* messages, and
// journalctl does not fail — `journalctl -u nginx` simply returns nothing,
// which reads as "this service never logged anything". Detect() records the
// group membership so the UI can say so before the user draws that conclusion.
func (a *App) FollowServiceLog(hostID, unit string, tail int, elevate bool) (LogStream, error) {
	if tail <= 0 || tail > 5000 {
		tail = 200
	}
	info, err := a.DetectHost(hostID)
	if err != nil {
		return LogStream{}, err
	}
	if !info.HasSystemd {
		return LogStream{}, fmt.Errorf("app: %s has no journal to follow", hostID)
	}
	if !info.CanReadJournal && !elevate {
		return LogStream{}, fmt.Errorf(
			i18n.S("%w: 이 사용자는 systemd-journal·adm 그룹에 없어 시스템 저널을 볼 수 없습니다 — 관리자 권한으로 열거나, 서버에서 그룹에 추가하세요"),
			ErrJournalUnreadable)
	}

	// -q suppresses the "you are not seeing messages from other users" notice,
	// which journalctl prints *into the output stream* and would otherwise be
	// the first thing in the log panel.
	// --no-pager matters too: journalctl pipes through less when it thinks it
	// has a terminal, and the stream would hang waiting for a key.
	args := []string{"-u", unit, "-n", strconv.Itoa(tail), "-f", "--no-pager", "-q"}

	if !elevate {
		return a.startLogStream(hostID, unit, nil, "journalctl", args...)
	}
	return a.startElevatedStream(hostID, unit, "journalctl", args...)
}

// startElevatedStream runs a follow through sudo, with the password on stdin.
//
// The stream keeps running after sudo has consumed the password, so the reader
// is a pipe that stays open rather than a one-shot string.
func (a *App) startElevatedStream(hostID, title, cmd string, args ...string) (LogStream, error) {
	info, err := a.DetectHost(hostID)
	if err != nil {
		return LogStream{}, err
	}
	if info.SudoNoPasswd {
		return a.startLogStream(hostID, title, nil, "sudo",
			append([]string{"-n", "--", cmd}, args...)...)
	}

	password, err := a.prompts.secretFunc(hostID, secret.KindSudo, i18n.S("sudo 비밀번호"))()
	if err != nil {
		return LogStream{}, err
	}
	return a.startLogStream(hostID, title, strings.NewReader(password+"\n"), "sudo",
		append([]string{"-S", "-p", "", "--", cmd}, args...)...)
}

// FollowContainerLog streams a container's output (§4.5).
func (a *App) FollowContainerLog(hostID, containerID string, tail int) (LogStream, error) {
	if tail <= 0 || tail > 5000 {
		tail = 200
	}
	runtime, err := a.containerRuntime(hostID)
	if err != nil {
		return LogStream{}, err
	}
	return a.startLogStream(hostID, shortID(containerID), nil,
		runtime, "logs", "--tail", strconv.Itoa(tail), "-f", "--", containerID)
}

// StopLogStream ends a follow.
func (a *App) StopLogStream(id string) error {
	a.logs.mu.Lock()
	stream, ok := a.logs.all[id]
	delete(a.logs.all, id)
	a.logs.mu.Unlock()
	if !ok {
		return nil // already gone; closing twice is not worth an error
	}
	return stream.Close()
}
