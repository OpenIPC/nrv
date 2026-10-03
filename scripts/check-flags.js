#!/usr/bin/env node
/**
 * Проверяет переключатель языка на странице настроек сервера.
 *
 * Скрипт нужен, чтобы убедиться, что флаги действительно отрисовались:
 * картинки рисуются встроенным SVG, и ошибка в разметке не даёт ни
 * ошибки в консоли, ни заметного сбоя — просто пустое место вместо флага.
 * Поэтому проверяем не наличие элемента, а число путей внутри SVG.
 *
 * Запуск:
 *   node scripts/check-flags.js
 */

const { chromium } = require('playwright')
const path = require('path')

const BASE = process.env.NVR_URL || 'http://localhost:3001'
const USER = process.env.NVR_USER || 'admin'
const PASSWORD = process.env.NVR_PASSWORD || 'admin123'
// Снимки кладём во временный каталог: снимки для документации делает
// make-screenshots.js, и два скрипта, пишущие в один файл, затирали бы
// друг друга — какой отработал последним, тот и попал бы в README.
const OUT = process.env.NVR_OUT || '/tmp/nvr-flag-check'

async function main() {
  const browser = await chromium.launch()
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    deviceScaleFactor: 2,
  })
  const page = await context.newPage()
  require('fs').mkdirSync(OUT, { recursive: true })

  await page.goto(`${BASE}/login`, { waitUntil: 'domcontentloaded' })
  await page.fill('input[type="text"], input[name="username"]', USER)
  await page.fill('input[type="password"]', PASSWORD)
  await page.click('button[type="submit"]')
  await page.waitForTimeout(2500)

  await page.goto(`${BASE}/server`, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(2500)

  // Считаем элементы внутри каждой кнопки языка. Если флаг не отрисовался,
  // получим ноль и поймём, что дело в разметке, а не в переводе.
  const report = await page.$$eval('button', (buttons) =>
    buttons
      .filter((b) => b.querySelector('svg') && /Русский|English|中文|한국어/.test(b.textContent || ''))
      .map((b) => ({
        label: (b.textContent || '').trim(),
        shapes: b.querySelectorAll('svg *').length,
      })),
  )

  console.log('Кнопки языков:', JSON.stringify(report, null, 2))

  const card = await page.$('text=Русский')
  if (card) {
    const box = await card.boundingBox()
    await page.screenshot({
      path: path.join(OUT, 'flags-ru.png'),
      clip: { x: 0, y: Math.max(0, box.y - 90), width: 1440, height: 210 },
    })
  }

  // Проверяем, что переключение языка меняет подписи на самой странице.
  for (const [label, file] of [
    ['English', 'flags-en.png'],
    ['中文', 'flags-zh.png'],
    ['한국어', 'flags-ko.png'],
  ]) {
    await page.click(`button:has-text("${label}")`)
    await page.waitForTimeout(700)
    const h1 = await page.textContent('h1')
    console.log(`${label} -> заголовок страницы: ${h1}`)
    await page.screenshot({ path: path.join(OUT, file) })
  }

  await page.click('button:has-text("Русский")')
  await page.waitForTimeout(500)

  await browser.close()
}

main().catch((e) => {
  console.error(e)
  process.exit(1)
})
