// Suggestions while typing in the terminal: a short list of likely lines above
// the prompt, picked with ↑ ↓ and taken with →. Tab stays the shell's.
//
// Two sources, each switched on its own in the terminal settings. What was run
// on this host before — this app's own record first, then the shell's history
// file where the host allows it to be read — and a short list of lines people
// type on any Linux box. History wins: a line this person ran here is a better
// guess than one everybody runs.
//
// Only ever a suggestion. Nothing is sent until a key says so, and what a key
// sends is exactly the rest of the line picked in the list.

import { HostShellHistory, TypedHistory } from './ipc'

/** Lines common enough on a Linux server to be worth offering to anybody,
 *  roughly most-typed first — the order the list shows them in. */
export const COMMON_COMMANDS: readonly string[] = [
  'ls -la',
  'ls -lh',
  'cd ..',
  'cd ~',
  'clear',
  'pwd',
  'exit',
  'sudo -i',
  'sudo su -',
  'cat /etc/os-release',
  'df -h',
  'du -sh *',
  'du -sh',
  'free -h',
  'top',
  'htop',
  'uptime',
  'whoami',
  'hostname -I',
  'ip a',
  'ip route',
  'ss -tulpn',
  'ps aux',
  'ps aux | grep ',
  'kill -9 ',
  'pkill ',
  'grep -rn ',
  'find . -name ',
  'tail -f ',
  'tail -n 100 ',
  'less ',
  'head ',
  'mkdir -p ',
  'rm -rf ',
  'cp -r ',
  'mv ',
  'chmod +x ',
  'chmod 755 ',
  'chown -R ',
  'ln -s ',
  'tar -xzf ',
  'tar -czf ',
  'tar -xf ',
  'tar -tzf ',
  'tar -xJf ',
  'tar -xjf ',
  'tar -cvf ',
  'unzip ',
  'wget ',
  'curl -I ',
  'curl -fsSL ',
  'ping -c 4 ',
  'nano ',
  'history',
  'which ',
  'man ',
  'echo $PATH',
  'env | grep ',
  'export ',
  'source ~/.bashrc',
  'systemctl status ',
  'systemctl restart ',
  'systemctl start ',
  'systemctl stop ',
  'systemctl enable --now ',
  'systemctl daemon-reload',
  'systemctl list-units --type=service',
  'journalctl -u ',
  'journalctl -f',
  'journalctl -xe',
  'sudo apt update',
  'sudo apt upgrade -y',
  'sudo apt install ',
  'sudo apt remove ',
  'apt search ',
  'sudo dnf install ',
  'sudo dnf update',
  'sudo yum install ',
  'sudo pacman -Syu',
  'docker ps',
  'docker ps -a',
  'docker images',
  'docker logs -f ',
  'docker exec -it ',
  'docker compose up -d',
  'docker compose down',
  'docker compose logs -f',
  'docker compose ps',
  'docker system prune',
  'kubectl get pods',
  'kubectl get pods -A',
  'kubectl get svc',
  'kubectl describe pod ',
  'kubectl logs -f ',
  'git status',
  'git pull',
  'git log --oneline',
  'git diff',
  'git add .',
  'git commit -m ""',
  'git push',
  'git checkout ',
  'git clone ',
  'ssh ',
  'scp ',
  'rsync -avz ',
  'crontab -e',
  'crontab -l',
  'ufw status',
  'sudo ufw allow ',
  'nvidia-smi',
  'lsblk',
  'mount',
  'uname -a',
  'lscpu',
  'dmesg | tail',
  'last',
  'w',
]

/** What each pane needs to know about one host, shared by every pane on it. */
interface HostHistory {
  /** Distinct lines, most recent first. */
  lines: string[]
  loading: Promise<void> | null
  loaded: boolean
}

const hosts = new Map<string, HostHistory>()

/** How many history lines are kept for matching. The shell file is capped in Go
 *  already; this only bounds what a long session adds on top. */
const MAX_LINES = 3000

function entry(hostID: string): HostHistory {
  let h = hosts.get(hostID)
  if (!h) {
    h = { lines: [], loading: null, loaded: false }
    hosts.set(hostID, h)
  }
  return h
}

/** A line worth suggesting again. A masked secret is not: Go replaced it with a
 *  placeholder, and offering that back would type the placeholder. */
function usable(line: string): boolean {
  const s = line.trim()
  return s.length > 1 && s.length <= 500 && !s.includes('\n') && !s.includes('••••')
}

/** Reads the host's history once per run. Failures leave it empty — a host
 *  whose history cannot be read still gets the common commands. */
export function loadHistory(hostID: string): Promise<void> {
  const h = entry(hostID)
  if (h.loaded) return Promise.resolve()
  if (h.loading) return h.loading
  h.loading = (async () => {
    const [typed, file] = await Promise.all([
      TypedHistory(hostID).catch(() => []),
      // Never elevated: root's history is a separate ask in the history panel,
      // and typing a letter is not that ask.
      HostShellHistory(hostID, false).catch(() => null),
    ])
    // Both newest first. This app's record goes first because it is the more
    // recent of the two — the shell writes its file when a session ends.
    const merged = [...typed.map((x) => x.command), ...(file?.commands ?? []).map((x) => x.command)]
    const seen = new Set(h.lines)
    // Lines remembered while this was loading are newer than anything read.
    const out = [...h.lines]
    for (const line of merged) {
      const s = line.trim()
      if (!usable(s) || seen.has(s)) continue
      seen.add(s)
      out.push(s)
      if (out.length >= MAX_LINES) break
    }
    h.lines = out
    h.loaded = true
    h.loading = null
  })()
  return h.loading
}

/** The host's history has been read, or could not be. */
export function historyLoaded(hostID: string): boolean {
  return hosts.get(hostID)?.loaded ?? false
}

/** A line just entered in one of this host's terminals goes to the front.
 *
 *  Read back from Go's record rather than taken as typed. Go masks secrets on
 *  the way out; a line that had one comes back with the placeholder and
 *  `usable` turns it away. Called once TypedEntered has recorded the line, so
 *  the newest row is that line. */
export async function rememberLatest(hostID: string) {
  const [last] = await TypedHistory(hostID).catch(() => [])
  if (!last) return
  const s = last.command.trim()
  if (!usable(s)) return
  const h = entry(hostID)
  h.lines = [s, ...h.lines.filter((x) => x !== s)].slice(0, MAX_LINES)
}

export interface SuggestOptions {
  history: boolean
  commands: boolean
}

/** How many lines the list shows at once. */
export const MAX_SUGGESTIONS = 8

/**
 * Whole lines that start with `typed`, best first: this host's history, then
 * the common commands, each line once.
 *
 * A prefix match, case and all — the shell is case-sensitive and so is this.
 * Nothing for an empty line or one that is only spaces: a suggestion before
 * anything was typed is the history panel's job, not the prompt's.
 */
export function suggestions(hostID: string, typed: string, opts: SuggestOptions): string[] {
  if (!typed.trim()) return []
  const out: string[] = []
  const seen = new Set<string>()
  const take = (list: readonly string[]) => {
    for (const line of list) {
      if (out.length >= MAX_SUGGESTIONS) return
      if (line.length <= typed.length || !line.startsWith(typed)) continue
      // `ls -la` from history and `ls -la` from the list are one suggestion.
      const key = line.trimEnd()
      if (seen.has(key)) continue
      seen.add(key)
      out.push(line)
    }
  }
  if (opts.history) take(hosts.get(hostID)?.lines ?? [])
  if (opts.commands) take(COMMON_COMMANDS)
  return out
}
