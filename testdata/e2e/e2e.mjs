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

// Every wait is scaled, and the scale is generous by default.
//
// A timeout costs nothing on a run that passes — nobody waits it out — and it
// is the whole of the answer on a run that does not. This harness was written
// against a laptop with the demo image already built, and the first time it met
// a loaded runner it failed: the same checks that take fifty seconds there took
// two minutes eighteen and then gave up. The waits were sized for the fast
// machine, which is the one case that never needed them.
//
// The demo container is the reason a runner can be this slow. Its first boot
// pulls three images through Docker-in-Docker on the vfs storage driver, and
// that runs while these checks do.
const SCALE = Number(process.env.LITEDECK_E2E_TIMEOUT_SCALE ?? 3)
const ms = (base) => Math.round(base * SCALE)

const visible = (loc, timeout) =>
  loc.waitFor({ state: 'visible', timeout }).then(() => true, () => false)

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

// What the file tree is showing, for a failure that has to explain itself.
//
// This check failed on a runner and passed on every machine here, including
// with every directory call delayed by eight seconds, so the next failure has
// to arrive with its own evidence rather than another guess.
async function treeState() {
  return page.evaluate(() => {
    const box = document.querySelector('.file-tree .view-toolbar input.search')
    const ph = document.querySelector('.file-tree .placeholder')
    const err = document.querySelector('.error, .warn-text, .error-banner')
    return {
      path: box ? box.value : '(no path box)',
      rows: document.querySelectorAll('.file-tree .trow').length,
      placeholder: ph ? ph.textContent.trim().slice(0, 80) : null,
      error: err ? err.textContent.trim().slice(0, 120) : null,
    }
  })
}

/**
 * Navigates the file tree, and keeps asking.
 *
 * Typing a path and pressing Enter is one shot at a server that might be busy,
 * and one shot is what failed in CI: the listing for the second connection
 * never arrived and the run ended there. Three tries cost nothing on a machine
 * where the first works.
 *
 * The wait before the first attempt is not decoration either. The path box is
 * empty until the home directory lands, and typing into a control the app is
 * about to fill in is a race that only shows up on a slow box.
 */
async function goTo(dir, marker) {
  const box = page.locator('.file-tree .view-toolbar input.search').first()
  await box.waitFor({ state: 'visible', timeout: ms(15000) })
  await page
    .waitForFunction(
      () => {
        const el = document.querySelector('.file-tree .view-toolbar input.search')
        return el && el.value.length > 0
      },
      null,
      { timeout: ms(30000) },
    )
    .catch(() => {})

  for (let attempt = 1; attempt <= 3; attempt++) {
    await box.fill(dir)
    await box.press('Enter')
    if (await visible(row(marker).first(), ms(20000))) return true
    console.log(`        (${dir} did not list on attempt ${attempt}: ${JSON.stringify(await treeState())})`)
  }
  return false
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
    const asked = await visible(trust, ms(15000))
    if (asked) await trust.click()
    check('the host key prompt is offered and answered', asked)
  }

  const files = page.getByRole('button', { name: 'Files', exact: true })
  check('the tabs appear once the host is connected', await visible(files, ms(30000)))
  await files.click()

  // -- navigate ----------------------------------------------------------
  const listed = await goTo(DIR, 'README.md')
  check(`${DIR} lists its contents`, listed, JSON.stringify(await treeState()))
  if (!listed) throw new Error('the listing never arrived; nothing after this could mean anything')

  // -- the editor writes to the server -----------------------------------
  const marker = `e2e-${Date.now()}`
  await open('README.md')
  const editor = page.locator('.cm-content')
  await editor.waitFor({ state: 'visible', timeout: ms(20000) })
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
  const shownDiff = await visible(diff, ms(15000))
  check('saving shows the diff against the server copy first', shownDiff)
  if (shownDiff) await diff.locator('button').filter({ hasText: /^Save/ }).first().click()

  let saved = false
  for (let i = 0; i < 40 * SCALE && !saved; i++) {
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
  const asked = await visible(nameBox, ms(10000))
  if (asked) {
    await nameBox.fill(sub)
    await nameBox.press('Enter')
  }
  let made = false
  for (let i = 0; i < 20 * SCALE && asked && !made; i++) {
    await page.waitForTimeout(500)
    made = exists(`${DIR}/${sub}`)
  }
  check('New folder creates the directory on the server', made, `${DIR}/${sub} is not there`)

  // -- delete ------------------------------------------------------------
  if (made) {
    await row(sub).first().click()
    await page.locator('.view-toolbar').getByRole('button', { name: 'Delete', exact: true }).click()
    const confirm = page.locator('form.dialog, .dialog').getByRole('button', { name: /Delete|OK/ }).last()
    if (await visible(confirm, ms(8000))) await confirm.click()
    let gone = false
    for (let i = 0; i < 20 * SCALE && !gone; i++) {
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
  await reconnect.waitFor({ state: 'visible', timeout: ms(20000) })
  await page.waitForTimeout(500)
  await reconnect.click()
  const backAgain = await visible(page.getByRole('button', { name: 'Files', exact: true }), ms(30000))
  check('the host reconnects after a disconnect', backAgain)

  if (backAgain) {
    await page.getByRole('button', { name: 'Files', exact: true }).click()
    const relisted = await goTo(DIR, 'README.md')
    check(
      'the file listing works on the second connection',
      relisted,
      JSON.stringify(await treeState()),
    )
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
  if (await visible(picker, ms(10000))) {
    const at = () => page.evaluate(() => document.documentElement.dataset.theme)
    const opened = await picker.inputValue()
    check('the picker opens on what the desktop is set to', opened === 'light', `it reads ${opened}`)

    let ok = true
    // Back to light at the end so the run leaves the config as it found it.
    for (const want of ['dark', 'light', 'dark', 'light']) {
      await picker.selectOption(want)
      // Polled rather than slept through. A fixed pause is a guess about the
      // slowest machine that will ever run this, and the guess was wrong once
      // already; waiting for the state itself is right on every machine and
      // faster on most.
      let shown = ''
      let stamped = ''
      for (let i = 0; i < 20 * SCALE; i++) {
        shown = await picker.inputValue()
        stamped = await at()
        if (shown === want && stamped === want) break
        await page.waitForTimeout(100)
      }
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
