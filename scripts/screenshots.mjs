// README screenshots and a browser smoke test, against a demo instance (DEMO_DATA=true seeds seven sources, three
// places to send to, five rules and ten weeks of alerts):
//
//   DATA_DIR=/tmp/alerts-demo DEMO_DATA=true ADMIN_EMAIL=demo@example.com ADMIN_PASSWORD=demodemo1 ADMIN_NAME="Ana Petrović" PORT=18316 ./pocket-alerts
//   CDP=http://127.0.0.1:19271 BASE=http://127.0.0.1:18316 node scripts/screenshots.mjs   # any Chrome with remote debugging
//   SHOTS=0 … node scripts/screenshots.mjs                                                  # checks only
//   OUT=/tmp/shots … node scripts/screenshots.mjs                                           # somewhere else than docs/screenshots
// playwright-core comes from web/node_modules, or set PW_FROM to a package.json next to an install of it.
import { createRequire } from 'node:module'
import assert from 'node:assert/strict'

const require = createRequire(process.env.PW_FROM || new URL('../web/package.json', import.meta.url))
let pw
try { pw = require('playwright') } catch { pw = require('playwright-core') }
const { chromium } = pw
const base = process.env.BASE || 'http://127.0.0.1:18316'
const shots = process.env.SHOTS !== '0'
const dir = process.env.OUT ? process.env.OUT.replace(/\/?$/, '/') : new URL('../docs/screenshots/', import.meta.url).pathname
const browser = process.env.CDP ? await chromium.connectOverCDP(process.env.CDP) : await chromium.launch()
const errors = []

async function session({ phone = false, dark = false } = {}) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: phone ? 3 : 2, isMobile: phone, hasTouch: phone, colorScheme: dark ? 'dark' : 'light',
    userAgent: phone ? 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1' : undefined,
  })
  const page = await ctx.newPage()
  page.on('pageerror', e => errors.push(`pageerror: ${e}`))
  page.on('console', m => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) errors.push(m.text()) })
  await page.goto(base + '/login')
  const status = await page.evaluate(() => fetch('/api/v1/auth/status').then(r => r.json()))
  if (status.auth_mode === 'local') {
    const res = await page.evaluate(() => fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email: 'demo@example.com', password: 'demodemo1' }) }).then(r => r.status))
    assert.equal(res, 200, 'demo sign-in')
  }
  return { ctx, page }
}
async function go(page, path, wait = 1500, still = true) {
  await page.goto(base + path, { waitUntil: 'networkidle' })
  // headless Chrome freezes CSS fade-ins: show everything at once
  if (still) await page.addStyleTag({ content: '*, *::before, *::after { transition: none !important; animation: none !important; }' })
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(wait)
}
async function shot(page, name) { if (shots) await page.screenshot({ path: dir + name + '.png' }) }
async function firstId(page, path, pick = x => true) {
  const list = await page.evaluate(p => fetch(p).then(r => r.json()), path)
  const items = Array.isArray(list) ? list : list.alerts
  const it = items.find(pick)
  assert.ok(it, `demo data at ${path}`)
  return it.id
}

// ---- desktop ----
{
  const { ctx, page } = await session()
  const alert = await firstId(page, '/api/v1/alerts?view=open', a => a.priority >= 4)
  await go(page, `/alerts/${alert}`)
  assert.ok(await page.locator('.ib-detail, [class*="ib-"]').first().isVisible(), 'inbox renders')
  await shot(page, 'desktop-inbox')
  await go(page, '/sources')
  await shot(page, 'desktop-sources')
  await go(page, `/sources/${await firstId(page, '/api/v1/sources', x => x.kind === 'push')}`)
  await shot(page, 'desktop-source')
  await go(page, '/send-to')
  await shot(page, 'desktop-send-to')
  await go(page, '/rules')
  await shot(page, 'desktop-rules')
  await go(page, '/quiet')
  await shot(page, 'desktop-quiet')
  await go(page, '/activity', 2000)
  await shot(page, 'desktop-activity')
  await go(page, '/settings/apps')
  await shot(page, 'desktop-settings')
  await ctx.close()
}
// ---- desktop, dark ----
{
  const { ctx, page } = await session({ dark: true })
  await go(page, '/')
  await shot(page, 'desktop-inbox-dark')
  await go(page, '/activity', 2000)
  await shot(page, 'desktop-activity-dark')
  await ctx.close()
}
// ---- phone ----
{
  const { ctx, page } = await session({ phone: true })
  await go(page, '/')
  await shot(page, 'phone-inbox')
  await go(page, `/alerts/${await firstId(page, '/api/v1/alerts?view=open', a => a.priority >= 4)}`)
  await shot(page, 'phone-alert')
  await go(page, '/sources')
  await shot(page, 'phone-sources')
  await go(page, '/quiet')
  await shot(page, 'phone-quiet')
  await ctx.close()
}
{
  const { ctx, page } = await session({ phone: true, dark: true })
  await go(page, '/')
  await shot(page, 'phone-inbox-dark')
  await ctx.close()
}

await browser.close()
if (errors.length) { console.error(errors.join('\n')); process.exit(1) }
console.log('ok', shots ? `screenshots in ${dir}` : '(checks only)')
