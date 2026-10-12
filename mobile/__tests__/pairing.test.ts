import { parsePairingString } from '../src/net/pairing';

/**
 * Разбор строки подключения из QR-кода.
 *
 * Проверяем в первую очередь чужие коды: человек наводит телефон на первый
 * попавшийся квадратик (ссылка на сайт, вайфай, билет), и приложение должно
 * сказать «это не наш код», а не подключиться не туда.
 */
describe('parsePairingString', () => {
  const valid =
    'nvr://pair?v=1&url=https%3A%2F%2Fhimik1987.duckdns.org&code=N3XD-FX8R&user=katya';

  it('разбирает код из панели', () => {
    expect(parsePairingString(valid)).toEqual({
      baseUrl: 'https://himik1987.duckdns.org',
      code: 'N3XD-FX8R',
      username: 'katya',
    });
  });

  it('принимает адрес без схемы: панель может открываться и по IP', () => {
    const raw = 'nvr://pair?v=1&url=192.168.1.111&code=ABCD-2345&user=admin';
    expect(parsePairingString(raw)).toEqual({
      baseUrl: 'http://192.168.1.111:8080',
      code: 'ABCD-2345',
      username: 'admin',
    });
  });

  it('отклоняет чужие ссылки', () => {
    expect(parsePairingString('https://example.com')).toBeNull();
    expect(parsePairingString('WIFI:S:home;T:WPA;P:secret;;')).toBeNull();
    expect(parsePairingString('')).toBeNull();
  });

  it('отклоняет код без обязательных полей', () => {
    expect(parsePairingString('nvr://pair?v=1&url=https%3A%2F%2Fnvr.local')).toBeNull();
    expect(parsePairingString('nvr://pair?v=1&code=ABCD-2345')).toBeNull();
  });

  it('отклоняет код другой версии формата', () => {
    const raw = 'nvr://pair?v=2&url=https%3A%2F%2Fnvr.local&code=ABCD-2345';
    expect(parsePairingString(raw)).toBeNull();
  });

  it('не спотыкается о лишние параметры', () => {
    const raw =
      'nvr://pair?url=https%3A%2F%2Fnvr.local&code=ABCD-2345&user=ivan&v=1&extra=1';
    expect(parsePairingString(raw)?.code).toBe('ABCD-2345');
  });
});
