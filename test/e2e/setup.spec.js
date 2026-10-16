const { test, expect } = require('@playwright/test')
const { sh, watchErrors, login } = require('./lib')

const conf = '/etc/wireguard/e2eold.conf'

async function review(page, backup) {
  await page.goto('/#/system/setup')
  await page.reload()
  await page.getByRole('button', { name: 'Start inspection' }).click()
  const picks = page.locator('.opt-card[role=checkbox]:visible')
  await expect(picks.first()).toBeVisible({ timeout: 60000 })
  for (const card of await picks.all()) {
    const want = (await card.textContent()).includes('System check')
    if ((await card.getAttribute('aria-checked') === 'true') !== want) await card.click()
  }
  await page.getByRole('button', { name: 'Continue' }).click()
  await page.getByRole('checkbox', { name: new RegExp('config ' + conf) }).click()
  const sw = page.getByRole('switch', { name: 'Back up before removing' })
  await expect(sw).toHaveAttribute('aria-checked', 'false')
  if (backup) await sw.click()
  await page.getByRole('button', { name: 'Continue' }).click()
  const verb = backup ? 'back up and remove' : 'remove'
  await expect(page.getByText(new RegExp('^' + verb + ' config ' + conf + '$'))).toBeVisible()
}

test('setup wizard removes old setups without a backup unless asked', async ({ page }) => {
  const errors = watchErrors(page)
  sh(`mkdir -p /etc/wireguard && printf '[Interface]\\nPrivateKey = aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa=\\nListenPort = 51999\\n' > ${conf}`)
  try {
    await login(page)
    await review(page, false)
    await review(page, true)
  } finally {
    sh(`rm -f ${conf}; rmdir /etc/wireguard 2>/dev/null || true`)
  }
  expect(errors).toEqual([])
})
