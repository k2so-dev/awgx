const { test, expect } = require('@playwright/test')
const { sh, awgxJSON, watchErrors, login, api, open, draftDock, toast, expectServerClean } = require('./lib')

async function jobDone(page) {
  await expect(toast(page, /: done| failed/).last()).toBeVisible({ timeout: 180000 })
  await expect(toast(page, / failed/)).toHaveCount(0)
  await expect(page.locator('section.task')).toBeHidden()
}

async function disconnect(page) {
  await open(page, 'egress/xui')
  await page.getByRole('button', { name: 'Disconnect', exact: true }).click()
  const dlg = page.getByRole('dialog', { name: 'Disconnect 3x-ui' })
  await dlg.getByRole('button', { name: 'Disconnect', exact: true }).click()
  await expect(dlg).toBeHidden()
  await jobDone(page)
  await expect(page.getByRole('heading', { name: '3x-ui is installed on this server' })).toBeVisible()
  await expect(draftDock(page)).toBeHidden()
  expect(awgxJSON('xui status').connected).toBe(false)
  expectServerClean()
}

async function connect(page) {
  await open(page, 'egress/xui')
  const x = await api(page, '/api/xui')
  await page.getByRole('button', { name: 'Connect 3x-ui' }).click()
  const dlg = page.getByRole('dialog', { name: 'Connect 3x-ui' })
  if (!x.saved_auth) await dlg.getByRole('switch', { name: 'Use the fallback token' }).click()
  await dlg.getByRole('button', { name: 'Connect', exact: true }).click()
  await expect(dlg).toBeHidden()
  await jobDone(page)
  await expect(page.locator('.card-head').filter({ hasText: '3x-ui' }).getByText('connected', { exact: true })).toBeVisible()
  await expect(draftDock(page)).toBeHidden()
  const st = awgxJSON('xui status')
  expect(st.connected).toBe(true)
  expect(st.problem || '').toBe('')
  expectServerClean()
}

test('3x-ui connect and disconnect finish without a confirm step', async ({ page }) => {
  const errors = watchErrors(page)
  sh('awgx egress reset')
  await login(page)
  const x = await api(page, '/api/xui')
  test.skip(!x.installed, '3x-ui is not installed')
  if (x.status && x.status.connected) {
    await disconnect(page)
    await connect(page)
  } else {
    await connect(page)
    await disconnect(page)
  }
  expect(errors).toEqual([])
})
