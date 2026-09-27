package cfgsync

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
)

// The sync over a real SSH remote (§10.2).
//
// The file:// tests cover the sequence — clone, read, commit, push, collide. What
// this covers is the half that only shows up over a network: key authentication,
// the host key of the git server, and a bare repository somebody made with
// `git init --bare` on their own box, which is the setup the settings screen
// tells people to use.
//
// Skips without Docker, like every other integration test here. CI is where it is
// mandatory.
func TestSyncOverSSHToABareRepositoryOnAServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped by -short")
	}
	remote, auth, hostKey := startGitRemote(t)

	// The host key of the git server is checked, not ignored (§5.2): the sync
	// repository goes through the same trust this app applies to every other
	// server, and nothing is trusted for being called github.com.
	auth.HostKeyCallback = hostKey

	vf, vault, err := CreateVault(goodPass)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}

	ctx := context.Background()
	dirA := filepath.Join(t.TempDir(), "a")
	a, err := OpenGit(ctx, GitOptions{Dir: dirA, URL: remote, Auth: auth})
	if err != nil && !errors.Is(err, ErrEmptyRemote) {
		t.Fatalf("open a: %v", err)
	}
	defer a.Close()

	vaultJSON, err := jsonMarshalIndent(vf)
	if err != nil {
		t.Fatalf("encode vault: %v", err)
	}
	recordPath, err := RecordPath(testID)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	plain, err := sampleRecord().Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	blob, err := vault.Encrypt(recordPath, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := a.CommitAndPush(ctx, map[string][]byte{
		AttributesPath: []byte(GitAttributes),
		VaultPath:      vaultJSON,
		recordPath:     blob,
	}, nil, "sync: a"); err != nil {
		t.Fatalf("push over ssh: %v", err)
	}

	// A second machine, cloning the same repository over SSH with the same key.
	dirB := filepath.Join(t.TempDir(), "b")
	b, err := OpenGit(ctx, GitOptions{Dir: dirB, URL: remote, Auth: auth})
	if err != nil {
		t.Fatalf("open b: %v", err)
	}
	defer b.Close()
	if _, err := b.Pull(ctx); err != nil {
		t.Fatalf("b pull: %v", err)
	}
	files, err := b.ReadAll()
	if err != nil {
		t.Fatalf("b read: %v", err)
	}

	// The record has to survive the round trip byte for byte. This is where
	// .gitattributes earns itself: a transport or a checkout that rewrote line
	// endings would leave the file looking fine and the AEAD refusing it.
	got, err := vault.Decrypt(recordPath, files[recordPath])
	if err != nil {
		t.Fatalf("the record did not survive the round trip over ssh: %v", err)
	}
	if string(got) != string(plain) {
		t.Errorf("the record came back changed:\n%s", got)
	}
	if string(files[AttributesPath]) != GitAttributes {
		t.Errorf(".gitattributes = %q", files[AttributesPath])
	}

	// And a wrong key is refused, so the test above is not passing because the
	// server accepts anybody.
	_, badPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	badSigner, err := ssh.NewSignerFromKey(badPriv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	bad := &gitssh.PublicKeys{User: "git", Signer: badSigner}
	bad.HostKeyCallback = hostKey
	if _, err := OpenGit(ctx, GitOptions{
		Dir: filepath.Join(t.TempDir(), "c"), URL: remote, Auth: bad,
	}); err == nil {
		t.Error("the server accepted a key it was never given")
	}
}

// startGitRemote builds and runs the fixture, returning the URL, an auth method
// holding a key the server will accept, and a callback that accepts only that
// container's host keys.
func startGitRemote(t *testing.T) (string, *gitssh.PublicKeys, ssh.HostKeyCallback) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker not running: %s", firstLineOf(out))
	}

	const image = "litedeck-test-gitremote"
	ctxDir := filepath.Join("..", "..", "testdata", "gitremote")
	if out, err := exec.Command("docker", "build", "-q", "-t", image, ctxDir).CombinedOutput(); err != nil {
		t.Skipf("docker build: %s", firstLineOf(out))
	}
	out, err := exec.Command("docker", "run", "-d", "-P", image).CombinedOutput()
	if err != nil {
		t.Skipf("docker run: %s", firstLineOf(out))
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	portOut, err := exec.Command("docker", "port", id, "22/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	hostPort := strings.TrimSpace(firstLineOf(portOut))
	port := hostPort[strings.LastIndex(hostPort, ":")+1:]

	// The key this "device" will use, generated here so nothing is baked into the
	// image — the same thing SyncGenerateKey does on a real machine.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "litedeck sync test")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(pem.EncodeToMemory(block))
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}

	authorized := string(ssh.MarshalAuthorizedKey(sshPub))
	install := exec.Command("docker", "exec", "-i", id, "sh", "-c",
		"cat > /home/git/.ssh/authorized_keys && chown git:git /home/git/.ssh/authorized_keys "+
			"&& chmod 600 /home/git/.ssh/authorized_keys")
	install.Stdin = strings.NewReader(authorized)
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("install authorized_keys: %s", firstLineOf(out))
	}

	// Every host key the container has, not just the ed25519 one: which type sshd
	// offers depends on what both sides support, and pinning one of them made
	// this fail as a host key mismatch — which is exactly what the check is for,
	// and exactly not what the test meant.
	keyOut, err := exec.Command("docker", "exec", id, "sh", "-c",
		"cat /etc/ssh/ssh_host_*_key.pub").Output()
	if err != nil {
		t.Fatalf("read host keys: %v", err)
	}
	var hostKeys []ssh.PublicKey
	rest := keyOut
	for len(rest) > 0 {
		k, _, _, next, err := ssh.ParseAuthorizedKey(rest)
		if err != nil {
			break
		}
		hostKeys = append(hostKeys, k)
		rest = next
	}
	if len(hostKeys) == 0 {
		t.Fatal("the container has no host keys")
	}
	hostKey := hostKeysCallback(hostKeys)

	// sshd takes a moment to bind.
	url := fmt.Sprintf("ssh://git@127.0.0.1:%s/home/git/sync.git", port)
	auth := &gitssh.PublicKeys{User: "git", Signer: signer}
	auth.HostKeyCallback = hostKey
	deadline := time.Now().Add(30 * time.Second)
	for {
		probe, err := OpenGit(context.Background(), GitOptions{
			Dir: filepath.Join(t.TempDir(), "probe"), URL: url, Auth: auth,
		})
		if err == nil || errors.Is(err, ErrEmptyRemote) {
			if probe != nil {
				_ = probe.Close()
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the git remote never came up: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return url, auth, hostKey
}

// hostKeysCallback accepts exactly the keys it was given.
func hostKeysCallback(keys []ssh.PublicKey) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, offered ssh.PublicKey) error {
		for _, k := range keys {
			if bytes.Equal(k.Marshal(), offered.Marshal()) {
				return nil
			}
		}
		return fmt.Errorf("unexpected host key %s", ssh.FingerprintSHA256(offered))
	}
}

func firstLineOf(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
