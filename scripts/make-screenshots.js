#!/usr/bin/env node
/**
 * Делает скриншоты веб-интерфейса для документации.
 *
 * Запуск:
 *   node scripts/make-screenshots.js
 *
 * Требуется запущенный сервер (WebUI на :3001) и вход администратора.
 * Адрес и данные можно переопределить переменными окружения:
 *   NVR_URL, NVR_USER, NVR_PASSWORD, NVR_OUT
 *
 * Скриншоты сохраняются в docs/screenshots и подключаются в README.
 */

const { chromium } = require('playwright')
const path = require('path')
const fs = require('fs')

const BASE = process.env.NVR_URL || 'http://localhost:3001'
const USER = process.env.NVR_USER || 'admin'
const PASSWORD = process.env.NVR_PASSWORD || 'admin123'
const OUT = process.env.NVR_OUT || path.join(__dirname, '..', 'docs', 'screenshots')

// Камера со звуком: на её карточке видны плеер, вкладки и настройки звука.
const DEMO_CAMERA = process.env.NVR_CAMERA || '3c5dc0fa-c1b9-4950-8def-f90eb41321b3'

/** Описание снимков: имя файла и что на нём. */
const SHOTS = [
  {
    file: '01-login.png',
    title: 'Вход',
    prepare: async (page) => {
      await page.goto(`${BASE}/login`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(1200)
    },
  },
  {
    file: '02-dashboard.png',
    title: 'Дашборд',
    prepare: async (page) => {
      await page.goto(`${BASE}/`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(2500)
    },
  },
  {
    file: '03-cameras.png',
    title: 'Камеры',
    prepare: async (page) => {
      await page.goto(`${BASE}/cameras`, { waitUntil: 'domcontentloaded' })
      // Ждём загрузки превью потоков: карточки показывают живые кадры.
      await page.waitForTimeout(14000)
    },
  },
  {
    file: '04-camera-live.png',
    title: 'Карточка камеры: просмотр',
    prepare: async (page) => {
      await page.goto(`${BASE}/cameras/${DEMO_CAMERA}`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(16000)
    },
  },
  {
    file: '05-camera-audio.png',
    title: 'Карточка камеры: настройки звука',
    prepare: async (page) => {
      await page.goto(`${BASE}/cameras/${DEMO_CAMERA}`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(9000)
      await page.locator('button:has-text("Звук")').first().click()
      await page.waitForTimeout(4000)
      // Показываем блок целиком: состояние, переключатели, детекция, динамик.
      await page.evaluate(() => {
        const el = [...document.querySelectorAll('h3, h4')]
          .find((e) => e.textContent.includes('Детекция звуковых'))
        if (el) el.scrollIntoView({ block: 'center' })
      })
      await page.waitForTimeout(1200)
    },
  },
  {
    file: '06-camera-detection.png',
    title: 'Карточка камеры: настройки детекции',
    prepare: async (page) => {
      await page.goto(`${BASE}/cameras/${DEMO_CAMERA}`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(9000)
      await page.locator('button:has-text("Детекция")').first().click()
      await page.waitForTimeout(4000)
    },
  },
  {
    file: '07-events.png',
    title: 'События детекции',
    prepare: async (page) => {
      await page.goto(`${BASE}/events`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(6000)
    },
  },
  {
    file: '08-audio-events.png',
    title: 'События звука',
    prepare: async (page) => {
      await page.goto(`${BASE}/audio-events`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(5000)
    },
  },
  {
    file: '09-recordings.png',
    title: 'Архив записей',
    prepare: async (page) => {
      await page.goto(`${BASE}/recordings`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(5000)
    },
  },
  {
    file: '10-recognition.png',
    title: 'Распознавание лиц и номеров',
    prepare: async (page) => {
      await page.goto(`${BASE}/recognition`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(5000)
    },
  },
  {
    file: '11-settings.png',
    title: 'Настройки сервера',
    prepare: async (page) => {
      await page.goto(`${BASE}/settings`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(5000)
    },
  },
  {
    file: '12-scanner.png',
    title: 'Сканер камер',
    prepare: async (page) => {
      await page.goto(`${BASE}/scanner`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(2500)
    },
  },
  {
    file: '13-acs.png',
    title: 'СКУД',
    prepare: async (page) => {
      await page.goto(`${BASE}/acs`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(4000)
    },
  },
  {
    file: '14-notify-telegram.png',
    title: 'Уведомления: Telegram',
    prepare: async (page) => {
      await page.goto(`${BASE}/notifications`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(4000)
      // Токен и chat_id — рабочие данные, в документацию они попадать не должны.
      await maskSecrets(page)
    },
  },
  {
    file: '15-notify-max.png',
    title: 'Уведомления: MAX',
    prepare: async (page) => {
      await page.goto(`${BASE}/notifications`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(4000)
      await page.locator('button:has-text("MAX")').first().click()
      await page.waitForTimeout(1500)
      await maskSecrets(page)
    },
  },
  {
    file: '16-notify-system.png',
    title: 'Уведомления: состояние сервера и камер',
    prepare: async (page) => {
      await page.goto(`${BASE}/notifications`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(4000)
      await page.locator('button:has-text("Сервер")').first().click()
      await page.waitForTimeout(1500)
      // Показываем пороги сверху: дальше идут списки каналов и журнал,
      // которые на снимке шириной в экран всё равно не поместятся.
      await page.evaluate(() => {
        const el = [...document.querySelectorAll('h3, h4')]
          .find((e) => /порог|проверк|отслеж/i.test(e.textContent))
        if (el) el.scrollIntoView({ block: 'start' })
      })
      await page.waitForTimeout(1200)
      await maskSecrets(page)
    },
  },
  {
    file: '17-notify-log.png',
    title: 'Журнал отправки уведомлений',
    prepare: async (page) => {
      await page.goto(`${BASE}/notifications`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(4000)
      await page.evaluate(() => {
        const el = [...document.querySelectorAll('h3, h4')]
          .find((e) => /журнал/i.test(e.textContent))
        if (el) el.scrollIntoView({ block: 'start' })
        else window.scrollTo(0, document.body.scrollHeight)
      })
      await page.waitForTimeout(1200)
      await maskSecrets(page)
    },
  },
]

/**
 * Закрывает токены и адреса чатов на снимке.
 *
 * Страница уведомлений хранит рабочие боты и chat_id, а снимки попадают
 * в репозиторий и в документацию. Токен бота — это ключ доступа к боту:
 * по нему можно писать в чат от его имени, поэтому в кадр он попадать
 * не должен. Поля не очищаем, а затираем: пустое поле выглядело бы как
 * незаполненная настройка, и по снимку было бы не понять, что там что-то есть.
 */
async function maskSecrets(page) {
  await page.evaluate(() => {
    const mask = '***'
    // Поля ввода: токен бота, chat_id, адрес прокси.
    //
    // На chat_id ориентируемся по подписи рядом с полем, а не по имени:
    // поля описаны через label, а не через атрибут name, и по атрибутам
    // chat_id неотличим от обычного числа.
    for (const input of document.querySelectorAll('input')) {
      const type = (input.getAttribute('type') || '').toLowerCase()
      const own = (input.getAttribute('placeholder') || '') + (input.name || '') +
        (input.id || '') + (input.getAttribute('aria-label') || '')
      const label = input.closest('label')
      const wrapper = input.closest('div')
      const nearby = (label?.textContent || '') + (wrapper?.textContent || '')
      // Ограничиваем окрестность: у обёртки может быть общий предок
      // со всей страницей, и тогда под маску попадёт всё подряд.
      const scope = nearby.length < 300 ? nearby : ''

      if (type === 'password' || /token|chat|прокси|proxy/i.test(own + scope)) {
        input.value = mask
        input.setAttribute('value', mask)
      }
    }
    // Токены, уже показанные текстом (журнал доставки).
    for (const el of document.querySelectorAll('td, span, code, div')) {
      const text = (el.textContent || '').trim()
      if (/^\d{6,}:[A-Za-z0-9_-]{20,}$/.test(text)) {
        el.textContent = mask
      }
    }
  })
}

async function main() {
  fs.mkdirSync(OUT, { recursive: true })

  const browser = await chromium.launch()
  // Широкий вьюпорт: интерфейс рассчитан на десктоп, при 1440 всё помещается.
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    locale: 'ru-RU',
  })
  const page = await context.newPage()

  // Вход выполняем один раз: токен остаётся в localStorage и переиспользуется.
  await page.goto(`${BASE}/login`, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(1200)
  await page.locator('input').first().fill(USER)
  await page.locator('input[type="password"]').fill(PASSWORD)
  await page.locator('button:has-text("Войти")').click()
  await page.waitForTimeout(2500)

  const done = []
  for (const shot of SHOTS) {
    try {
      await shot.prepare(page)
      await page.screenshot({
        path: path.join(OUT, shot.file),
        fullPage: false,
        type: 'png',
      })
      const size = fs.statSync(path.join(OUT, shot.file)).size
      done.push({ ...shot, size })
      console.log(`  OK   ${shot.file.padEnd(26)} ${Math.round(size / 1024)} KB  — ${shot.title}`)
    } catch (e) {
      console.log(`  СБОЙ ${shot.file.padEnd(26)} ${String(e.message).slice(0, 90)}`)
    }
  }

  await browser.close()
  console.log(`\nСохранено снимков: ${done.length} из ${SHOTS.length}`)
  console.log(`Каталог: ${OUT}`)
}

main().catch((e) => {
  console.error('Ошибка:', e.message)
  process.exit(1)
})
