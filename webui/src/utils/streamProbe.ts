import type { StreamProbeResult } from '../api/client'

/**
 * Подпись к результату проверки RTSP-потока.
 *
 * Проверка одна и та же, а показывается в двух местах — в сканере сети и при
 * редактировании камеры. Поэтому разбор кодов живёт здесь, а не в разметке
 * каждой из страниц: иначе подписи разъедутся.
 */

/** Ключ перевода и подстановки к нему. */
export interface ProbeText {
  key: string
  params?: Record<string, string>
}
/**
 * Коды без подстановок. Отдельным словарём, потому что подстановок у них
 * нет и городить для них разбор в switch незачем.
 */
const PROBE_KEYS: Record<string, string> = {
  url_empty: 'streamProbe.urlEmpty',
  timeout: 'streamProbe.timeout',
  auth_failed: 'streamProbe.authFailed',
  path_not_found: 'streamProbe.pathNotFound',
  unreachable: 'streamProbe.unreachable',
  bad_response: 'streamProbe.badResponse',
  no_video: 'streamProbe.noVideo',
  request_failed: 'streamProbe.requestFailed',
}

/**
 * Возвращает ключ перевода для результата проверки.
 *
 * Отдаётся ключ, а не готовая строка: файл ничего не знает о языке
 * интерфейса. Незнакомый код показываем как есть — так новый код с сервера
 * не превратится в пустое место, пока его не перевели.
 */
export function streamProbeText(p: StreamProbeResult): ProbeText {
  if (p.code === 'ok') {
    // Две отдельные фразы вместо склейки «видео» + «звук»: в китайском
    // запятая другая, а порядок «звук после видео» не универсален.
    return {
      key: p.has_audio ? 'streamProbe.ok' : 'streamProbe.okSilent',
      params: {
        codec: p.codec ?? '',
        width: String(p.width ?? 0),
        height: String(p.height ?? 0),
        audio: p.audio_codec ?? '',
      },
    }
  }

  if (p.code === 'failed') {
    // Сырой текст ffprobe приходит готовым: разбирать формулировки
    // конкретной сборки ffprobe бессмысленно, они меняются от версии.
    return { key: 'streamProbe.failed', params: { detail: p.detail ?? '' } }
  }

  const key = PROBE_KEYS[p.code]
  return key ? { key } : { key: 'streamProbe.other', params: { code: p.code } }
}
