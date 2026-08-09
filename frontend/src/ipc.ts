// Typed access to the Go bindings and the Wails event bus.
//
// Wails exposes bound methods on window.go.<package>.<struct>.<Method> and its
// event helpers on window.runtime. Going through this module rather than
// touching those globals keeps the whole binding surface visible in one file.

import type { Platform } from './platform'

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
  startupError?: string
}

export type ServerPlatform = 'linux' | 'windows' | 'darwin' | 'bsd' | 'unknown'

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
  tooLarge: boolean
  binary: boolean
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
}

/** An open terminal tab (§4.6). */
export interface TerminalInfo {
  id: string
  hostId: string
  title: string
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
}

export interface MetricsView {
  /** -1 until a second sample exists — the counters are totals since boot. */
  cpu: number
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
  ColdStartMs(): Promise<number>

  ListHosts(): Promise<HostView[]>
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
  HostNetwork(id: string): Promise<NetworkView>
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
  WriteTextFile(id: string, p: string, content: string): Promise<ActionResult>

  StartUpload(id: string, localPaths: string[], remoteDir: string): Promise<string[]>
  StartDownload(id: string, remotePaths: string[], localDir: string): Promise<string[]>
  CancelTransfer(transferId: string): Promise<void>
  Transfers(): Promise<Transfer[]>
  ClearFinishedTransfers(): Promise<void>
  PickLocalFiles(): Promise<string[]>
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
      'Go 바인딩을 찾을 수 없습니다 — `wails dev`로 실행해야 합니다 (순수 Vite 서버로는 동작하지 않습니다).',
    )
  }
  return bound
}

export const ListSSHSessions = (h: string) => api().ListSSHSessions(h)
export const EndSSHSession = (h: string, pid: number) => api().EndSSHSession(h, pid)
export const Bootstrap = () => api().Bootstrap()
export const GetPlatform = () => api().Platform()
export const ColdStartMs = () => api().ColdStartMs()

export const ListHosts = () => api().ListHosts()
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
export const HostNetwork = (id: string) => api().HostNetwork(id)
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
export const WriteTextFile = (id: string, p: string, content: string) =>
  api().WriteTextFile(id, p, content)

export const StartUpload = (id: string, localPaths: string[], remoteDir: string) =>
  api().StartUpload(id, localPaths, remoteDir)
export const StartDownload = (id: string, remotePaths: string[], localDir: string) =>
  api().StartDownload(id, remotePaths, localDir)
export const CancelTransfer = (transferId: string) => api().CancelTransfer(transferId)
export const GetTransfers = () => api().Transfers()
export const ClearFinishedTransfers = () => api().ClearFinishedTransfers()
export const PickLocalFiles = () => api().PickLocalFiles()
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
