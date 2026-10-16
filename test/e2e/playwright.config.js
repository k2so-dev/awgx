const { defineConfig } = require('@playwright/test')

module.exports = defineConfig({
  testDir: '.',
  testMatch: '*.spec.js',
  workers: 1,
  timeout: 240000,
  expect: { timeout: 30000 },
  reporter: [['list']],
  use: {
    baseURL: process.env.AWGX_URL || 'http://127.0.0.1:17457',
    channel: 'chrome',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure'
  }
})
