// Вкладка «Обновления» на странице настроек сервера.
//
// Установка идёт на хосте: скачивается код, собираются образы, контейнеры
// перезапускаются. Из-за перезапуска страница на время теряет связь с
// сервером, поэтому ход работ виден по журналу, сохранённому на хосте, —
// после перезагрузки вкладки его можно дочитать.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  CheckCircle2, Download, History, Info, Loader2, RefreshCw, RotateCcw,
  AlertTriangle, XCircle, GitBranch, KeyRound, Save,
} from 'lucide-react'
import { useToast } from '../context/ToastContext'
import { settingsAPI } from '../api/client'
import {
  updatesAPI, type CheckResponse, type StatusResponse, type UpdateState,
} from '../api/updates'

/**
 * Площадки, встречающиеся чаще всего.
 *
 * Это только подсказки-кнопки: адрес всё равно можно ввести руками,
 * потому что установки живут и на своих Git-серверах. В код адреса не
 * вшиты — они подставляются в поле, которое оператор правит.
 */
const REPO_PRESETS = [
  { key: 'github', url: 'https://github.com/OpenIPC/nrv' },
  { key: 'gitverse', url: 'https://gitverse.ru/himik19872/OpenIPC-NRV' },
]

const cardStyle: React.CSSProperties = {
  background: 'var(--card-bg, #1a1d23)',
  border: '1px solid var(--border, #2a2d35)',
  borderRadius: 12,
  padding: 20,
  marginBottom: 16,
}

const hintStyle: React.CSSProperties = {
  fontSize: 12,
  color: 'var(--text-secondary, #9aa0aa)',
  marginTop: 6,
  lineHeight: 1.5,
}

const buttonStyle: React.CSSProperties = {
  display: 'inline-flex',
  alignItems: 'center',
  gap: 8,
  padding: '9px 16px',
  borderRadius: 8,
  border: '1px solid var(--border, #2a2d35)',
  background: 'var(--input-bg, #12141a)',
  color: 'var(--text, #e6e8eb)',
  fontSize: 14,
  fontWeight: 500,
  cursor: 'pointer',
}

const primaryButtonStyle: React.CSSProperties = {
  ...buttonStyle,
  background: '#2563eb',
  border: '1px solid #2563eb',
  color: '#fff',
}

const warningStyle: React.CSSProperties = {
  display: 'flex',
  gap: 10,
  alignItems: 'flex-start',
  padding: 14,
  borderRadius: 10,
  background: 'rgba(245, 158, 11, 0.12)',
  border: '1px solid rgba(245, 158, 11, 0.35)',
  color: '#f5b545',
  fontSize: 13,
  lineHeight: 1.6,
  marginBottom: 16,
}

const logStyle: React.CSSProperties = {
  marginTop: 12,
  padding: 12,
  borderRadius: 8,
  background: '#0d0f13',
  border: '1px solid var(--border, #2a2d35)',
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
  fontSize: 12,
  lineHeight: 1.6,
  color: '#c9ced6',
  maxHeight: 280,
  overflowY: 'auto',
  whiteSpace: 'pre-wrap',
}

/** Дата коммита в читаемом виде. */
function formatDate(value: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

export default function UpdatesPanel() {
  const { t } = useTranslation()
  const { success, error: toastError } = useToast()

  const [check, setCheck] = useState<CheckResponse | null>(null)
  const [status, setStatus] = useState<StatusResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [checking, setChecking] = useState(false)
  const [installing, setInstalling] = useState(false)

  // Поля источника обновлений: держим отдельно от ответа сервера, чтобы
  // оператор мог править адрес и ветку до сохранения.
  const [repoDraft, setRepoDraft] = useState('')
  const [branchDraft, setBranchDraft] = useState('')
  const [tokenDraft, setTokenDraft] = useState('')
  const [tokenSet, setTokenSet] = useState(false)
  const [savingSource, setSavingSource] = useState(false)

  const state: UpdateState | undefined = status?.state
  const running = state?.state === 'running'

  const load = useCallback(async () => {
    try {
      const res = await updatesAPI.check()
      setCheck(res.data)
      setRepoDraft(res.data.repo ?? '')
      setBranchDraft(res.data.branch ?? 'main')
      setTokenSet(Boolean(res.data.token_set))
    } catch {
      toastError(t('updates.checkFailed'))
    } finally {
      setLoading(false)
    }
  }, [t, toastError])

  const loadSource = useCallback(async () => {
    try {
      const res = await settingsAPI.get()
      const src = res.data.updates
      if (src) {
        setRepoDraft(src.repo_url ?? '')
        setBranchDraft(src.branch ?? 'main')
        setTokenSet(Boolean(src.token_set))
      }
    } catch {
      // Настройки недоступны — покажем то, что пришло с проверкой
      // обновлений: адрес и ветку она тоже возвращает.
    }
  }, [])

  // Сохраняет адрес, ветку и токен, затем сразу перепроверяет обновления:
  // оператор меняет источник именно за тем, чтобы увидеть новую версию.
  const saveSource = async (token?: string) => {
    setSavingSource(true)
    try {
      const res = await settingsAPI.updateSource({
        repo_url: repoDraft.trim(),
        branch: branchDraft.trim() || 'main',
        token,
      })
      const src = res.data.updates
      setTokenSet(Boolean(src?.token_set))
      setTokenDraft('')
      success(t('updates.sourceSaved'))
      await load()
    } catch {
      toastError(t('updates.sourceSaveFailed'))
    } finally {
      setSavingSource(false)
    }
  }

  const loadStatus = useCallback(async () => {
    try {
      const res = await updatesAPI.status()
      setStatus(res.data)
      if (res.data.state?.state === 'running') setInstalling(true)
      if (res.data.state?.state === 'done') setInstalling(false)
      if (res.data.state?.state === 'failed') setInstalling(false)
    } catch {
      // Обрыв связи во время установки — ожидаемое: контейнеры
      // перезапускаются. Показывать ошибку в этом случае незачем.
    }
  }, [])

  useEffect(() => {
    loadStatus()
    loadSource().then(load)
  }, [loadStatus, loadSource, load])

  // Пока установка идёт, состояние и журнал обновляем сами: страница
  // на время теряет связь, и без опроса оператор видел бы застывший ход.
  useEffect(() => {
    if (!running && !installing) return
    const timer = setInterval(loadStatus, 3000)
    return () => clearInterval(timer)
  }, [running, installing, loadStatus])

  // Ежедневная автоматическая проверка: оператор должен узнавать о новой
  // версии сам, а не только когда откроет вкладку. Отметку держим в
  // браузере, потому что проверка нужна не серверу, а человеку.
  useEffect(() => {
    const KEY = 'nvr-update-checked-at'
    const last = Number(localStorage.getItem(KEY) ?? 0)
    const dayMs = 24 * 60 * 60 * 1000
    if (Date.now() - last < dayMs) return
    localStorage.setItem(KEY, String(Date.now()))
    updatesAPI.check().catch(() => {
      // Тихая проверка: сеть может быть недоступна, и мешать оператору
      // сообщением об этом не нужно — он увидит результат при возврате.
    })
  }, [])

  const runCheck = async () => {
    setChecking(true)
    try {
      const res = await updatesAPI.check()
      setCheck(res.data)
      if (res.data.error) {
        toastError(res.data.error)
      } else if (res.data.check?.behind) {
        success(t('updates.foundNew'))
      } else {
        success(t('updates.upToDate'))
      }
    } catch {
      toastError(t('updates.checkFailed'))
    } finally {
      setChecking(false)
    }
  }

  const apply = async () => {
    // Обновление прерывает работающий просмотр и перезапускает контейнеры:
    // без явного согласия запускать такое нельзя.
    const confirmed = window.confirm(t('updates.confirmInstall'))
    if (!confirmed) return

    try {
      await updatesAPI.apply()
      setInstalling(true)
      success(t('updates.installStarted'))
      loadStatus()
    } catch (err) {
      const message = (err as { response?: { data?: { error?: string } } })
        ?.response?.data?.error
      toastError(message || t('updates.installFailed'))
    }
  }

  const rollback = async () => {
    const confirmed = window.confirm(t('updates.confirmRollback'))
    if (!confirmed) return

    try {
      await updatesAPI.rollback()
      setInstalling(true)
      success(t('updates.installStarted'))
      loadStatus()
    } catch (err) {
      const message = (err as { response?: { data?: { error?: string } } })
        ?.response?.data?.error
      toastError(message || t('updates.installFailed'))
    }
  }

  const version = check?.check
  const behind = version?.behind ?? false

  return (
    <div>
      <div style={cardStyle}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <GitBranch size={20} />
          <h2 style={{ margin: 0, fontSize: 17 }}>{t('updates.sourceTitle')}</h2>
        </div>

        <div style={{ marginBottom: 8 }}>
          <label style={{ display: 'block', fontSize: 13, fontWeight: 500, marginBottom: 6, color: 'var(--text-secondary, #9aa0aa)' }}>
            {t('updates.repoLabel')}
          </label>
          <input
            value={repoDraft}
            onChange={(e) => setRepoDraft(e.target.value)}
            placeholder="https://github.com/OpenIPC/nrv"
            style={{
              width: '100%', padding: '9px 12px', borderRadius: 8,
              border: '1px solid var(--border, #2a2d35)',
              background: 'var(--input-bg, #12141a)', color: 'var(--text, #e6e8eb)',
              fontSize: 14, boxSizing: 'border-box',
            }}
          />
          <div style={hintStyle}>{t('updates.repoHint')}</div>
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 8 }}>
            {REPO_PRESETS.map((preset) => (
              <button
                key={preset.key}
                onClick={() => setRepoDraft(preset.url)}
                style={{ ...buttonStyle, fontSize: 13, padding: '6px 12px' }}
              >
                {preset.url}
              </button>
            ))}
          </div>
        </div>

        <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', marginBottom: 8 }}>
          <div style={{ flex: '1 1 180px' }}>
            <label style={{ display: 'block', fontSize: 13, fontWeight: 500, marginBottom: 6, color: 'var(--text-secondary, #9aa0aa)' }}>
              {t('updates.branchLabel')}
            </label>
            <input
              value={branchDraft}
              onChange={(e) => setBranchDraft(e.target.value)}
              placeholder="main"
              style={{
                width: '100%', padding: '9px 12px', borderRadius: 8,
                border: '1px solid var(--border, #2a2d35)',
                background: 'var(--input-bg, #12141a)', color: 'var(--text, #e6e8eb)',
                fontSize: 14, boxSizing: 'border-box',
              }}
            />
          </div>
          <div style={{ flex: '2 1 320px' }}>
            <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, fontWeight: 500, marginBottom: 6, color: 'var(--text-secondary, #9aa0aa)' }}>
              <KeyRound size={13} />
              {t('updates.tokenLabel')}
              {tokenSet && (
                <span style={{ color: '#22c55e', fontWeight: 400 }}>
                  · {t('updates.tokenSet')}
                </span>
              )}
            </label>
            <input
              type="password"
              value={tokenDraft}
              onChange={(e) => setTokenDraft(e.target.value)}
              placeholder={tokenSet ? '••••••••' : t('updates.tokenPlaceholder')}
              autoComplete="off"
              style={{
                width: '100%', padding: '9px 12px', borderRadius: 8,
                border: '1px solid var(--border, #2a2d35)',
                background: 'var(--input-bg, #12141a)', color: 'var(--text, #e6e8eb)',
                fontSize: 14, boxSizing: 'border-box',
              }}
            />
            <div style={hintStyle}>{t('updates.tokenHint')}</div>
          </div>
        </div>

        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', marginTop: 10 }}>
          <button
            style={primaryButtonStyle}
            onClick={() => saveSource(tokenDraft.trim() ? tokenDraft.trim() : undefined)}
            disabled={savingSource}
          >
            {savingSource ? <Loader2 size={16} className="spin" /> : <Save size={16} />}
            {t('updates.saveSource')}
          </button>
          {tokenSet && (
            // Токен не показывается, поэтому и убрать его можно только
            // явной кнопкой: пустое поле означает «не менять токен».
            <button
              style={buttonStyle}
              onClick={() => saveSource('')}
              disabled={savingSource}
            >
              <XCircle size={16} />
              {t('updates.clearToken')}
            </button>
          )}
        </div>
      </div>

      <div style={cardStyle}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <History size={20} />
          <h2 style={{ margin: 0, fontSize: 17 }}>{t('updates.versionTitle')}</h2>
        </div>

        {loading ? (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <Loader2 size={18} className="spin" />
            <span>{t('common.loading')}</span>
          </div>
        ) : check?.error ? (
          <div style={warningStyle}>
            <AlertTriangle size={18} style={{ flexShrink: 0, marginTop: 2 }} />
            <div>{check.error}</div>
          </div>
        ) : (
          <div style={{ display: 'grid', gap: 6, fontSize: 14 }}>
            <div>
              <strong>{t('updates.currentVersion')}:</strong>{' '}
              <code>{version?.current.short}</code>
              {version?.current.date ? ` · ${formatDate(version.current.date)}` : ''}
            </div>
            <div style={{ color: 'var(--text-secondary, #9aa0aa)' }}>
              {version?.current.subject}
            </div>
            <div style={{ marginTop: 6, fontSize: 13 }}>
              <strong>{t('updates.repository')}:</strong>{' '}
              {check?.repo || version?.dir || '—'}
              {' · '}
              {t('updates.branch')}: <code>{check?.branch ?? 'main'}</code>
            </div>
            <div style={hintStyle}>
              {t('updates.installDir')}: <code>{version?.dir}</code>
            </div>
            {version?.dirty && (
              <div style={{ ...hintStyle, color: '#f5b545' }}>
                {t('updates.dirtyWarning')}
              </div>
            )}
          </div>
        )}
      </div>

      <div style={cardStyle}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <Download size={20} />
          <h2 style={{ margin: 0, fontSize: 17 }}>{t('updates.checkTitle')}</h2>
        </div>

        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
          <button
            style={buttonStyle}
            onClick={runCheck}
            disabled={checking || running}
          >
            {checking ? <Loader2 size={16} className="spin" /> : <RefreshCw size={16} />}
            {checking ? t('updates.checking') : t('updates.checkButton')}
          </button>

          {behind && (
            <button
              style={primaryButtonStyle}
              onClick={apply}
              disabled={running || installing || version?.enough_space === false}
            >
              {running || installing
                ? <Loader2 size={16} className="spin" />
                : <Download size={16} />}
              {running ? t('updates.installing') : t('updates.installButton')}
            </button>
          )}

          {version?.previous?.sha && (
            <button style={buttonStyle} onClick={rollback} disabled={running || installing}>
              <RotateCcw size={16} />
              {t('updates.rollbackButton')}
            </button>
          )}
        </div>

        {version && !version.enough_space && (
          <div style={{ ...warningStyle, marginTop: 14, marginBottom: 0 }}>
            <AlertTriangle size={18} style={{ flexShrink: 0, marginTop: 2 }} />
            <div>
              {t('updates.lowSpace', { free: version.free_gb })}
            </div>
          </div>
        )}

        {version && !version.compose && (
          <div style={{ ...warningStyle, marginTop: 14, marginBottom: 0 }}>
            <AlertTriangle size={18} style={{ flexShrink: 0, marginTop: 2 }} />
            <div>{t('updates.noCompose')}</div>
          </div>
        )}

        {version?.behind && (
          <>
            <div style={{ ...warningStyle, marginTop: 16, marginBottom: 0 }}>
              <AlertTriangle size={18} style={{ flexShrink: 0, marginTop: 2 }} />
              <div>{t('updates.installWarning')}</div>
            </div>
            <div style={{ marginTop: 16, fontSize: 14 }}>
              <strong>{t('updates.changesTitle')}</strong>
              {version.diff && (
                <span style={{ color: 'var(--text-secondary, #9aa0aa)' }}>
                  {' — '}{version.diff}
                </span>
              )}
            </div>
            <div style={logStyle}>
              {version.commits.length > 0
                ? version.commits.join('\n')
                : t('updates.noChanges')}
            </div>
          </>
        )}

        {version && !version.behind && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 14, color: '#22c55e' }}>
            <CheckCircle2 size={18} />
            {t('updates.upToDate')}
          </div>
        )}
      </div>

      {(installing || running || state?.state === 'failed' || (status?.log?.length ?? 0) > 0) && (
        <div style={cardStyle}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
            {state?.state === 'failed' ? <XCircle size={20} color="#ef4444" /> : <Info size={20} />}
            <h2 style={{ margin: 0, fontSize: 17 }}>{t('updates.logTitle')}</h2>
            {state?.step && (
              <span style={{ fontSize: 13, color: 'var(--text-secondary, #9aa0aa)' }}>
                {state.step}
              </span>
            )}
          </div>

          {state?.error && (
            <div style={{ color: '#ef4444', fontSize: 13, marginBottom: 8 }}>{state.error}</div>
          )}

          <div style={logStyle}>
            {(status?.log ?? []).join('\n') || t('updates.logEmpty')}
          </div>

          <div style={hintStyle}>{t('updates.logHint')}</div>
        </div>
      )}
    </div>
  )
}
