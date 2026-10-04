/*
 * Проверка одного экрана на непереведённые подписи.
 *
 * Зачем отдельный скрипт: переводы переносятся по файлам постепенно, и после
 * каждой правки нужно убедиться, что на экране не осталось русского при
 * выбранном китайском или корейском языке. Глазами это ловится плохо —
 * забытая строка теряется среди переведённых, а на редкий экран (например
 * «Планы») вообще можно не зайти.
 *
 * Запуск:
 *   node scripts/check-i18n-page.js /plans zh-CN
 *   node scripts/check-i18n-page.js /switches ko
 *
 * Кириллица в именах устройств — это данные из базы, они и должны оставаться
 * русскими, поэтому скрипт их показывает, но помечает как возможные.
 */
const { chromium } = require('playwright')

const BASE = process.env.NVR_BASE || 'http://192.168.1.111:3001'
const path = process.argv[2] || '/'
const lang = process.argv[3] || 'zh-CN'

;(async () => {
  const browser = await chromium.launch()
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } })

  // Язык кладём в localStorage до первой загрузки: так проверяем именно
  // сохранённый выбор, а не автоопределение по языку браузера.
  await page.addInitScript((l) => localStorage.setItem('nvr_language', l), lang)

  await page.goto(BASE + path, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)

  // Токен берём у API напрямую, а не через форму входа: форма живёт на другом
  // порту и в другом процессе, и при проверке перевода ошибка в ней выглядит
  // как непереведённый экран — то есть скрипт сообщал бы не о том.
  if (await page.locator('input[type="password"]').count()) {
    const api = process.env.NVR_API || 'http://192.168.1.111:8080'
    const res = await fetch(`${api}/api/v1/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        username: process.env.NVR_LOGIN || 'admin',
        password: process.env.NVR_PASSWORD || 'admin123',
      }),
    })
    const body = await res.json()
    if (!body.token) throw new Error('не удалось получить токен: ' + JSON.stringify(body).slice(0, 200))
    await page.evaluate((tok) => localStorage.setItem('token', tok), body.token)
    await page.goto(BASE + path, { waitUntil: 'networkidle' })
  }

  await page.waitForTimeout(2500)

  // NVR_TAB — текст кнопки, которую надо нажать перед проверкой.
  // Нужен для экранов, где часть подписей лежит на вкладке, которая по
  // умолчанию не открыта: иначе проверка молча смотрит не туда и
  // показывает, что всё переведено.
  if (process.env.NVR_TAB) {
    // Ищем по тексту, а если не нашли — по CSS-селектору: часть кнопок
    // подписана только значком (например редактирование контроллера),
    // и по тексту их не найти.
    let target = page.locator('button', { hasText: process.env.NVR_TAB }).first()
    if ((await target.count()) === 0) {
      target = page.locator(process.env.NVR_TAB).first()
    }
    if (await target.count()) {
      await target.click()
      await page.waitForTimeout(1500)
    } else {
      console.log(`Кнопка «${process.env.NVR_TAB}» не найдена — проверяю открытую вкладку.`)
    }
  }

  const found = await page.evaluate(() => {
    const out = []
    const walk = (node) => {
      if (node.nodeType === 3) {
        const s = node.textContent.trim()
        if (s && /[А-Яа-яЁё]{3,}/.test(s)) {
          const el = node.parentElement
          out.push({ text: s.slice(0, 120), tag: el ? el.tagName : '?', cls: el ? el.className : '' })
        }
        return
      }
      if (node.nodeType !== 1) return
      for (const attr of node.attributes || []) {
        if (/^(title|placeholder|aria-label)$/.test(attr.name) && /[А-Яа-яЁё]{3,}/.test(attr.value)) {
          out.push({ text: attr.value.slice(0, 120), tag: attr.name, cls: node.className })
        }
      }
      for (const child of node.childNodes) walk(child)
    }
    walk(document.body)
    return out
  })

  console.log(`Экран ${path}, язык ${lang}.`)
  if (found.length === 0) {
    console.log('Русских подписей не найдено.')
  } else {
    console.log(`Найдено мест с кириллицей: ${found.length}`)
    for (const f of found) console.log(`  [${f.tag}] ${f.text}`)
  }

  await browser.close()
})()
