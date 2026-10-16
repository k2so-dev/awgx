const { test, expect } = require('@playwright/test')
const { sh, awgxJSON, rules, watchErrors, login, api, open, draftDock, planDialog, toast, applyDraft, expectApplied, expectServerClean } = require('./lib')

const sorted = s => s.split('\n').map(l => l.trim()).sort().join('\n')

async function target(page) {
  const e = await api(page, '/api/egress')
  const inst = Object.keys(e.default)[0]
  const cur = e.default[inst].profile
  const p = e.profiles.find(p => p.kind === 'iface' && p.health === 'ok' && p.hops.length && p.name !== cur)
  return { e, inst, cur, profile: p && p.name, dev: p && p.hops[0] }
}

let errors
test.beforeEach(async ({ page }) => {
  sh('awgx egress reset')
  errors = watchErrors(page)
  await login(page)
})
test.afterEach(() => { expect(errors).toEqual([]) })

test('default route applies at once and rolls back from History', async ({ page }) => {
  await open(page, 'egress/routes')
  const t = await target(page)
  test.skip(!t.profile, 'no healthy iface profile')
  const before = rules()
  const applied = awgxJSON('egress status').applied
  const route = page.getByRole('combobox', { name: 'Default route for ' + t.inst })

  await route.selectOption(t.profile)
  await expect(draftDock(page)).toBeVisible()
  await applyDraft(page)
  await expectApplied(page)
  expect(rules()).not.toBe(before)
  expectServerClean()

  await page.getByRole('link', { name: /^History/ }).click()
  const row = page.locator('.hist-row').filter({ hasText: new RegExp('Snapshot #' + applied + '(?!\\d)') })
  await row.getByRole('button', { name: 'Roll back to this' }).click()
  await page.getByRole('dialog', { name: 'Roll back to snapshot #' + applied + '?' }).getByRole('button', { name: 'Apply snapshot #' + applied }).click()
  await expectApplied(page)
  expect(sorted(rules())).toBe(sorted(before))
  expectServerClean()
  await page.getByRole('link', { name: /^Routes/ }).click()
  await expect(route).toHaveValue(t.cur)
})

test('failed post-check keeps the change and offers a rollback', async ({ page }) => {
  await open(page, 'egress/routes')
  const t = await target(page)
  test.skip(!t.profile, 'no healthy iface profile')
  const before = rules()
  const dlg = planDialog(page)
  sh(`ip netns exec awgx nft -f - <<'EOF'
table inet e2e {
  chain forward {
    type filter hook forward priority -10; policy accept;
    iifname "${t.inst}" oifname "${t.dev}" drop
  }
}
EOF`)
  try {
    await page.getByRole('combobox', { name: 'Default route for ' + t.inst }).selectOption(t.profile)
    await applyDraft(page)
    await expect(dlg.getByRole('alert').filter({ hasText: 'post-check failed' })).toBeVisible({ timeout: 120000 })
    await expect(toast(page, 'a post-check failed')).toBeVisible()
    await expect(dlg.locator('.diff div', { hasText: /^fail / }).first()).toBeVisible()
    expect(rules()).not.toBe(before)
  } finally {
    sh('ip netns exec awgx nft delete table inet e2e 2>/dev/null || true')
  }
  await dlg.getByRole('button', { name: /^Roll back to #\d+$/ }).click()
  await expectApplied(page)
  expect(sorted(rules())).toBe(sorted(before))
  expectServerClean()
})

test('peer override from Assign peers and back', async ({ page }) => {
  await open(page, 'egress/routes')
  const t = await target(page)
  test.skip(!t.profile, 'no healthy iface profile')
  const rows = (await api(page, '/api/overview?rows=1')).rows
  const peer = rows.find(r => r.instance === t.inst && !t.e.peers[r.name])
  test.skip(!peer, 'every peer has an override')
  const before = rules()

  await page.getByRole('button', { name: 'Assign peers' }).click()
  const dlg = page.getByRole('dialog', { name: 'Assign peers' })
  await dlg.locator('.check-row').filter({ has: page.locator('span.medium', { hasText: new RegExp('^' + peer.name + '$') }) }).click()
  await dlg.getByLabel('Route').selectOption(t.profile)
  await dlg.getByRole('button', { name: 'Assign 1 peer' }).click()
  await expect(dlg).toBeHidden()
  await applyDraft(page)
  await expectApplied(page)
  expect(rules()).toContain('from ' + peer.address4 + ' iif ' + t.inst + ' lookup')
  expectServerClean()

  await page.getByRole('button', { name: 'Use the instance default for ' + peer.name }).click()
  await applyDraft(page)
  await expectApplied(page)
  expect(sorted(rules())).toBe(sorted(before))
  expectServerClean()
})

test('uplink test reports its checks', async ({ page }) => {
  await open(page, 'egress/uplinks')
  const e = await api(page, '/api/egress')
  test.skip(!(e.uplinks || []).length, 'no uplinks')
  const card = page.locator('.card').filter({ has: page.getByRole('heading', { name: e.uplinks[0].name, exact: true }) })
  await card.getByRole('button', { name: 'Test', exact: true }).click()
  await expect(card.locator('.diff div').first()).toBeVisible({ timeout: 60000 })
  await expect(card.locator('.diff div', { hasText: /^fail/i })).toHaveCount(0)
})
