// Разовая проверка вкладки «Обновления» на странице /server.
//
// NVR_FAKE_BEHIND=1 подменяет ответ проверки на «есть новая версия»: так
// видно список изменений и кнопку установки, не дожидаясь реального
// расхождения с репозиторием.
const { chromium } = require('playwright')

const URL = process.env.NVR_URL || 'http://192.168.1.111:3001'
const USER = process.env.NVR_USER || 'admin'
const PASS = process.env.NVR_PASS || 'admin123'
const FAKE = process.env.NVR_FAKE_BEHIND === '1'

const fakeCheck = {
  available: true,
  repo: 'https://github.com/OpenIPC/nrv',
  branch: 'main',
  check: {
    dir: '/opt/nvr',
    branch: 'main',
    current: {
      sha: 'aaaaaaaaaaaaaaaa', short: 'aaaaaaa1',
      date: '2026-10-01T10:00:00+03:00', subject: 'Прошлый релиз',
    },
    remote: {
      sha: 'bbbbbbbbbbbbbbbb', short: 'bbbbbbb2',
      date: '2026-10-10T13:38:51+03:00', subject: 'Новый релиз',
    },
    behind: true,
    commits: [
      'bbbbbbb2 Обновления: установка из репозитория через вкладку сервера',
      'ccccccc3 Домофония: звонки с телефона, группы с номерами',
      'ddddddd4 Документация: установка на Astra, Debian и Ubuntu',
    ],
    diff: '109 files changed, 14083 insertions(+), 82 deletions(-)',
    dirty: false,
    compose: true,
    free_gb: 29.7,
    enough_space: true,
    previous: { sha: 'aaaaaaa1', branch: 'main' },
  },
}

;(async () => {
  const browser = await chromium.launch()
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })

  await page.goto(URL, { waitUntil: 'networkidle' })
  await page.fill('input[type="text"], input[name="username"]', USER)
  await page.fill('input[type="password"]', PASS)
  await page.keyboard.press('Enter')
  await page.waitForTimeout(2500)

  if (FAKE) {
    await page.route('**/api/v1/updates/check*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(fakeCheck) }),
    )
  }

  await page.goto(`${URL}/server`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1200)

  // Переходим на вкладку обновлений.
  await page.getByRole('button', { name: /Обновления|Updates/ }).first().click()
  await page.waitForTimeout(2500)

  const text = await page.locator('body').innerText()
  console.log('--- содержимое вкладки ---')
  console.log(text.split('\n').filter(Boolean).slice(0, 30).join('\n'))

  const shot = FAKE ? '/tmp/updates-tab-new-version.png' : '/tmp/updates-tab.png'
  await page.screenshot({ path: shot, fullPage: true })
  console.log('--- снимок:', shot, '---')

  await browser.close()
})().catch((err) => {
  console.error('ОШИБКА:', err.message)
  process.exit(1)
})
