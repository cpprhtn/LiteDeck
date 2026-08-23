// Typed access to the Go bindings and the Wails event bus.
//
// Wails exposes bound methods on window.go.<package>.<struct>.<Method> and its
// event helpers on window.runtime. Going through this module rather than
// touching those globals keeps the whole binding surface visible in one file.

import type { Platform } from './platform'
import { t } from './i18n'

/* ------------------------------------------------------------------ types */

export type AuthMethod = 'agent' | 'key' | 'password'

export interface Host {
  id: string
  name: string
  group?: string
  hostname: string
  port: number
  user: string
  auth: AuthMethod[]
  identityFile?: string
  proxyJump?: string
  source?: string
}

export type ConnState = 'disconnected' | 'connecting' | 'connected' | 'reconnecting'

export interface HostView extends Host {
  state: ConnState
}

export interface BootstrapData {
  version: string
  platform: Platform
  hosts: HostView[]
  coldStartMs: number
  keychainOk: boolean
  configDir: string
  hostsPath: string
  /** Explicit choice, or '' for "follow the OS". */
  language: string
  /** What Go read out of the environment, for when the webview cannot say. */
  systemLanguage: string
  startupError?: string
}

export type ServerPlatform = 'linux' | 'windows' | 'darwin' | 'bsd' | 'unknown'

export interface MCPChange {
  id: string
  hostId: string
  path: string
  at: string
  /** 'write' | 'delete' */
  action: string
  /** The change made a file that did not exist; undoing it removes the file. */
  created: boolean
  bytes: number
  undoable: boolean
}

export interface MCPWritePrompt {
  id: string
  hostId: string
  host: string
  tool: string
  summary: string
  command?: string
  path?: string
  before?: string
  after?: string
}

export interface WritePolicyView {
  /** 'ask' | 'auto' | 'bypass' */
  mode: string
  /** Unix seconds when a relaxed mode reverts. */
  until?: number
}

export interface MCPStatus {
  enabled: boolean
  running: boolean
  url?: string
  token?: string
  hosts: Record<string, boolean>
  write: Record<string, WritePolicyView>
  delete: Record<string, boolean>
  /** The port actually bound, and the one asked for. They differ when something
   *  else held the preferred port. */
  port?: number
  wantedPort?: number
  portPinned?: boolean
  /** The exact `claude mcp add` line, assembled by Go so nobody mistypes it. */
  snippet?: string
  /** The same for Codex, which wants the token as an environment variable's
   *  name rather than a header, so it is two lines and not an edit of the one
   *  above. */
  codexSnippet?: string
  error?: string
}

export interface ServerInfo {
  platform: ServerPlatform
  /** Whether an adapter exists for this platform. Comes from Go rather than being
   *  derived from `platform` here — deriving it meant the UI kept showing
   *  "unsupported" over a working Windows adapter because this side still said
   *  `platform !== 'linux'`. */
  supported: boolean
  /** Raw `uname -s`, or the Windows `ver` line. Verbatim, for bug reports. */
  kernel?: string
  prettyName: string
  id: string
  version: string
  hasSystemd: boolean
  systemdVersion: number
  systemdJson: boolean
  hasDocker: boolean
  hasPodman: boolean
  hasSudo: boolean
  sudoNoPasswd: boolean
  isRoot: boolean
  groups?: string[]
  /** false when this user sees only their own journal messages. */
  canReadJournal: boolean
  /** What the processor calls itself. Read once per connection, not per poll —
   *  it cannot change while the connection is up. Empty where the kernel does
   *  not name one. */
  cpuModel?: string
  hostname?: string
  timezone?: string
  warnings?: string[]
  capabilities: Record<string, boolean>
}

export interface SSHSession {
  pid: number
  ppid: number
  user: string
  tty?: string
  from?: string
  elapsed: number
  idle?: string
  what?: string
  /** True for the connection LiteDeck itself is using. The binding refuses to end
   *  these regardless of what the UI does. */
  self: boolean
}

export interface ServiceUnit {
  name: string
  description?: string
  load?: string
  active?: string
  sub?: string
  enabled?: string
  preset?: string
  template?: boolean
}

/** One entry in a directory listing (§4.2). */
export interface FileEntry {
  name: string
  path: string
  size: number
  mode: string
  perm: number
  isDir: boolean
  isSymlink: boolean
  linkTarget?: string
  broken?: boolean
  modTime: number
  uid: number
  gid: number
}

export interface DirListing {
  path: string
  parent: string
  entries: FileEntry[]
  total: number
  truncated: boolean
  /** Recursive deletion here requires the path to be typed out (§7.4). */
  protected: boolean
}

export interface PathStatus {
  path: string
  exists: boolean
  isDir: boolean
  size: number
  protected: boolean
}

export interface TextFile {
  path: string
  content: string
  size: number
  perm: number
  /** Unix seconds. Handed back on save so the app can tell an untouched file
   *  from one somebody else edited meanwhile (§4.7-3). */
  modTime: number
  tooLarge: boolean
  binary: boolean
}

export interface SaveRequest {
  path: string
  content: string
  /** What the file looked like when it was opened. Zero skips the check. */
  baseModTime: number
  baseSize: number
  /** Save anyway, after the user has been shown the conflict. */
  force: boolean
}

export interface SaveResult extends ActionResult {
  /** The file on the server is not the one that was opened. Nothing was written. */
  conflict: boolean
  /** The atomic path was unavailable and the file was written over itself. */
  inPlace: boolean
  modTime: number
  size: number
}

export interface Transfer {
  id: string
  hostId: string
  direction: 'upload' | 'download'
  local: string
  remote: string
  size: number
  done: number
  status: 'queued' | 'running' | 'done' | 'failed' | 'cancelled'
  error?: string
  startedAt: number
  /** A directory tree is one queue entry covering many files. */
  dir?: boolean
  files?: number
  filesDone?: number
  currentRel?: string
  /** The bytes already moved are still on disk, so this can be picked up. */
  resumable?: boolean
  /** Where the current attempt began, so the bar does not restart at zero. */
  resumed?: number
}

/** The outcome of an action that may require root (§7.2). */
export interface ActionResult {
  ok: boolean
  /** The command failed only for want of privileges; offer to retry as admin. */
  needsElevation: boolean
  error?: string
  stderr?: string
}

/** One published port mapping (§4.5). */
export interface Port {
  hostIp?: string
  hostPort?: string
  container: string
  protocol: string
}

export interface Container {
  id: string
  name: string
  image: string
  command: string
  state: string
  status: string
  created: string
  ports: Port[]
  /** Parsed out of Status for exited containers; -1 when not applicable. */
  exitCode: number
  networks?: string[]
  size?: string
  /** Set when Compose started this container; absent otherwise. */
  compose?: Compose
}

/** The Compose project and service a container belongs to (§4.5). */
export interface Compose {
  project: string
  service: string
  /** From `compose run`. Part of the project, but not of its declared set. */
  oneOff?: boolean
}

/** A read-only look at a file the editor will not open (§4.2). */
export interface FilePreview {
  path: string
  size: number
  /** "image" when the webview can draw it, "binary" otherwise. */
  kind: 'image' | 'binary'
  mime: string
  /** base64. The whole file for an image, a bounded prefix otherwise. */
  data: string
  truncated?: boolean
  tooLarge?: boolean
}

/** An open terminal tab (§4.6). */
export interface TerminalInfo {
  id: string
  hostId: string
  title: string
  /** Orders tabs recovered after the view remounted. */
  seq: number
}

/** A path a caught `code`/`vi` resolved to (§4.6a). */
export interface RevealRequest {
  hostId: string
  path: string
  isDir: boolean
  /** Not there yet, but its directory is — `vi test.cpp`. */
  new: boolean
  error?: string
}

export interface TerminalOptions {
  cols: number
  rows: number
  dir?: string
  containerId?: string
}

/** One mounted filesystem (§4.7). */
export interface Filesystem {
  device: string
  mountPoint: string
  size: number
  used: number
  available: number
  percent: number
  /** The other way a filesystem fills up. A disk with room and no inodes left
   *  cannot create a file, and says "no space left on device" either way.
   *  Absent on filesystems with no inode table, such as btrfs. */
  inodesTotal?: number
  inodesUsed?: number
  inodesPercent?: number
}

/** One interface. Rates are bytes per second since the last sample, -1 before
 *  there were two. Errors and drops are raw totals: what matters is whether
 *  they are climbing. */
export interface NetIface {
  name: string
  rxBytes: number
  txBytes: number
  rxErrs: number
  txErrs: number
  rxDrop: number
  txDrop: number
  rxRate: number
  txRate: number
}

/** One block device. iowait says the machine is waiting on storage; this says
 *  which storage. */
export interface DiskIO {
  name: string
  readBytes: number
  writeBytes: number
  readRate: number
  writeRate: number
}

/** Pressure stall information. Utilisation says how much of a thing is used;
 *  pressure says how much time was lost waiting for it. */
export interface Pressure {
  some10: number
  some60: number
  some300: number
  full10: number
}

/** One NVIDIA card (§4.7). NVIDIA only: nvidia-smi ships with every driver and
 *  answers over a plain SSH connection; AMD and Intel need a package that is not
 *  installed by default. */
export interface GPU {
  index: number
  name: string
  /** -1 where the card does not report the figure, the same convention cpu uses.
   *  Passively cooled datacentre cards have no fan reading, and a 0 there would
   *  read as a stopped fan. */
  utilization: number
  fan: number
  tempC: number
  memTotal: number
  memUsed: number
  memPercent: number
}

/** One row of the event timeline. See arch/07. */
export interface ServerEvent {
  at: string
  kind:
    | 'oom'
    | 'unit-failed'
    | 'start-failed'
    | 'coredump'
    | 'restart'
    | 'boot'
    | 'shutdown'
    | 'session'
    | 'other'
  /** Journal PRIORITY: 0 emerg … 7 debug (RFC 5424). */
  severity: number
  unit?: string
  message: string
  /** Two adjacent rows with different boot ids have a reboot between them. */
  bootId: string
}

/**
 * `access` is why the list may be empty, which matters more here than anywhere
 * else: a user outside systemd-journal/adm gets an empty journal with no error,
 * and rendered plainly that reads as "nothing has gone wrong on this server".
 */
export interface EventsView {
  events: ServerEvent[]
  access: 'ok' | 'needs-sudo' | 'denied' | 'no-journal'
  range: '1h' | '24h' | '7d'
  /** The read hit its line cap, so the window shown is narrower than asked for. */
  truncated: boolean
}

/** One logical CPU. Thirty-two cores at "40%" is either every core half busy or
 *  one pinned and the rest idle, and those are different problems. */
export interface Core {
  index: number
  /** -1 until a second sample. */
  usage: number
}

/** Where the CPU time went between two samples, in percent. -1 when unknown.
 *  90% that is all iowait is waiting for a disk, not short of CPU; 90% steal is
 *  the hypervisor handing the time to somebody else. */
export interface CPUSplit {
  user: number
  system: number
  iowait: number
  steal: number
}

export interface MetricsView {
  /** -1 until a second sample exists — the counters are totals since boot. */
  cpu: number
  cores: Core[]
  split: CPUSplit
  memBuffers: number
  memCached: number
  memShared: number
  memDirty: number
  /** vmstat's r and b: tasks wanting a CPU, and tasks stuck in uninterruptible
   *  IO. Between them they say which of the two a slow machine is short of. */
  runnable: number
  blocked: number
  /** Context switches per second, -1 before a second sample. */
  switchRate: number
  net: NetIface[]
  diskIO: DiskIO[]
  /** In-use descriptors. fdMax is 0 where the kernel reports no real ceiling. */
  fdUsed: number
  fdMax: number
  /** False on a kernel without CONFIG_PSI or older than 4.20. */
  hasPSI: boolean
  psiCPU: Pressure
  psiIO: Pressure
  psiMemory: Pressure
  memTotal: number
  memAvailable: number
  memUsed: number
  memPercent: number
  swapTotal: number
  swapUsed: number
  load1: number
  load5: number
  load15: number
  /** False where the OS has no load average at all — Windows. The tile is hidden
   *  rather than showing 0.00, which reads as an idle machine. */
  hasLoad: boolean
  uptimeSeconds: number
  filesystems: Filesystem[]
  /** The filtered, sorted subset worth showing. */
  disks: Filesystem[]
  /** Empty on every host without an NVIDIA card, which is most of them. */
  gpus: GPU[]
}

/** An open live-log follow (§4.3, §4.5). */
export interface LogStream {
  id: string
  hostId: string
  title: string
}

export interface LogLine {
  text: string
  stderr: boolean
}

/** One network interface (v1.x). */
export interface NetInterface {
  name: string
  mac?: string
  state: string
  mtu: number
  loopback: boolean
  addresses: { address: string; prefix: number; family: string; scope?: string }[]
}

export interface Listener {
  protocol: string
  address: string
  port: string
  process?: string
  pid?: number
  ipv6: boolean
  /** Bound to all interfaces rather than loopback — reachable from outside. */
  exposed: boolean
}

export interface NetworkView {
  interfaces: NetInterface[]
  listeners: Listener[]
  warnings: string[]
}

/** One container image (v1.x). */
export interface Image {
  id: string
  repository: string
  tag: string
  size: string
  sizeBytes: number
  created: string
  /** How many containers use it; -1 when the daemon did not compute it. */
  containers: number
  /** An untagged layer left by a rebuild. */
  dangling: boolean
}

export interface Volume {
  name: string
  driver: string
  mountpoint: string
  scope: string
  inUse: boolean
}

/** One systemd timer (v1.x). Next/Last are unix seconds; 0 means never. */
export interface Timer {
  unit: string
  activates: string
  next: number
  last: number
  description?: string
}

export interface CommandEntry {
  seq: number
  hostId: string
  line: string
  at: string
  status: 'running' | 'ok' | 'probe' | 'failed' | 'error'
  exitCode: number
  durationMs: number
  /** '' = user action, 'poll' = background refresh, 'probe' = capability check. */
  kind?: '' | 'poll' | 'probe'
  stderr?: string
  /** 'ai' when an MCP client asked for it. */
  origin?: string
  /** Time spent waiting for a session slot, when long enough to matter. */
  queuedMs?: number
  /** How many times this background read has run. Absent for user actions. */
  repeat?: number
}

export interface ConnectionState {
  hostId: string
  state: ConnState
  error?: string
}

export interface HostKeyPrompt {
  id: string
  hostId: string
  address: string
  keyType: string
  fingerprint: string
}

export interface SecretPrompt {
  id: string
  hostId: string
  kind: string
  label: string
  canRemember: boolean
  echo: boolean
}

export interface SSHDDirective {
  keyword: string
  value: string
  file: string
  line: number
  conditional?: boolean
}

export interface SSHDNote {
  code: string
  level: 'warn' | 'info'
  value: string
  file: string
  line: number
}

export interface SSHDReport {
  files: string[]
  declared: SSHDDirective[]
  matches?: SSHDDirective[]
  notes?: SSHDNote[]
  unreadable?: string[]
}

export interface ImportResult {
  path: string
  imported: number
  skipped: number
}

/* --------------------------------------------------------------- bindings */

interface Bindings {
  ListSSHSessions(hostID: string): Promise<SSHSession[]>
  EndSSHSession(hostID: string, pid: number): Promise<ActionResult>
  Bootstrap(): Promise<BootstrapData>
  Platform(): Promise<Platform>
  ReadClipboard(): Promise<string>
  ColdStartMs(): Promise<number>

  ListHosts(): Promise<HostView[]>
  ApplyLanguage(tag: string): Promise<void>
  MCPState(): Promise<MCPStatus>
  SetMCPEnabled(enabled: boolean): Promise<MCPStatus>
  SetMCPHost(hostID: string, allowed: boolean): Promise<MCPStatus>
  RotateMCPToken(): Promise<MCPStatus>
  PinMCPPort(port: number): Promise<MCPStatus>
  SetMCPWritePolicy(hostID: string, mode: string, minutes: number): Promise<MCPStatus>
  AnswerMCPWrite(id: string, approved: boolean): Promise<void>
  MCPChanges(hostID: string): Promise<MCPChange[]>
  RestoreMCPChange(id: string): Promise<ActionResult>
  SetMCPHostDelete(hostID: string, allowed: boolean): Promise<MCPStatus>
  SetLanguage(tag: string): Promise<ActionResult>
  SaveHost(h: Host): Promise<void>
  DeleteHost(id: string): Promise<void>
  ImportSSHConfig(): Promise<ImportResult>
  ConnectHost(id: string): Promise<void>
  DisconnectHost(id: string): Promise<void>
  ForgetSecrets(id: string): Promise<void>

  DetectHost(id: string): Promise<ServerInfo>
  ListProcesses(id: string, asTree: boolean): Promise<ProcessInfo[]>
  KillProcess(
    id: string,
    pid: number,
    signal: string,
    elevate: boolean,
  ): Promise<ActionResult>
  Renice(
    id: string,
    pid: number,
    nice: number,
    elevate: boolean,
  ): Promise<ActionResult>
  ProcessExists(id: string, pid: number): Promise<boolean>

  HostMetrics(id: string): Promise<MetricsView>
  HostEvents(id: string, range: string, elevate: boolean): Promise<EventsView>
  HostNetwork(id: string): Promise<NetworkView>
  SSHDConfig(id: string): Promise<SSHDReport>
  FollowServiceLog(
    id: string,
    unit: string,
    tail: number,
    elevate: boolean,
  ): Promise<LogStream>
  FollowContainerLog(id: string, containerId: string, tail: number): Promise<LogStream>
  StopLogStream(streamId: string): Promise<void>
  ListContainers(id: string): Promise<Container[]>
  ContainerAction(
    id: string,
    containerId: string,
    action: string,
    elevate: boolean,
  ): Promise<ActionResult>
  ComposeAction(
    id: string,
    project: string,
    service: string,
    action: string,
    elevate: boolean,
  ): Promise<ActionResult>
  RemoveContainer(
    id: string,
    containerId: string,
    force: boolean,
    elevate: boolean,
  ): Promise<ActionResult>
  ContainerLogs(id: string, containerId: string, lines: number): Promise<string>
  ListImages(id: string): Promise<Image[]>
  ListVolumes(id: string): Promise<Volume[]>
  RemoveImage(id: string, imageId: string, elevate: boolean): Promise<ActionResult>
  RemoveVolume(id: string, name: string, elevate: boolean): Promise<ActionResult>
  PruneImages(id: string, elevate: boolean): Promise<ActionResult>
  ListTimers(id: string): Promise<Timer[]>

  OpenTerminal(id: string, opts: TerminalOptions): Promise<TerminalInfo>
  ListTerminals(id: string): Promise<TerminalInfo[]>
  RevealFromTerminal(termId: string, arg: string): Promise<RevealRequest>
  WriteTerminal(termId: string, data: string): Promise<void>
  ResizeTerminal(termId: string, cols: number, rows: number): Promise<void>
  CloseTerminal(termId: string): Promise<void>

  ListDir(id: string, dir: string): Promise<DirListing>
  StatPath(id: string, p: string): Promise<PathStatus>
  HomeDir(id: string): Promise<string>
  MakeDir(id: string, p: string): Promise<ActionResult>
  RenamePath(id: string, from: string, to: string): Promise<ActionResult>
  Chmod(id: string, p: string, perm: number): Promise<ActionResult>
  DeletePaths(
    id: string,
    paths: string[],
    recursive: boolean,
    typed: string,
  ): Promise<ActionResult>
  ReadTextFile(id: string, p: string): Promise<TextFile>
  PreviewFile(id: string, p: string): Promise<FilePreview>
  WriteTextFile(id: string, p: string, content: string): Promise<ActionResult>
  SaveTextFile(id: string, req: SaveRequest): Promise<SaveResult>

  StartUpload(id: string, localPaths: string[], remoteDir: string): Promise<string[]>
  StartDownload(id: string, remotePaths: string[], localDir: string): Promise<string[]>
  CancelTransfer(transferId: string): Promise<void>
  ResumeTransfer(transferId: string): Promise<void>
  Transfers(): Promise<Transfer[]>
  ClearFinishedTransfers(): Promise<void>
  PickLocalFiles(): Promise<string[]>
  PickLocalUploadDir(): Promise<string>
  PickLocalDir(): Promise<string>
  ListServices(id: string): Promise<ServiceUnit[]>
  ServiceAction(
    id: string,
    unit: string,
    action: string,
    elevate: boolean,
  ): Promise<ActionResult>

  AnswerHostKey(id: string, decision: string): Promise<void>
  AnswerSecret(id: string, value: string, remember: boolean): Promise<void>
  CancelPrompt(id: string): Promise<void>

  CommandLog(): Promise<CommandEntry[]>
  ClearCommandLog(): Promise<void>

  // Render benchmark (M0 risk ④). Kept until Windows and Linux webviews have
  // been measured too; enabled by LITEDECK_BENCH_OUT.
  BenchMode(): Promise<boolean>
  BenchResize(n: number): Promise<number>
  BenchSnapshot(): Promise<Snapshot>
  BenchDiff(): Promise<Diff>
  ReportSample(s: RenderSample): Promise<string>
  BenchSweepDone(): Promise<string>
}

/** One row of the process view (§4.4). */
export interface ProcessInfo {
  pid: number
  ppid: number
  user: string
  cpu: number
  mem: number
  rss: number
  state: string
  elapsed: number
  command: string
  args: string
  /** Indent level; set only when the list was requested as a tree. */
  depth?: number
}

export interface ProcessRow {
  pid: number
  ppid: number
  user: string
  cpu: number
  mem: number
  rss: number
  state: string
  elapsed: number
  command: string
  args: string
}

export interface Snapshot {
  rows: ProcessRow[]
  seq: number
  genNs: number
}

export interface Diff {
  upserted: ProcessRow[] | null
  removed: number[] | null
  total: number
  seq: number
  genNs: number
}

export interface RenderSample {
  rows: number
  mode: string
  ipcMs: number
  applyMs: number
  renderMs: number
  totalMs: number
  bytes: number
  coldStart: number
  sweepIndex: number
}

interface WailsRuntime {
  EventsOn(event: string, cb: (...data: any[]) => void): () => void
  EventsOff(event: string): void
}

declare global {
  interface Window {
    go?: { app?: { App?: Bindings } }
    runtime?: WailsRuntime
  }
}

function api(): Bindings {
  const bound = window.go?.app?.App
  if (!bound) {
    throw new Error(
      t('Go 바인딩을 찾을 수 없습니다 — `wails dev`로 실행해야 합니다 (순수 Vite 서버로는 동작하지 않습니다).'),
    )
  }
  return bound
}

export const ListSSHSessions = (h: string) => api().ListSSHSessions(h)
export const EndSSHSession = (h: string, pid: number) => api().EndSSHSession(h, pid)
export const Bootstrap = () => api().Bootstrap()
export const GetPlatform = () => api().Platform()
/** Reads the system clipboard through Go — WebKit refuses to let the page do
 *  it. See App.ReadClipboard. */
export const ReadClipboard = () => api().ReadClipboard()
export const ColdStartMs = () => api().ColdStartMs()

export const ListHosts = () => api().ListHosts()
export const ApplyLanguage = (tag: string) => api().ApplyLanguage(tag)
export const MCPState = () => api().MCPState()
export const SetMCPEnabled = (enabled: boolean) => api().SetMCPEnabled(enabled)
export const SetMCPHost = (hostID: string, allowed: boolean) => api().SetMCPHost(hostID, allowed)
export const RotateMCPToken = () => api().RotateMCPToken()
export const PinMCPPort = (port: number) => api().PinMCPPort(port)
export const SetMCPWritePolicy = (hostID: string, mode: string, minutes: number) =>
  api().SetMCPWritePolicy(hostID, mode, minutes)
export const AnswerMCPWrite = (id: string, approved: boolean) => api().AnswerMCPWrite(id, approved)
export const MCPChanges = (hostID: string) => api().MCPChanges(hostID)
export const RestoreMCPChange = (id: string) => api().RestoreMCPChange(id)
export const SetMCPHostDelete = (hostID: string, allowed: boolean) =>
  api().SetMCPHostDelete(hostID, allowed)
export const SetLanguage = (tag: string) => api().SetLanguage(tag)
export const SaveHost = (h: Host) => api().SaveHost(h)
export const DeleteHost = (id: string) => api().DeleteHost(id)
export const ImportSSHConfig = () => api().ImportSSHConfig()
export const ConnectHost = (id: string) => api().ConnectHost(id)
export const DisconnectHost = (id: string) => api().DisconnectHost(id)
export const ForgetSecrets = (id: string) => api().ForgetSecrets(id)

export const DetectHost = (id: string) => api().DetectHost(id)
export const ListProcesses = (id: string, asTree: boolean) =>
  api().ListProcesses(id, asTree)
export const KillProcess = (
  id: string,
  pid: number,
  signal: string,
  elevate: boolean,
) => api().KillProcess(id, pid, signal, elevate)
export const Renice = (id: string, pid: number, nice: number, elevate: boolean) =>
  api().Renice(id, pid, nice, elevate)
export const ProcessExists = (id: string, pid: number) =>
  api().ProcessExists(id, pid)

export const HostMetrics = (id: string) => api().HostMetrics(id)
export const HostEvents = (id: string, range: string, elevate: boolean) =>
  api().HostEvents(id, range, elevate)
export const HostNetwork = (id: string) => api().HostNetwork(id)
export const SSHDConfig = (id: string) => api().SSHDConfig(id)
export const FollowServiceLog = (
  id: string,
  unit: string,
  tail: number,
  elevate: boolean,
) => api().FollowServiceLog(id, unit, tail, elevate)
export const FollowContainerLog = (id: string, containerId: string, tail: number) =>
  api().FollowContainerLog(id, containerId, tail)
export const StopLogStream = (streamId: string) => api().StopLogStream(streamId)
export const ListContainers = (id: string) => api().ListContainers(id)
export const ContainerAction = (
  id: string,
  containerId: string,
  action: string,
  elevate: boolean,
) => api().ContainerAction(id, containerId, action, elevate)
export const ComposeAction = (
  id: string,
  project: string,
  service: string,
  action: string,
  elevate: boolean,
) => api().ComposeAction(id, project, service, action, elevate)
export const RemoveContainer = (
  id: string,
  containerId: string,
  force: boolean,
  elevate: boolean,
) => api().RemoveContainer(id, containerId, force, elevate)
export const ContainerLogs = (id: string, containerId: string, lines: number) =>
  api().ContainerLogs(id, containerId, lines)
export const ListImages = (id: string) => api().ListImages(id)
export const ListVolumes = (id: string) => api().ListVolumes(id)
export const RemoveImage = (id: string, imageId: string, elevate: boolean) =>
  api().RemoveImage(id, imageId, elevate)
export const RemoveVolume = (id: string, name: string, elevate: boolean) =>
  api().RemoveVolume(id, name, elevate)
export const PruneImages = (id: string, elevate: boolean) => api().PruneImages(id, elevate)
export const ListTimers = (id: string) => api().ListTimers(id)

export const OpenTerminal = (id: string, opts: TerminalOptions) =>
  api().OpenTerminal(id, opts)
export const ListTerminals = (id: string) => api().ListTerminals(id)
export const RevealFromTerminal = (termId: string, arg: string) =>
  api().RevealFromTerminal(termId, arg)
export const WriteTerminal = (termId: string, data: string) =>
  api().WriteTerminal(termId, data)
export const ResizeTerminal = (termId: string, cols: number, rows: number) =>
  api().ResizeTerminal(termId, cols, rows)
export const CloseTerminal = (termId: string) => api().CloseTerminal(termId)

export const ListDir = (id: string, dir: string) => api().ListDir(id, dir)
export const StatPath = (id: string, p: string) => api().StatPath(id, p)
export const HomeDir = (id: string) => api().HomeDir(id)
export const MakeDir = (id: string, p: string) => api().MakeDir(id, p)
export const RenamePath = (id: string, from: string, to: string) =>
  api().RenamePath(id, from, to)
export const Chmod = (id: string, p: string, perm: number) =>
  api().Chmod(id, p, perm)
export const DeletePaths = (
  id: string,
  paths: string[],
  recursive: boolean,
  typed: string,
) => api().DeletePaths(id, paths, recursive, typed)
export const ReadTextFile = (id: string, p: string) => api().ReadTextFile(id, p)
export const PreviewFile = (id: string, p: string) => api().PreviewFile(id, p)
export const WriteTextFile = (id: string, p: string, content: string) =>
  api().WriteTextFile(id, p, content)
export const SaveTextFile = (id: string, req: SaveRequest) =>
  api().SaveTextFile(id, req)

export const StartUpload = (id: string, localPaths: string[], remoteDir: string) =>
  api().StartUpload(id, localPaths, remoteDir)
export const StartDownload = (id: string, remotePaths: string[], localDir: string) =>
  api().StartDownload(id, remotePaths, localDir)
export const CancelTransfer = (transferId: string) => api().CancelTransfer(transferId)
export const ResumeTransfer = (transferId: string) => api().ResumeTransfer(transferId)
export const GetTransfers = () => api().Transfers()
export const ClearFinishedTransfers = () => api().ClearFinishedTransfers()
export const PickLocalFiles = () => api().PickLocalFiles()
export const PickLocalUploadDir = () => api().PickLocalUploadDir()
export const PickLocalDir = () => api().PickLocalDir()
export const ListServices = (id: string) => api().ListServices(id)
export const ServiceAction = (
  id: string,
  unit: string,
  action: string,
  elevate: boolean,
) => api().ServiceAction(id, unit, action, elevate)

export const AnswerHostKey = (id: string, decision: string) =>
  api().AnswerHostKey(id, decision)
export const AnswerSecret = (id: string, value: string, remember: boolean) =>
  api().AnswerSecret(id, value, remember)
export const CancelPrompt = (id: string) => api().CancelPrompt(id)

export const GetCommandLog = () => api().CommandLog()
export const ClearCommandLog = () => api().ClearCommandLog()

export const BenchMode = () => api().BenchMode()
export const BenchResize = (n: number) => api().BenchResize(n)
export const BenchSnapshot = () => api().BenchSnapshot()
export const BenchDiff = () => api().BenchDiff()
export const ReportSample = (s: RenderSample) => api().ReportSample(s)
export const BenchSweepDone = () => api().BenchSweepDone()

/** Subscribes to a Wails event, returning an unsubscribe function. */
export function on<T>(event: string, cb: (payload: T) => void): () => void {
  const rt = window.runtime
  if (!rt) return () => {}
  return rt.EventsOn(event, (payload: T) => cb(payload))
}
