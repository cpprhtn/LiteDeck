package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// The container view (§4.5).
//
// Docker access is usually granted through group membership rather than sudo,
// so most users never see an elevation prompt here — but when the daemon
// refuses, the same §7.2 path applies as everywhere else.

// containerRuntime picks docker or podman for this host. They share a CLI, so
// only the binary name differs.
func (a *App) containerRuntime(hostID string) (string, error) {
	info, err := a.DetectHost(hostID)
	if err != nil {
		return "", err
	}
	switch {
	case info.HasDocker:
		return "docker", nil
	case info.HasPodman:
		return "podman", nil
	default:
		return "", fmt.Errorf("app: %s has no container runtime", hostID)
	}
}

// ListContainers returns the container table (§4.5).
func (a *App) ListContainers(hostID string) ([]adapter.Container, error) {
	runtime, err := a.containerRuntime(hostID)
	if err != nil {
		return nil, err
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	res, err := conn.Poll(ctx, runtime, adapter.PSArgsContainers()...)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		// The daemon being unreachable is the common failure and deserves a
		// better line than a raw stderr dump.
		if strings.Contains(strings.ToLower(string(res.Stderr)), "permission denied") {
			return nil, i18n.Errorf("%s 데몬에 접근할 권한이 없습니다 — 사용자가 docker 그룹에 있는지 확인하세요", runtime)
		}
		return nil, res.Err()
	}
	return adapter.ParseContainers(res.Stdout)
}

// containerActions is an allowlist. `rm` is included but the UI double-confirms
// it, and there is deliberately no `exec`, `run` or `cp` here — those would turn
// a button into arbitrary remote execution.
var containerActions = map[string]bool{
	"start": true, "stop": true, "restart": true, "pause": true, "unpause": true,
}

// ContainerAction runs one lifecycle verb against one container (§4.5).
func (a *App) ContainerAction(hostID, id, action string, elevate bool) ActionResult {
	if !containerActions[action] {
		return failResult(fmt.Errorf("app: unsupported container action %q", action))
	}
	return a.runContainerCommand(hostID, elevate, action, "--", id)
}

// RemoveContainer deletes a container. Separate from ContainerAction because it
// is destructive and the UI has to confirm it (§7.4).
//
// force maps to `-f`, which kills a running container rather than refusing.
func (a *App) RemoveContainer(hostID, id string, force, elevate bool) ActionResult {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, "--", id)
	return a.runContainerCommand(hostID, elevate, args...)
}

func (a *App) runContainerCommand(hostID string, elevate bool, args ...string) ActionResult {
	runtime, err := a.containerRuntime(hostID)
	if err != nil {
		return failResult(err)
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return failResult(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), PromptTimeout+pollTimeout)
	defer cancel()

	res, err := a.execMaybeElevated(ctx, conn, hostID, elevate, runtime, args...)
	if err != nil {
		return failResult(err)
	}
	return a.classify(hostID, res, elevate)
}

// ContainerLogs returns the tail of a container's log (§4.5).
//
// A bounded tail rather than a stream: following live output belongs to the
// terminal, and an unbounded read of a chatty container would push megabytes
// across the IPC boundary for no benefit.
func (a *App) ContainerLogs(hostID, id string, lines int) (string, error) {
	if lines <= 0 || lines > 5000 {
		lines = 500
	}
	runtime, err := a.containerRuntime(hostID)
	if err != nil {
		return "", err
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	res, err := conn.Exec(ctx, runtime, "logs", "--tail", strconv.Itoa(lines), "--", id)
	if err != nil {
		return "", err
	}
	// Container logs routinely go to stderr — that is where most daemons write.
	// Treating a non-zero exit as fatal would hide the very output the user
	// asked for, so both streams are returned and the exit code is advisory.
	out := string(res.Stdout) + string(res.Stderr)
	if !res.OK() && strings.TrimSpace(out) == "" {
		return "", res.Err()
	}
	return out, nil
}

// Images and volumes (v1.x). Reclaiming disk is the reason people open these.

// ListImages returns the image list (§4.5's v1.x backlog).
func (a *App) ListImages(hostID string) ([]adapter.Image, error) {
	runtime, err := a.containerRuntime(hostID)
	if err != nil {
		return nil, err
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	res, err := conn.Poll(ctx, runtime, adapter.ImagesArgs()...)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return nil, res.Err()
	}
	return adapter.ParseImages(res.Stdout)
}

// ListVolumes returns the volume list, marking which ones nothing references.
//
// Two commands: `docker volume ls` has no in-use column, and knowing what is
// reclaimable is the whole point of the view.
func (a *App) ListVolumes(hostID string) ([]adapter.Volume, error) {
	runtime, err := a.containerRuntime(hostID)
	if err != nil {
		return nil, err
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	res, err := conn.Poll(ctx, runtime, adapter.VolumesArgs()...)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return nil, res.Err()
	}
	// A failure here degrades the in-use column rather than the whole list.
	var dangling []byte
	if d, err := conn.Poll(ctx, runtime, adapter.DanglingVolumesArgs()...); err == nil && d.OK() {
		dangling = d.Stdout
	}
	return adapter.ParseVolumes(res.Stdout, dangling)
}

// RemoveImage deletes an image (§7.4: destructive, the UI confirms).
//
// force is deliberately not exposed: `docker rmi -f` on an image a stopped
// container still references leaves that container unable to start, with an
// error that names a layer hash rather than the image. Letting the daemon
// refuse is the more useful answer.
func (a *App) RemoveImage(hostID, id string, elevate bool) ActionResult {
	return a.runContainerCommand(hostID, elevate, "rmi", "--", id)
}

// RemoveVolume deletes a volume. The daemon refuses if it is in use, which is
// the guard — LiteDeck does not offer a way past it.
func (a *App) RemoveVolume(hostID, name string, elevate bool) ActionResult {
	return a.runContainerCommand(hostID, elevate, "volume", "rm", "--", name)
}

// PruneImages removes dangling layers — the usual answer to a full disk.
func (a *App) PruneImages(hostID string, elevate bool) ActionResult {
	// -f suppresses docker's own y/n prompt, which would hang a non-interactive
	// session forever. The confirmation happens in the UI instead.
	return a.runContainerCommand(hostID, elevate, "image", "prune", "-f")
}
