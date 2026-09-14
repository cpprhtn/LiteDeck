// The browser half of testdata/e2e/run.sh.
//
// Every check ends at the server, not at the screen. The app saying a file was
// renamed is exactly what it said while renaming files into the wrong directory,
// so each step reads the result back through a channel this app is not part of
// and compares. What the UI claims is never the evidence.
import { execFileSync } from 'node:child_process'
import { chromium } from 'playwright-core'

const URL = process.env.LITEDECK_E2E_URL ?? 'http://127.0.0.1:18765'
const CONTAINER = process.env.LITEDECK_E2E_CONTAINER ?? 'litedeck-e2e'
const ARTIFACTS = process.env.LITEDECK_E2E_ARTIFACTS ?? '.'
const DIR = '/srv/api'

let failures = 0
const results = []

function check(name, ok, detail = '') {
  results.push({ name, ok, detail })
  if (!ok) failures++
  console.log(`${ok ? '  ok  ' : 'FAIL  '}${name}${detail && !ok ? `\n        ${detail}` : ''}`)
}

// onServer runs a command in the demo container as the login user.
//
// Through docker rather than over SSH, so it cannot be satisfied by the same
// connection the app is using. A hung channel would otherwise make both halves
// of the test hang together and agree with each other.
function onServer(cmd) {
  return execFileSync('docker', ['exec', '-u', 'deploy', CONTAINER, 'bash', '-lc', cmd], {
    encoding: 'utf8',
  }).trim()
}

function exists(path) {
  try {
    return onServer(`test -e ${JSON.stringify(path)} && echo yes || echo no`) === 'yes'
  } catch {
    return false
  }
}

// The browser already on the machine, by default. Setting the variable to an
// empty string asks for whatever Playwright itself has downloaded, which is how
// this runs where Chrome is not installed.
const channel = process.env.LITEDECK_E2E_CHANNEL ?? 'chrome'
const browser = await chromium.launch(channel ? { channel } : {})
// A light desktop, pinned. The theme checks below move the picker back to
// "follow the OS" and assert the colours do not move, which needs the OS half
// of that to be a known quantity rather than whatever the machine running this
// happens to be set to.
const page = await browser.newPage({
  viewport: { width: 1440, height: 900 },
  colorScheme: 'light',
})
const consoleErrors = []
page.on('console', (m) => {
  if (m.type() === 'error') consoleErrors.push(m.text())
})
page.on('pageerror', (e) => consoleErrors.push(String(e)))

const visible = (loc, ms) =>
  loc.waitFor({ state: 'visible', timeout: ms }).then(() => true, () => false)

// row addresses one line of the file tree by its exact name.
//
// By the name span rather than by text anywhere in the row, because a row also
// carries a size, a permission string and a date, and "26" is a plausible file
// name. Anchored, so package.json does not answer to json.
const row = (name) =>
  page.locator('.file-tree .trow').filter({
    has: page.locator('.tree-name > span.ellipsis', {
      hasText: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`),
    }),
  })

// open double-clicks nothing.
//
// The tree is virtualised: it recycles a fixed pool of row elements and moves
// them with transform. Between the two halves of a double-click the element
// under the pointer can be holding a different file, and it does — the first
// version of this opened package.json while asking for README.md. Select, then
// press Enter, which is the app's own shortcut and involves no second hit test.
async function open(name) {
  await row(name).first().click()
  await page.keyboard.press('Enter')
}

try {
  // -- connect -----------------------------------------------------------
  await page.goto(URL, { waitUntil: 'networkidle' })

  const connect = page.getByRole('button', { name: 'Connect', exact: true })
  if ((await connect.count()) > 0) {
    await connect.click()
    // First contact with this host key. Answering it is part of the path under
    // test: the prompt travels over the web transport like everything else.
    const trust = page.getByRole('button', { name: 'Always trust' })
    const asked = await visible(trust, 15000)
    if (asked) await trust.click()
    check('the host key prompt is offered and answered', asked)
  }

  const files = page.getByRole('button', { name: 'Files', exact: true })
  check('the tabs appear once the host is connected', await visible(files, 30000))
  await files.click()

  // -- navigate ----------------------------------------------------------
  const pathBox = page.locator('.file-tree .view-toolbar input.search').first()
  await pathBox.waitFor({ state: 'visible', timeout: 15000 })
  // The path box is empty until the home listing lands. Typing into it before
  // then is typing into a control the app is about to overwrite.
  await page
    .waitForFunction(() => {
      const el = document.querySelector('.file-tree .view-toolbar input.search')
      return el && el.value.length > 0
    }, null, { timeout: 30000 })
    .catch(() => {})
  await pathBox.fill(DIR)
  await pathBox.press('Enter')
  const listed = await visible(row('README.md').first(), 20000)
  check(`${DIR} lists its contents`, listed)
  if (!listed) throw new Error('the listing never arrived; nothing after this could mean anything')

  // -- the editor writes to the server -----------------------------------
  const marker = `e2e-${Date.now()}`
  await open('README.md')
  const editor = page.locator('.cm-content')
  await editor.waitFor({ state: 'visible', timeout: 20000 })
  await editor.click()
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.type(`\n${marker}\n`)
  // The button rather than the shortcut. A shortcut tests the key handler; the
  // button is what the person clicks, and both end in the same save.
  //
  // Matched by prefix: the label carries a bullet while there is something
  // unsaved, so an exact "Save" finds nothing at the one moment it matters.
  await page.locator('button').filter({ hasText: /^Save/ }).first().click()

  // Saving shows the diff against the server's copy first. Confirming it is
  // part of the path, and the dialog appearing at all is worth asserting: it is
  // the last thing between a stale editor buffer and somebody's file.
  const diff = page.locator('.diff-dialog')
  const shownDiff = await visible(diff, 15000)
  check('saving shows the diff against the server copy first', shownDiff)
  if (shownDiff) await diff.locator('button').filter({ hasText: /^Save/ }).first().click()

  let saved = false
  for (let i = 0; i < 40 && !saved; i++) {
    await page.waitForTimeout(500)
    try {
      saved = onServer(`grep -c ${JSON.stringify(marker)} ${DIR}/README.md || true`) !== '0'
    } catch {
      saved = false
    }
  }
  check(
    'the editor writes through to the file on the server',
    saved,
    `${DIR}/README.md does not contain ${marker}`,
  )

  // -- new folder --------------------------------------------------------
  const sub = `e2e-sub-${Date.now()}`
  await page.getByRole('button', { name: 'New folder' }).click()
  const nameBox = page.locator('form.dialog input').first()
  const asked = await visible(nameBox, 10000)
  if (asked) {
    await nameBox.fill(sub)
    await nameBox.press('Enter')
  }
  let made = false
  for (let i = 0; i < 20 && asked && !made; i++) {
    await page.waitForTimeout(500)
    made = exists(`${DIR}/${sub}`)
  }
  check('New folder creates the directory on the server', made, `${DIR}/${sub} is not there`)

  // -- delete ------------------------------------------------------------
  if (made) {
    await row(sub).first().click()
    await page.locator('.view-toolbar').getByRole('button', { name: 'Delete', exact: true }).click()
    const confirm = page.locator('form.dialog, .dialog').getByRole('button', { name: /Delete|OK/ }).last()
    if (await visible(confirm, 8000)) await confirm.click()
    let gone = false
    for (let i = 0; i < 20 && !gone; i++) {
      await page.waitForTimeout(500)
      gone = !exists(`${DIR}/${sub}`)
    }
    check('deleting removes the directory from the server', gone, `${DIR}/${sub} is still there`)
    if (!gone) onServer(`rm -rf ${JSON.stringify(`${DIR}/${sub}`)}`)
  }

  // -- reconnect ---------------------------------------------------------
  //
  // Not a formality. The reconnect path is where a pending prompt used to be
  // dropped, leaving a connection waiting for an answer nobody would be asked.
  await page.getByRole('button', { name: 'Disconnect', exact: true }).click()
  const reconnect = page.getByRole('button', { name: 'Connect', exact: true })
  await reconnect.waitFor({ state: 'visible', timeout: 20000 })
  await page.waitForTimeout(500)
  await reconnect.click()
  const backAgain = await visible(page.getByRole('button', { name: 'Files', exact: true }), 30000)
  check('the host reconnects after a disconnect', backAgain)

  if (backAgain) {
    await page.getByRole('button', { name: 'Files', exact: true }).click()
    const again = page.locator('.file-tree .view-toolbar input.search').first()
    await again.waitFor({ state: 'visible', timeout: 15000 })
    await again.fill(DIR)
    await again.press('Enter')
    const relisted = await visible(row('README.md').first(), 20000)
    check('the file listing works on the second connection', relisted)
  }

  // -- the theme picker --------------------------------------------------
  //
  // Two positions, and both have to stick. The picker is a controlled <select>
  // over a module variable, so React restores it to its last rendered value
  // unless the change produces a re-render — the colours moved and the control
  // snapped back, which showed or did not depending on whether something else
  // re-rendered the rail in the same tick.
  //
  // The browser is told to report a light desktop, so the position the picker
  // opens on is known: nothing is stored on a fresh config, and an unstored
  // theme resolves against the OS.
  const picker = page.locator('select[aria-label="테마"], select[aria-label="Theme"]').first()
  if (await visible(picker, 10000)) {
    const at = () => page.evaluate(() => document.documentElement.dataset.theme)
    const opened = await picker.inputValue()
    check('the picker opens on what the desktop is set to', opened === 'light', `it reads ${opened}`)

    let ok = true
    // Back to light at the end so the run leaves the config as it found it.
    for (const want of ['dark', 'light', 'dark', 'light']) {
      await picker.selectOption(want)
      await page.waitForTimeout(700)
      const shown = await picker.inputValue()
      const stamped = await at()
      if (shown !== want || stamped !== want) {
        ok = false
        check(
          `the theme picker settles on ${want}`,
          false,
          `the control reads ${JSON.stringify(shown)} and the page is ${stamped}`,
        )
      }
    }
    if (ok) check('the theme picker sticks where it is put', true)
  } else {
    check('the theme picker is on screen', false)
  }

  // -- the console -------------------------------------------------------
  //
  // A React error boundary or an unhandled rejection leaves the screen looking
  // fine and the next interaction broken. Nothing above would notice.
  check(
    'nothing threw in the browser',
    consoleErrors.length === 0,
    consoleErrors.slice(0, 5).join('\n        '),
  )
} catch (err) {
  check('the run finished', false, String(err).split('\n')[0])
} finally {
  await page.screenshot({ path: `${ARTIFACTS}/e2e-final.png`, fullPage: true }).catch(() => {})
  await browser.close()
}

console.log(`\n${results.filter((r) => r.ok).length}/${results.length} passed`)
process.exit(failures === 0 ? 0 : 1)
