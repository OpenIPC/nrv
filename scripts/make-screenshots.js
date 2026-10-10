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
  {
    file: '18-plans.png',
    title: 'Планы помещений',
    prepare: async (page) => {
      await page.goto(`${BASE}/plans`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(4000)
      await maskSecrets(page)
    },
  },
  {
    file: '19-switches.png',
    title: 'Коммутаторы: порты, питание PoE и устройства',
    prepare: async (page) => {
      await page.goto(`${BASE}/switches`, { waitUntil: 'domcontentloaded' })
      // Ждём опроса: состояние портов приходит отдельным запросом, и до
      // его завершения таблица пуста.
      await page.waitForTimeout(5000)
      // Открываем коммутатор, который сообщает порты в таблице MAC: на нём
      // видно и питание, и определение привязки камер. На части моделей
      // прошивка порт не сообщает, и раздел выглядит иначе.
      const sw = page.locator('button', { hasText: 'GPS204V3' }).first()
      if (await sw.count()) {
        await sw.click()
        await page.waitForTimeout(4000)
      }
      // Прокручиваем к таблице устройств: она ниже списка портов.
      await page.evaluate(() => {
        const el = [...document.querySelectorAll('h3')]
          .find((e) => e.textContent.includes('Устройства на портах'))
        if (el) el.scrollIntoView({ block: 'center' })
      })
      await page.waitForTimeout(1200)
    },
  },
  {
    file: '20-camera-network.png',
    title: 'Карточка камеры: подключение к коммутатору',
    prepare: async (page) => {
      await page.goto(`${BASE}/cameras/${DEMO_CAMERA}`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(9000)
      // Прокручиваем к блоку подключения: он в боковой колонке и без
      // прокрутки не попадает в кадр.
      await page.evaluate(() => {
        const el = [...document.querySelectorAll('h3')]
          .find((e) => e.textContent.includes('Подключение'))
        if (el) el.scrollIntoView({ block: 'center' })
      })
      await page.waitForTimeout(1500)
    },
  },
  {
    file: '21-camera-device.png',
    title: 'Карточка камеры: паспорт и состояние устройства',
    // Блок «Устройство» длинный: паспорт, состояние и потоки не помещаются
    // в обычный вьюпорт, и снимок обрывался бы на середине.
    viewport: { width: 1440, height: 1500 },
    prepare: async (page) => {
      await page.goto(`${BASE}/cameras/${DEMO_CAMERA}`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(9000)
      // Прокручиваем к блоку «Устройство»: он в боковой колонке ниже
      // «Подключения», и без прокрутки в кадр попадёт не то.
      await page.evaluate(() => {
        const el = [...document.querySelectorAll('h3')]
          .find((e) => e.textContent.trim() === 'Устройство')
        if (el) el.scrollIntoView({ block: 'start' })
      })
      await page.waitForTimeout(1500)
    },
  },
  {
    file: '26-language-ru.png',
    title: 'Настройки сервера: переключатель языка',
    viewport: { width: 1440, height: 1100 },
    prepare: async (page) => {
      // Порядок именно такой: сначала переходим на страницу, потом
      // переключаем язык. Переключатель есть только здесь, и попытка
      // нажать его на другой странице ничего не делает — снимок молча
      // получился бы на прежнем языке.
      await page.goto(`${BASE}/server`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(4500)
      await setLanguage(page, 'Русский')
      await page.evaluate(() => window.scrollTo(0, 0))
      await page.waitForTimeout(800)
    },
  },
  {
    file: '27-language-en.png',
    title: 'Настройки сервера: тот же экран по-английски',
    viewport: { width: 1440, height: 1100 },
    prepare: async (page) => {
      await page.goto(`${BASE}/server`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(3500)
      await setLanguage(page, 'English')
      await page.evaluate(() => window.scrollTo(0, 0))
      await page.waitForTimeout(800)
    },
  },
  {
    file: '28-language-zh.png',
    title: 'Настройки сервера: тот же экран по-китайски',
    viewport: { width: 1440, height: 1100 },
    prepare: async (page) => {
      // Тот же экран на другом языке: по снимку видно, что перевод
      // применяется целиком, включая боковое меню, а не только к надписи
      // в самой карточке.
      await page.goto(`${BASE}/server`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(3500)
      await setLanguage(page, '简体中文')
      await page.evaluate(() => window.scrollTo(0, 0))
      await page.waitForTimeout(800)
    },
  },
  {
    file: '29-language-ko.png',
    title: 'Настройки сервера: тот же экран по-корейски',
    viewport: { width: 1440, height: 1100 },
    prepare: async (page) => {
      await page.goto(`${BASE}/server`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(3500)
      await setLanguage(page, '한국어')
      await page.evaluate(() => window.scrollTo(0, 0))
      await page.waitForTimeout(800)
    },
  },
  {
    // Отдельный снимок для инструкции «как сменить язык вручную».
    //
    // Окно узкое, а страница прокручена к началу: так в кадр попадает
    // только переключатель. На общем снимке страницы он теряется среди
    // настроек времени и сети, и по нему не понять, куда нажимать.
    file: '30-language-switch.png',
    title: 'Переключатель языка — как сменить вручную',
    viewport: { width: 1000, height: 340 },
    prepare: async (page) => {
      await page.goto(`${BASE}/server`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(3500)
      await setLanguage(page, 'Русский')
      await page.evaluate(() => window.scrollTo(0, 0))
      await page.waitForTimeout(800)
    },
  },

  // --- Домофония: звонки с панелей на трубки и в приложение ---
  //
  // Снимки для инструкции docs/intercom.md. Панель одна на всех страницах,
  // поэтому по ним видно и вкладки раздела, и карточку абонента с тем, что
  // о нём знает Asterisk.
  {
    file: '31-intercom-accounts.png',
    title: 'Домофония: абоненты',
    prepare: async (page) => {
      await page.goto(`${BASE}/intercom`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(4000)
    },
  },
  {
    file: '32-intercom-groups.png',
    title: 'Домофония: группы вызова и номера групп',
    prepare: async (page) => {
      await page.goto(`${BASE}/intercom`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(3000)
      await page.locator('button:has-text("Группы вызова")').first().click()
      await page.waitForTimeout(1500)
    },
  },
  {
    file: '33-intercom-rules.png',
    title: 'Домофония: правила вызова',
    prepare: async (page) => {
      await page.goto(`${BASE}/intercom`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(3000)
      await page.locator('button:has-text("Правила вызова")').first().click()
      await page.waitForTimeout(1500)
    },
  },
  {
    file: '34-intercom-settings.png',
    title: 'Домофония: настройки сервера телефонии (внешний адрес, свои сети)',
    prepare: async (page) => {
      await page.goto(`${BASE}/intercom`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(3000)
      await page.locator('button:has-text("Настройки")').first().click()
      await page.waitForTimeout(1500)
    },
  },
  {
    file: '35-intercom-account.png',
    title: 'Домофония: карточка абонента (данные Asterisk, размещение, группы)',
    viewport: { width: 1440, height: 1500 },
    prepare: async (page) => {
      // Карточку открываем по идентификатору, который берём у сервера: он
      // у каждой установки свой, и вписывать его в скрипт нельзя.
      const id = await page.evaluate(async () => {
        const token = localStorage.getItem('token') || sessionStorage.getItem('token') || ''
        const response = await fetch('/api/v1/sip/accounts', {
          headers: { Authorization: `Bearer ${token}` },
        })
        if (!response.ok) return ''
        const accounts = await response.json()
        // Берём устройство, а не приложение: у него есть и адрес, и состояние
        // в Asterisk — то, ради чего этот снимок и делается.
        const device = accounts.find((a) => a.kind !== 'softphone') || accounts[0]
        return device ? device.id : ''
      })
      if (!id) {
        throw new Error('не удалось получить абонента для карточки')
      }
      await page.goto(`${BASE}/intercom/${id}`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(4500)
    },
  },
]

/**
 * Переключает язык интерфейса.
 *
 * Нажатие делается прямо в разметке, а не через поиск элемента: поверх
 * страницы остаётся слой всплывающих сообщений, и обычное нажатие по
 * координатам до кнопки не доходит.
 *
 * Выбор языка запоминается браузером, поэтому после съёмки возвращаем
 * русский: иначе следующий снимок в списке оказался бы на чужом языке.
 */
async function setLanguage(page, label) {
  await page.evaluate((text) => {
    const btn = [...document.querySelectorAll('button')]
      .find((b) => (b.innerText || '').trim() === text)
    if (btn) btn.click()
  }, label)
  await page.waitForTimeout(1200)
}

/**
 * Закрывает учётные данные в адресах потоков.
 *
 * Адрес RTSP вида rtsp://admin:пароль@192.168.1.11/... показывается в
 * карточке камеры как есть, и он попадал на снимки, которые уходят в
 * публичный репозиторий. Пароль камеры в открытом виде в документации —
 * это готовый доступ к потоку для любого, кто её читает.
 *
 * Затираем только пару логин:пароль, оставляя схему и адрес: по снимку
 * должно оставаться понятно, что это адрес потока, иначе он перестанет
 * что-либо объяснять.
 *
 * Функция вызывается для каждого снимка без исключения — так надёжнее,
 * чем полагаться на то, что автор новой записи вспомнит о маскировке.
 */
async function maskCredentials(page) {
  await page.evaluate(() => {
    // Логин и пароль между «//» и «@». Слэши и пробелы внутри не берём:
    // иначе под правило попал бы весь остаток строки до случайной собаки.
    const re = /(\/\/)[^\s/@:]+:[^\s/@]+@/g

    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    const nodes = []
    while (walker.nextNode()) {
      const n = walker.currentNode
      const v = n.nodeValue || ''
      if (v.includes('@') && v.includes('//')) nodes.push(n)
    }
    for (const n of nodes) {
      n.nodeValue = n.nodeValue.replace(re, '$1***:***@')
    }

    // Поля ввода: адрес может быть открыт в форме редактирования.
    document.querySelectorAll('input, textarea').forEach((el) => {
      if (el.value && el.value.includes('@') && el.value.includes('//')) {
        el.value = el.value.replace(re, '$1***:***@')
      }
    })
  })
}

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
  // Фильтр по части имени файла: при правке одной страницы пересъёмка
  // всех тридцати снимков занимает минуты и заново трогает файлы, к
  // которым правка отношения не имеет. Запуск: NVR_ONLY=language node ...
  const only = process.env.NVR_ONLY || ''
  const shots = only ? SHOTS.filter((s) => s.file.includes(only)) : SHOTS
  if (only && shots.length === 0) {
    console.log(`Нет снимков с «${only}» в имени.`)
  }
  for (const shot of shots) {
    try {
      // Размер окна можно задать для отдельного снимка: длинные блоки не
      // помещаются в общий вьюпорт, и без этого снимок обрывался бы.
      await page.setViewportSize(shot.viewport || { width: 1440, height: 900 })
      await shot.prepare(page)
      // Маскировка паролей в адресах потоков — для каждого снимка, без
      // исключений: снимки публикуются, и одна забытая запись открыла бы
      // доступ к камере.
      await maskCredentials(page)
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
  console.log(`\nСохранено снимков: ${done.length} из ${shots.length}`)
  console.log(`Каталог: ${OUT}`)
}

main().catch((e) => {
  console.error('Ошибка:', e.message)
  process.exit(1)
})
