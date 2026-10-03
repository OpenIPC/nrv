#!/usr/bin/env node
/**
 * Делает скриншоты нашей интеграции в Home Assistant.
 *
 * Запуск:
 *   HA_URL=http://192.168.1.30:8123 HA_USER=himik HA_PASSWORD=... \
 *     node scripts/make-ha-screenshots.js
 *
 * Отдельный скрипт от снимков нашего веб-интерфейса: там браузер работает
 * с нашим же приложением, а здесь — с чужим, и вход устроен иначе,
 * с сохранением сессии. Держать это в одном файле значило бы усложнять
 * оба сценария ради экономии нескольких строк.
 *
 * Снимки сохраняются в docs/screenshots с префиксом ha-.
 */

const { chromium } = require('playwright')
const path = require('path')
const fs = require('fs')

const BASE = process.env.HA_URL || 'http://192.168.1.30:8123'
const USER = process.env.HA_USER || 'himik'
const PASSWORD = process.env.HA_PASSWORD || ''
const OUT = process.env.HA_OUT || path.join(__dirname, '..', 'docs', 'screenshots')

// Размер окна, под который рассчитаны снимки. Панель ассистента тянется
// под ширину, и на широком окне карточки расползаются так, что на снимке
// читать их неудобно.
const VIEWPORT = { width: 1366, height: 900 }

// Устройство камеры, которое попадает на снимок карточки устройства.
// Список устройств в ассистенте разложен по теневой разметке, и надёжно
// выбрать из него нужное переходом по ссылке не получается.
const DEMO_DEVICE =
  process.env.HA_DEVICE || '1e128648590b9ac98a8c4e946c014277'

async function login(browser) {
  const context = await browser.newContext({
    viewport: VIEWPORT,
    locale: 'ru-RU',
    // Часовой пояс задаём явно: ассистент показывает время событий
    // в поясе браузера, и без этого снимки могли бы разойтись по времени.
    timezoneId: 'Europe/Moscow',
  })
  const page = await context.newPage()

  await page.goto(`${BASE}/`, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(2500)

  // Форма входа: ассистент запоминает сессию, поэтому проверяем, показана
  // ли она вообще — при повторном запуске скрипта вход уже сделан.
  //
  // Поля ищем по именам, а не по типу: на странице есть и флажок
  // «Запомнить», и поиск по типу легко попадает не в то поле.
  const loginField = page.locator('input[name="username"]').first()
  if (await loginField.count()) {
    await loginField.fill(USER)
    await page.locator('input[name="password"]').first().fill(PASSWORD)

    // Отправляем форму клавишей. Кнопка входа объявлена внутри теневой
    // разметки компонента ассистента, и обычный поиск по тексту её
    // не находит — нажатие клавиши работает всегда.
    await page.locator('input[name="password"]').first().press('Enter')
    await page.waitForTimeout(6000)
  }

  return { context, page }
}

async function shoot(page, file, prepare) {
  try {
    await prepare(page)
    await page.waitForTimeout(1200)
  } catch (err) {
    // Один неудавшийся снимок не должен отменять остальные: разделы
    // ассистента устроены по-разному, и часть из них рисуется целиком
    // в теневой разметке, куда обычный поиск не достаёт.
    console.warn(`  ${file} — пропущен: ${err.message.split('\n')[0]}`)
    return
  }

  const target = path.join(OUT, file)
  await page.screenshot({ path: target })
  const size = fs.statSync(target).size
  console.log(`  ${file} — ${Math.round(size / 1024)} КБ`)
}

async function main() {
  if (!PASSWORD) {
    console.error('Не задан пароль: укажите HA_PASSWORD')
    process.exit(1)
  }

  fs.mkdirSync(OUT, { recursive: true })

  const browser = await chromium.launch()
  const { context, page } = await login(browser)

  console.log('Снимки интеграции:')

  await shoot(page, '22-ha-integration.png', async (p) => {
    // Страница интеграции: по ней видно, что компонент опознан ассистентом
    // и сколько он даёт устройств и сущностей.
    await p.goto(`${BASE}/config/integrations/integration/openipc_nvr`, {
      waitUntil: 'domcontentloaded',
    })
    await p.waitForSelector('text=OpenIPC NVR (192.168.1.111)', { timeout: 30000 })
  })

  await shoot(page, '23-ha-camera-device.png', async (p) => {
    // Карточка устройства камеры: камера, детекция, кнопки управления
    // и история срабатываний.
    //
    // Адрес устройства берётся из переменной окружения, а не находится
    // переходом по списку: ссылки в списке объявлены внутри теневой
    // разметки, и поиск по ним то срабатывает, то нет. Устройство нужно
    // ровно одно — то, которое попадёт на снимок.
    await p.goto(`${BASE}/config/devices/device/${DEMO_DEVICE}`, {
      waitUntil: 'domcontentloaded',
    })
    await p.waitForTimeout(5000)
  })

  await shoot(page, '24-ha-card-events.png', async (p) => {
    // Наша карточка на вкладке видео: лента событий с кадрами.
    await p.goto(`${BASE}/lovelace/video`, { waitUntil: 'domcontentloaded' })
    await p.waitForTimeout(9000)
  })

  await shoot(page, '25-ha-archive.png', async (p) => {
    // Просмотр архива. Открывается как самостоятельная страница ассистента;
    // сам список файлов подгружается внутри компонента, поэтому ждём
    // подольше и сразу снимаем — без перехода по папкам, который
    // целиком упирается в теневую разметку.
    await p.goto(`${BASE}/media-browser/browser`, { waitUntil: 'domcontentloaded' })
    await p.waitForTimeout(8000)
  })

  await context.close()
  await browser.close()
  console.log('Готово. Снимки в', OUT)
}

main().catch((err) => {
  console.error('Ошибка:', err.message)
  process.exit(1)
})
