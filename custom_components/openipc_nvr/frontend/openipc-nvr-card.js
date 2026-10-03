/**
 * Карточка последних событий OpenIPC NVR.
 *
 * Показывает ленту срабатываний с кадрами: камера, что обнаружено, когда.
 * Сделана по образцу карточки Frigate — там лента событий с превью, и это
 * оказалось удобнее, чем список сущностей: видно, что произошло, не открывая
 * ничего по отдельности.
 *
 * Данные карточка не запрашивает сама. Она читает описание датчика, которое
 * уже лежит в ассистенте, поэтому ей не нужны ни адрес сервера, ни учётные
 * данные — их негде было бы хранить, кроме конфигурации панели, а это
 * показало бы пароли всем, кто её откроет.
 *
 * Собрана без сборщика: один файл, который браузер загружает как обычный
 * модуль. Так её можно положить в /config/www и проверить, не собирая
 * ничего; для публикации через HACS тот же файл подходит без изменений.
 */

const DEFAULT_ENTITY = "sensor.server_videonabliudeniia_poslednie_sobytiia";

// Через сколько перерисовывать подписи времени. События приходят редко,
// а подписи «2 мин назад» устаревают сами по себе — без этого списка
// пришлось бы ждать нового события, чтобы время обновилось.
const CLOCK_TICK_MS = 30000;

class OpenIpcNvrEventsCard extends HTMLElement {
  constructor() {
    super();
    this.attachShadow({ mode: "open" });
    this._events = [];
    this._timer = null;
  }

  setConfig(config) {
    this._config = {
      entity: config.entity || DEFAULT_ENTITY,
      camera: config.camera || "",
      columns: config.columns || 3,
      title: config.title || "События",
      hours: config.hours || 24,
      limit: config.limit || 24,
    };
    // Ширина плитки считается от числа столбцов: сетка должна заполнять
    // карточку целиком, а не оставлять пустое место справа.
    this._render();
  }

  set hass(hass) {
    this._hass = hass;
    const events = this._collect(hass);

    // Сравнение по содержимому, а не по ссылке: ассистент присылает новый
    // объект при каждом обновлении состояния, и перерисовка по ссылке
    // заставляла бы картинки мигать несколько раз в минуту.
    const next = JSON.stringify(events);
    if (next === this._lastJson) return;
    this._lastJson = next;
    this._events = events;
    this._render();
  }

  connectedCallback() {
    if (this._timer) return;
    // Подписи времени обновляются сами: иначе «только что» осталось бы
    // на экране и через час, потому что новое состояние не приходило.
    this._timer = setInterval(() => this._render(), CLOCK_TICK_MS);
  }

  disconnectedCallback() {
    clearInterval(this._timer);
    this._timer = null;
  }

  _collect(hass) {
    const state = hass.states[this._config.entity];
    if (!state) return [];

    const all = state.attributes.events || [];
    const since = Date.now() - this._config.hours * 3600 * 1000;

    return all
      .filter((ev) => {
        if (this._config.camera && ev.camera_id !== this._config.camera) {
          return false;
        }
        const ts = Date.parse(ev.timestamp || "");
        return !Number.isNaN(ts) && ts >= since;
      })
      .slice(0, this._config.limit);
  }

  _render() {
    if (!this._config) return;

    const cards = this._events.map((ev) => this._renderEvent(ev)).join("");
    const empty =
      this._events.length === 0
        ? `<div class="empty">За последние ${this._config.hours} ч событий не было</div>`
        : "";

    this.shadowRoot.innerHTML = `
      <style>
        ha-card { padding: 12px 16px 16px; }
        .head {
          display: flex; align-items: baseline; justify-content: space-between;
          margin-bottom: 10px;
        }
        .title { font-size: 18px; font-weight: 500; }
        .count { font-size: 13px; color: var(--secondary-text-color); }
        .grid {
          display: grid;
          grid-template-columns: repeat(${this._config.columns}, minmax(0, 1fr));
          gap: 10px;
        }
        .item {
          background: var(--secondary-background-color);
          border-radius: 10px; overflow: hidden; cursor: pointer;
        }
        .shot {
          display: block; width: 100%; aspect-ratio: 4 / 3;
          object-fit: cover; background: var(--disabled-color, #000);
        }
        .shot-missing {
          display: flex; align-items: center; justify-content: center;
          aspect-ratio: 4 / 3; color: var(--secondary-text-color); font-size: 12px;
        }
        .meta { padding: 6px 8px 8px; }
        .row { display: flex; justify-content: space-between; gap: 6px; }
        .class { font-size: 14px; font-weight: 500; }
        .when { font-size: 12px; color: var(--secondary-text-color); }
        .camera {
          font-size: 12px; color: var(--secondary-text-color);
          white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
          margin-top: 2px;
        }
        .empty {
          padding: 24px; text-align: center;
          color: var(--secondary-text-color); font-size: 14px;
        }
        @media (max-width: 600px) {
          .grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
        }
      </style>
      <ha-card>
        <div class="head">
          <div class="title">${this._escape(this._config.title)}</div>
          <div class="count">${this._events.length} за ${this._config.hours} ч</div>
        </div>
        <div class="grid">${cards}</div>
        ${empty}
      </ha-card>
    `;

    // Обработчики навешиваются после отрисовки: подставлять их прямо
    // в разметку значило бы собирать код из данных сервера.
    this.shadowRoot.querySelectorAll(".item").forEach((node) => {
      node.addEventListener("click", () => {
        const url = node.getAttribute("data-snapshot");
        if (url) window.open(url, "_blank");
      });
    });
  }

  _renderEvent(ev) {
    const snapshot = ev.snapshot || "";
    const image = snapshot
      ? `<img class="shot" src="${this._escape(snapshot)}" loading="lazy"
              alt="${this._escape(ev.class_label || "")}">`
      : `<div class="shot-missing">нет кадра</div>`;

    const label = ev.class_label || ev.object_class || "событие";
    // Если объект опознан по справочнику, показываем имя: «человек» без
    // имени не отвечает на главный вопрос — кто это был.
    const title =
      ev.matched_name && ev.match_type !== "unknown"
        ? `${label}: ${ev.matched_name}`
        : label;

    return `
      <div class="item" data-snapshot="${this._escape(snapshot)}">
        ${image}
        <div class="meta">
          <div class="row">
            <div class="class">${this._escape(title)}</div>
            <div class="when">${this._when(ev.timestamp)}</div>
          </div>
          <div class="camera">${this._escape(ev.camera_name || "")}</div>
        </div>
      </div>
    `;
  }

  _when(timestamp) {
    const ts = Date.parse(timestamp || "");
    if (Number.isNaN(ts)) return "";

    const seconds = Math.max(0, Math.round((Date.now() - ts) / 1000));
    if (seconds < 60) return "только что";

    const minutes = Math.round(seconds / 60);
    if (minutes < 60) return `${minutes} мин назад`;

    const hours = Math.round(minutes / 60);
    if (hours < 24) return `${hours} ч назад`;

    // Дальше суток подпись относительным не делаем: «2 дня назад» хуже
    // помогает найти событие, чем само время.
    return new Date(ts).toLocaleString("ru-RU", {
      day: "2-digit",
      month: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
    });
  }

  _escape(value) {
    return String(value == null ? "" : value).replace(
      /[&<>"']/g,
      (ch) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[ch],
    );
  }

  getCardSize() {
    return 5;
  }

  static getStubConfig() {
    return { title: "События камер", columns: 3, hours: 24 };
  }
}

customElements.define("openipc-nvr-events", OpenIpcNvrEventsCard);

// Описание для списка карточек: без него карточку можно добавить только
// вручную, правкой конфигурации панели.
window.customCards = window.customCards || [];
window.customCards.push({
  type: "openipc-nvr-events",
  name: "OpenIPC NVR — события",
  description: "Лента событий камер с кадрами",
  preview: false,
});

console.info("%c OPENIPC-NVR-EVENTS %c карточка загружена ", "color:#fff;background:#03a9f4", "color:#03a9f4;background:#fff");
