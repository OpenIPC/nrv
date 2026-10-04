import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { acsAPI, Z5RWorkmode } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { AlertTriangle, Cloud, CloudOff, RefreshCw, Server } from 'lucide-react'

/**
 * Панель режима работы контроллера Z5R WEB BT.
 *
 * Панель нужна из-за особенности этого контроллера: он умеет работать в
 * четырёх режимах, и из коробки настроен на облако производителя. В этом
 * режиме он отвечает по сети и выглядит исправным, но журнал проходов
 * уходит третьей стороне, а к нам не приходит вообще. Без отдельной панели
 * оператор видел бы «контроллер онлайн» и пустой журнал, а причину искать
 * было бы негде.
 *
 * Поэтому панель не просто показывает режим, а прямо говорит, смотрит ли
 * контроллер на наш сервер — это главный вопрос, на который она отвечает.
 */
export default function Z5RModePanel({ controllerID }: { controllerID: string }) {
  const { t } = useTranslation()
  const { data, loading, error, refetch } = useAsync<Z5RWorkmode>(
    () => acsAPI.workmode(controllerID),
  )

  const [busy, setBusy] = useState('')
  const [actionError, setActionError] = useState('')
  const [notice, setNotice] = useState('')

  const run = async (name: string, fn: () => Promise<any>, success: string) => {
    setBusy(name)
    setActionError('')
    setNotice('')
    try {
      await fn()
      setNotice(success)
      refetch()
    } catch (e: any) {
      setActionError(e?.response?.data?.error || t('z5rPanel.actionFailed'))
    } finally {
      setBusy('')
    }
  }

  if (loading) {
    return <div className="card" style={{ padding: 12 }}><div className="spinner" /></div>
  }

  // Ошибку чтения показываем отдельно: чаще всего она означает, что
  // контроллер недоступен, и это сама по себе полезная информация.
  if (error || !data) {
    return (
      <div className="card" style={{ padding: 12, borderLeft: '3px solid var(--danger)' }}>
        <div style={{ fontSize: 13, color: 'var(--danger)' }}>
          {t('z5rPanel.loadFailed')}
        </div>
        <button className="btn btn-outline btn-sm" onClick={refetch} style={{ marginTop: 8 }}>
          <RefreshCw size={14} />
          {t('z5rPanel.retry')}
        </button>
      </div>
    )
  }

  const ok = data.points_to_ours

  return (
    <div
      className="card"
      style={{
        padding: 12,
        borderLeft: `3px solid ${ok ? 'var(--success)' : 'var(--warning)'}`,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
        {ok ? <Server size={16} /> : <AlertTriangle size={16} color="var(--warning)" />}
        <strong style={{ fontSize: 14 }}>{t('z5rPanel.title')}</strong>
      </div>

      {/*
        Состояние показываем словами, а не только цветом: оператор должен
        понимать причину, а не догадываться по индикатору.
      */}
      {ok ? (
        <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 8 }}>
          <Trans
            i18nKey="z5rPanel.okText"
            values={{ mode: data.mode_name, url: data.server_url }}
            components={{ 1: <strong />, 2: <code /> }}
          />
        </div>
      ) : (
        <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 8 }}>
          <Trans
            i18nKey={data.server_url ? 'z5rPanel.badTextWithUrl' : 'z5rPanel.badTextNoUrl'}
            values={{ mode: data.mode_name, url: data.server_url }}
            components={{ 1: <strong />, 2: <code />, 3: <strong /> }}
          />
        </div>
      )}

      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        {!ok && (
          <button
            className="btn btn-primary btn-sm"
            disabled={busy !== ''}
            onClick={() => run('server', () => acsAPI.enableServerMode(controllerID),
              t('z5rPanel.savedMode'))}
          >
            <Server size={14} />
            {busy === 'server' ? t('z5rPanel.saving') : t('z5rPanel.toServer')}
          </button>
        )}

        {/*
          Отвязка от облака — отдельное действие, а не часть перевода.
          Она чистит только облачные настройки и не меняет режим: оператор
          может захотеть убрать отправку данных третьей стороне, не
          переключая контроллер на наш сервер.
        */}
        <button
          className="btn btn-outline btn-sm"
          disabled={busy !== ''}
          title={t('z5rPanel.unlinkHint')}
          onClick={() => run('unlink', () => acsAPI.unlinkCloud(controllerID),
            t('z5rPanel.unlinked'))}
        >
          <CloudOff size={14} />
          {busy === 'unlink' ? t('z5rPanel.unlinking') : t('z5rPanel.unlink')}
        </button>

        <button
          className="btn btn-outline btn-sm"
          disabled={busy !== ''}
          title={t('z5rPanel.restartHint')}
          onClick={() => run('restart', () => acsAPI.restartController(controllerID),
            t('z5rPanel.restarted'))}
        >
          <RefreshCw size={14} />
          {busy === 'restart' ? t('z5rPanel.restarting') : t('z5rPanel.restart')}
        </button>
      </div>

      {/*
        Напоминание про перезапуск показываем после сохранения настроек:
        контроллер применяет режим только после перезапуска, и без этого
        предупреждения оператор решил бы, что переключение не сработало.
      */}
      {notice && (
        <div style={{ fontSize: 12, color: 'var(--warning)', marginTop: 8 }}>
          <Cloud size={12} style={{ verticalAlign: -2, marginRight: 4 }} />
          {notice}
        </div>
      )}
      {actionError && (
        <div style={{ fontSize: 12, color: 'var(--danger)', marginTop: 8 }}>{actionError}</div>
      )}
    </div>
  )
}
