import { StreamLanguage, type StreamParser } from '@codemirror/language'
import type { Extension } from '@codemirror/state'

// Which grammar to use for a file, and how to fetch it (§4.7-3).
//
// Every entry is a dynamic import, so a session that only ever opens YAML pays
// for YAML. That is the whole reason CodeMirror was chosen over Monaco: the
// languages are separable, and the ones nobody opens cost nothing.
//
// The list is picked for what is actually edited over SSH. A systemd unit, an
// nginx site, a compose file and a shell script come before any language that
// is more fun to support.

type Loader = () => Promise<Extension>

/** Wraps one of the ported CodeMirror 5 modes, which come as stream parsers. */
function legacy(load: () => Promise<StreamParser<unknown>>): Loader {
  return async () => StreamLanguage.define(await load())
}

const shell = legacy(() => import('@codemirror/legacy-modes/mode/shell').then((m) => m.shell))
const properties = legacy(() =>
  import('@codemirror/legacy-modes/mode/properties').then((m) => m.properties),
)
const nginx = legacy(() => import('@codemirror/legacy-modes/mode/nginx').then((m) => m.nginx))
const dockerfile = legacy(() =>
  import('@codemirror/legacy-modes/mode/dockerfile').then((m) => m.dockerFile),
)
const toml = legacy(() => import('@codemirror/legacy-modes/mode/toml').then((m) => m.toml))
const lua = legacy(() => import('@codemirror/legacy-modes/mode/lua').then((m) => m.lua))
const ruby = legacy(() => import('@codemirror/legacy-modes/mode/ruby').then((m) => m.ruby))
const perl = legacy(() => import('@codemirror/legacy-modes/mode/perl').then((m) => m.perl))
const powershell = legacy(() =>
  import('@codemirror/legacy-modes/mode/powershell').then((m) => m.powerShell),
)
const diff = legacy(() => import('@codemirror/legacy-modes/mode/diff').then((m) => m.diff))

const js = (opts: { typescript?: boolean; jsx?: boolean } = {}): Loader => () =>
  import('@codemirror/lang-javascript').then((m) => m.javascript(opts))

const json = () => import('@codemirror/lang-json').then((m) => m.json())
const yaml = () => import('@codemirror/lang-yaml').then((m) => m.yaml())
const python = () => import('@codemirror/lang-python').then((m) => m.python())
const markdown = () => import('@codemirror/lang-markdown').then((m) => m.markdown())
const html = () => import('@codemirror/lang-html').then((m) => m.html())
const css = () => import('@codemirror/lang-css').then((m) => m.css())
const sql = () => import('@codemirror/lang-sql').then((m) => m.sql())
const xml = () => import('@codemirror/lang-xml').then((m) => m.xml())
const go = () => import('@codemirror/lang-go').then((m) => m.go())
const rust = () => import('@codemirror/lang-rust').then((m) => m.rust())
const cpp = () => import('@codemirror/lang-cpp').then((m) => m.cpp())
const java = () => import('@codemirror/lang-java').then((m) => m.java())
const php = () => import('@codemirror/lang-php').then((m) => m.php())

export type Language = { label: string; load: Loader }

const SHELL: Language = { label: 'Shell', load: shell }
const INI: Language = { label: 'INI', load: properties }
const YAML: Language = { label: 'YAML', load: yaml }
const JSON_: Language = { label: 'JSON', load: json }

const BY_EXT: Record<string, Language> = {
  // Configuration first — this is what the tool is for.
  yaml: YAML,
  yml: YAML,
  json: JSON_,
  jsonc: JSON_,
  toml: { label: 'TOML', load: toml },
  ini: INI,
  cfg: INI,
  conf: INI,
  properties: INI,
  env: INI,
  // systemd units are INI files with a different suffix, and they are the most
  // likely thing in this list to be opened on a server.
  service: INI,
  socket: INI,
  timer: INI,
  target: INI,
  mount: INI,
  path: INI,
  slice: INI,
  netdev: INI,
  network: INI,
  link: INI,

  sh: SHELL,
  bash: SHELL,
  zsh: SHELL,
  ksh: SHELL,
  ps1: { label: 'PowerShell', load: powershell },
  psm1: { label: 'PowerShell', load: powershell },

  py: { label: 'Python', load: python },
  js: { label: 'JavaScript', load: js() },
  mjs: { label: 'JavaScript', load: js() },
  cjs: { label: 'JavaScript', load: js() },
  jsx: { label: 'JSX', load: js({ jsx: true }) },
  ts: { label: 'TypeScript', load: js({ typescript: true }) },
  mts: { label: 'TypeScript', load: js({ typescript: true }) },
  cts: { label: 'TypeScript', load: js({ typescript: true }) },
  tsx: { label: 'TSX', load: js({ typescript: true, jsx: true }) },
  go: { label: 'Go', load: go },
  rs: { label: 'Rust', load: rust },
  java: { label: 'Java', load: java },
  php: { label: 'PHP', load: php },
  rb: { label: 'Ruby', load: ruby },
  pl: { label: 'Perl', load: perl },
  pm: { label: 'Perl', load: perl },
  lua: { label: 'Lua', load: lua },
  c: { label: 'C', load: cpp },
  h: { label: 'C', load: cpp },
  cc: { label: 'C++', load: cpp },
  cpp: { label: 'C++', load: cpp },
  cxx: { label: 'C++', load: cpp },
  hpp: { label: 'C++', load: cpp },
  hh: { label: 'C++', load: cpp },

  sql: { label: 'SQL', load: sql },
  md: { label: 'Markdown', load: markdown },
  markdown: { label: 'Markdown', load: markdown },
  html: { label: 'HTML', load: html },
  htm: { label: 'HTML', load: html },
  vue: { label: 'HTML', load: html },
  css: { label: 'CSS', load: css },
  scss: { label: 'CSS', load: css },
  less: { label: 'CSS', load: css },
  xml: { label: 'XML', load: xml },
  svg: { label: 'XML', load: xml },
  xsl: { label: 'XML', load: xml },
  plist: { label: 'XML', load: xml },
  patch: { label: 'Diff', load: diff },
  diff: { label: 'Diff', load: diff },
}

// Files whose whole name is the type. Most of the interesting ones on a server
// have no extension at all.
const BY_NAME: Record<string, Language> = {
  dockerfile: { label: 'Dockerfile', load: dockerfile },
  containerfile: { label: 'Dockerfile', load: dockerfile },
  'nginx.conf': { label: 'nginx', load: nginx },
  'default.conf': { label: 'nginx', load: nginx },
  '.bashrc': SHELL,
  '.bash_profile': SHELL,
  '.bash_aliases': SHELL,
  '.profile': SHELL,
  '.zshrc': SHELL,
  '.zprofile': SHELL,
  '.env': INI,
  'sshd_config': INI,
  'ssh_config': INI,
  'config': INI,
  'fstab': INI,
  'hosts': INI,
  'hostname': INI,
  'resolv.conf': INI,
  'crontab': SHELL,
  'authorized_keys': INI,
  'known_hosts': INI,
  '.gitconfig': INI,
  'gitconfig': INI,
}

/** Directories under which a bare `.conf` is nginx rather than an INI file. */
const NGINX_DIRS = ['/etc/nginx/', '/usr/local/nginx/', '/opt/nginx/']

/**
 * Picks a grammar for a path, or null when nothing fits.
 *
 * Guessing wrong is worse than not guessing: a mode applied to the wrong syntax
 * paints half the file as a string, and the user cannot tell that from the file
 * being broken. So unknown stays unknown, and the editor shows plain text.
 */
export function detectLanguage(path: string): Language | null {
  const name = (path.split('/').pop() || '').toLowerCase()

  if (BY_NAME[name]) return BY_NAME[name]
  // A leading dot is part of the name, not the start of an extension: `.bashrc`
  // must not be read as a file with the extension "bashrc".
  const dot = name.lastIndexOf('.')
  if (dot <= 0) return null

  const ext = name.slice(dot + 1)
  if (ext === 'conf' && NGINX_DIRS.some((d) => path.startsWith(d))) {
    return { label: 'nginx', load: nginx }
  }
  return BY_EXT[ext] ?? null
}
