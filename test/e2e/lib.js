const { execFileSync } = require('node:child_process')
const { expect } = require('@playwright/test')

function sh(cmd) {
  return execFileSync('ssh', ['-o', 'BatchMode=yes', process.env.AWGX_SSH, 'sudo sh -s'], { input: cmd, encoding: 'utf8' })
}

function awgxJSON(args) { return JSON.parse(sh('awgx ' + args + ' --json')) }

function rules() { return sh('ip -n awgx rule show').trim() }

function watchErrors(page) {
  const errors = []
  page.on('pageerror', e => errors.push(e.message))
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()) })
  return errors
}

async function login(page) {
  await page.goto('/')
  await page.locator('#pw').fill(process.env.AWGX_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible()
}

async function api(page, path) {
  return page.evaluate(p => fetch(p).then(r => r.json()), path)
}

async function open(page, hash) {
  await page.goto('/#/' + hash)
  await expect(page.getByRole('heading', { name: 'Egress', level: 1 })).toBeVisible()
}

function draftDock(page) { return page.getByRole('region', { name: 'Draft' }) }

function planDialog(page) { return page.getByRole('dialog', { name: 'Review and apply' }) }

function toast(page, text) { return page.locator('.toasts [role=status]').filter({ hasText: text }) }

async function applyDraft(page) {
  await draftDock(page).getByRole('button', { name: 'Review and apply' }).click()
  const dlg = planDialog(page)
  await expect(dlg).toBeVisible()
  await dlg.getByRole('button', { name: 'Apply', exact: true }).click()
  return dlg
}

async function expectApplied(page) {
  await expect(toast(page, /Snapshot #\d+ applied/).last()).toBeVisible({ timeout: 120000 })
  await expect(planDialog(page)).toBeHidden()
  await expect(draftDock(page)).toBeHidden()
  await expect(page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Egress', exact: true })).toBeVisible()
  await expect(page.locator('dialog[open]')).toHaveCount(0)
}

function expectServerClean() {
  expect(awgxJSON('egress plan').changed).toBe(false)
  expect(awgxJSON('egress status').drift || []).toEqual([])
}

module.exports = { sh, awgxJSON, rules, watchErrors, login, api, open, draftDock, planDialog, toast, applyDraft, expectApplied, expectServerClean }
