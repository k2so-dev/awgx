const { chromium } = require('@playwright/test')
const path = require('node:path')

const base = process.env.AWGX_URL || 'http://127.0.0.1:17457'
const out = process.env.AWGX_SHOTS || path.join(__dirname, '../../docs/screenshots')
const masks = JSON.parse(process.env.AWGX_MASKS || '{}')
const clients = process.env.AWGX_CLIENT_IP || ''
const pool = ['198.51.100.23', '203.0.113.87', '192.0.2.145', '198.51.100.201', '203.0.113.34', '192.0.2.61', '198.51.100.9']
const only = process.argv.slice(2)

const period = page => page.getByRole('radio', { name: '1h', exact: true }).filter({ visible: true }).first().click()

const shots = [
  { name: 'dashboard', hash: 'dashboard', ready: '.card', act: period },
  { name: 'peers', hash: 'peers', ready: 'table tbody tr' },
  {
    name: 'config', hash: 'peers', ready: 'table tbody tr',
    act: async page => {
      await page.locator('tr', { hasText: 'alice-iphone' }).getByRole('button', { name: /config/i }).click()
      await page.locator('dialog[open]').waitFor()
    }
  },
  { name: 'stats', hash: 'stats', ready: 'svg', act: period },
  { name: 'instances', hash: 'instances', ready: '.card' },
  { name: 'egress', hash: 'egress/routes', ready: '.card' },
  { name: 'uplinks', hash: 'egress/uplinks', ready: '.card' },
  { name: 'doctor', hash: 'doctor', ready: '.card', act: page => page.getByRole('button', { name: /Show/ }).filter({ visible: true }).first().click() },
  {
    name: 'plan', hash: 'egress/routes', ready: '.card',
    act: async page => {
      const route = page.getByRole('combobox', { name: /^Default route for / }).first()
      await route.selectOption(process.env.AWGX_PLAN_PROFILE)
      await page.getByRole('region', { name: 'Draft' }).getByRole('button', { name: 'Review and apply' }).click()
      await page.getByRole('dialog', { name: 'Review and apply' }).locator('.diff').first().waitFor()
    }
  }
]

async function mask(page) {
  await page.evaluate(([m, c, pool]) => {
    const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    for (let n = w.nextNode(); n; n = w.nextNode()) {
      let t = n.nodeValue
      for (const [k, v] of Object.entries(m)) t = t.split(k).join(v)
      if (c) t = t.replace(new RegExp(c.replaceAll('.', '\\.') + ':(\\d+)', 'g'), (_, port) => pool[port % pool.length] + ':' + port)
      if (t !== n.nodeValue) n.nodeValue = t
    }
    for (const el of document.querySelectorAll('input, textarea')) {
      for (const [k, v] of Object.entries(m)) el.value = el.value.split(k).join(v)
    }
  }, [masks, clients, pool])
}

async function run(theme) {
  const browser = await chromium.launch({ channel: 'chrome' })
  const ctx = await browser.newContext({ baseURL: base, viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: theme })
  const page = await ctx.newPage()
  await page.goto('/')
  await page.locator('#pw').fill(process.env.AWGX_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.getByRole('navigation', { name: 'Main' }).waitFor()
  for (const s of shots) {
    if (only.length && !only.includes(s.name)) continue
    await page.goto('/#/' + s.hash)
    await page.locator(s.ready + ':visible').first().waitFor()
    if (s.act) await s.act(page)
    await page.waitForLoadState('networkidle')
    await page.waitForTimeout(1500)
    await mask(page)
    await page.screenshot({ path: path.join(out, `${s.name}-${theme}.png`) })
    await page.keyboard.press('Escape')
  }
  await page.evaluate(async () => {
    const { csrf } = await fetch('/api/session').then(r => r.json())
    await fetch('/api/egress/reset', { method: 'POST', headers: { 'X-CSRF-Token': csrf } })
  })
  await browser.close()
}

;(async () => {
  for (const theme of ['light', 'dark']) await run(theme)
})().catch(e => { console.error(e); process.exit(1) })
