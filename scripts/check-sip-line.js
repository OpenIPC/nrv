/**
 * Проверка регистрации линии на Asterisk без телефона.
 *
 * Нужна потому, что собрать APK и запустить его на устройстве можно не
 * всегда, а убедиться, что выданные серверу реквизиты действительно
 * работают, нужно до того, как оператор возьмёт телефон. Здесь повторяется
 * ровно тот путь, которым идёт приложение: SIP over WebSocket на порт 8088
 * Asterisk, регистрация и короткий исходящий вызов.
 *
 * Запуск: node scripts/check-sip-line.js 301 ПАРОЛЬ [кому_звонить]
 */

const { UA, WebSocketInterface } = require('../mobile/node_modules/jssip');

// В Node 20 глобального WebSocket ещё нет, а JsSIP берёт его именно оттуда:
// подставляем реализацию из пакета ws, который уже стоит в приложении.
globalThis.WebSocket = require('../mobile/node_modules/ws');

// JsSIP ищет WebRTC через `window.RTCPeerConnection`. В приложении это
// закрывает `src/net/sipPolyfills.ts`, здесь хватает псевдонима глобальной
// области: полноценного WebRTC в Node нет, поэтому звонок проверяется только
// по сигнализации (ушёл ли INVITE и что ответила станция).
globalThis.window = globalThis;

const number = process.argv[2] || '301';
const password = process.argv[3];
const target = process.argv[4];
const server = process.argv[5] || '192.168.1.111';
const wsUrl = `ws://${server}:8088/ws`;

if (!password) {
  console.error('Укажите пароль линии: node check-sip-line.js 301 ПАРОЛЬ [номер]');
  process.exit(2);
}

const socket = new WebSocketInterface(wsUrl);
const ua = new UA({
  sockets: [socket],
  uri: `sip:${number}@${server}`,
  password,
  display_name: 'Проверка линии',
  register: true,
  register_expires: 300,
  user_agent: 'NVR Check',
  // Контакт и адрес Via задаём явно. Без них JsSIP подставляет случайный
  // хост вида «xxxx.invalid» (в браузере он берёт свой из адресной строки,
  // а здесь браузера нет), и Asterisk не может ответить на такой контакт —
  // регистрация молча висит.
  contact_uri: `sip:${number}@${server};transport=ws`,
  via_host: server,
});

let done = false;
const finish = (code) => {
  if (done) return;
  done = true;
  try {
    ua.stop();
  } catch {
    /* уже остановлен */
  }
  process.exit(code);
};

ua.on('registered', () => {
  console.log(`РЕГИСТРАЦИЯ: линия ${number} на связи (${wsUrl})`);
  if (!target) {
    finish(0);
    return;
  }
  // Звонок в Node возможен только с настоящим WebRTC-стеком, а его здесь
  // нет: JsSIP отказывается начинать сессию без RTCPeerConnection. Это не
  // мешает главной проверке — регистрации линии.
  if (!globalThis.RTCPeerConnection) {
    console.error(
      'ЗВОНОК: пропущен — в Node нет WebRTC. Регистрация проверена; вызов проверяйте с телефона в приложении.',
    );
    finish(0);
    return;
  }
  console.log(`ЗВОНОК: ${target}`);
  try {
    ua.call(`sip:${target}@${server}`, { mediaConstraints: { audio: true, video: false } });
  } catch (error) {
    console.error('ЗВОНОК: ошибка отправки —', error.message);
    finish(1);
  }
});

ua.on('registrationFailed', (event) => {
  console.error(
    'ОТКАЗ:',
    event.cause,
    event.response ? `код ${event.response.status_code} ${event.response.reason_phrase}` : '',
  );
  finish(1);
});

ua.on('newRTCSession', ({ session }) => {
  session.on('progress', () => console.log('ЗВОНОК: идёт вызов (100/180)'));
  session.on('accepted', () => console.log('ЗВОНОК: ответили, разговор состоялся'));
  session.on('confirmed', () => {
    console.log('ЗВОНОК: соединение подтверждено');
    setTimeout(() => session.terminate(), 1500);
  });
  session.on('failed', (event) => {
    console.error('ЗВОНОК: не состоялся —', event.cause);
    setTimeout(() => finish(1), 500);
  });
  session.on('ended', () => {
    console.log('ЗВОНОК: завершён');
    finish(0);
  });
});

// Без этого шага UA только создан, но не подключается: JsSIP ждёт
// явной команды, и регистрация не начинается.
ua.start();

// Страховка от вечного ожидания: если станция молчит, скрипт должен
// закончиться сам, а не висеть в терминале.
setTimeout(() => {
  console.error('ТАЙМАУТ: ответа от Asterisk нет');
  finish(1);
}, 20000);
