// Вкладка «Перенос» на странице настроек сервера.
//
// Нужна, когда сервер меняют: вместо того чтобы заводить заново камеры,
// СКУД, домофонию и учётные записи, настройки снимают в файл и применяют
// на новом сервере.
//
// Видеоархив и журналы в файл не попадают намеренно: на новом сервере
// записи ссылались бы на файлы, которых там нет, а события прошлого
// сервера ничего не объясняют про новый.
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  AlertTriangle, CheckCircle2, Download, FileJson, Info, Loader2,
  ShieldAlert, Upload, XCircle,
} from 'lucide-react'
import { transferAPI, type ConfigBundle, type ConfigImportReport } from '../api/client'
import { useToast } from '../context/ToastContext'

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

const tableStyle: React.CSSProperties = {
  width: '100%',
  borderCollapse: 'collapse',
  fontSize: 13,
  marginTop: 12,
}

const cellStyle: React.CSSProperties = {
  padding: '6px 8px',
  borderBottom: '1px solid var(--border, #2a2d35)',
  textAlign: 'left',
}

const numberCellStyle: React.CSSProperties = {
  ...cellStyle,
  textAlign: 'right',
  fontVariantNumeric: 'tabular-nums',
}

export default function ConfigTransferPanel() {
  const { t } = useTranslation()
  const { success, error: toastError } = useToast()

  const [exporting, setExporting] = useState(false)
  const [busy, setBusy] = useState(false)
  const [fileName, setFileName] = useState('')
  // Текст файла храним как есть и отправляем на сервер без изменений:
  // лишнее преобразование — лишняя причина расхождений при разборе отказа.
  const [fileText, setFileText] = useState('')
  const [bundle, setBundle] = useState<ConfigBundle | null>(null)
  const [report, setReport] = useState<ConfigImportReport | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)

  const download = async () => {
    setExporting(true)
    try {
      const res = await transferAPI.exportFile()
      const url = URL.createObjectURL(res.data)
      const link = document.createElement('a')
      link.href = url
      link.download = `nvr-config-${new Date().toISOString().slice(0, 10)}.json`
      link.click()
      // Ссылку освобождаем не сразу: браузер начинает сохранение файла
      // асинхронно, и мгновенный отзыв может оборвать его на середине.
      window.setTimeout(() => URL.revokeObjectURL(url), 2000)
      success(t('serverPage.transferDownloaded'))
    } catch {
      toastError(t('serverPage.transferDownloadFailed'))
    } finally {
      setExporting(false)
    }
  }

  const pickFile = async (file: File) => {
    const text = await file.text()
    let parsed: ConfigBundle
    try {
      parsed = JSON.parse(text) as ConfigBundle
    } catch {
      toastError(t('serverPage.transferBadFormat'))
      return
    }
    // Формат проверяем до запроса: так человек сразу видит, что выбрал не
    // тот файл, а не читает отказ сервера.
    if (parsed?.format !== 'nvr-config') {
      toastError(t('serverPage.transferBadFormat'))
      return
    }
    setBundle(parsed)
    setFileText(text)
    setFileName(file.name)
    setReport(null)
  }

  const run = async (preview: boolean) => {
    if (!preview && !window.confirm(t('serverPage.transferConfirm'))) return
    setBusy(true)
    try {
      const res = await transferAPI.importFile(fileText, preview)
      setReport(res.data)
      if (preview) success(t('serverPage.transferChecked'))
      else success(t('serverPage.transferApplied'))
    } catch (err: unknown) {
      // Текст отказа приходит с сервера: он объясняет причину словами
      // (какая таблица и что именно не сошлось), и подменять его общим
      // «не удалось» значит отнять у человека единственную зацепку.
      const message =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        t('serverPage.transferFailed')
      toastError(message)
    } finally {
      setBusy(false)
    }
  }

  const reset = () => {
    setBundle(null)
    setFileText('')
    setFileName('')
    setReport(null)
    if (inputRef.current) inputRef.current.value = ''
  }

  const totals = bundle
    ? Object.values(bundle.counts ?? {}).reduce((sum, n) => sum + n, 0)
    : 0
  const sections = bundle
    ? Object.values(bundle.counts ?? {}).filter((n) => n > 0).length
    : 0

  return (
    <div>
      <div style={cardStyle}>
        <h2 style={{ margin: '0 0 8px', fontSize: 17, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Download size={18} />
          {t('serverPage.transferExportTitle')}
        </h2>
        <div style={{ fontSize: 14, lineHeight: 1.6 }}>
          {t('serverPage.transferIntro')}
        </div>
        <div style={hintStyle}>{t('serverPage.transferExcludes')}</div>

        <div style={{ ...warningStyle, marginTop: 14, marginBottom: 0 }}>
          <ShieldAlert size={18} style={{ flexShrink: 0, marginTop: 1 }} />
          <div>{t('serverPage.transferSecrets')}</div>
        </div>

        <button
          onClick={download}
          disabled={exporting}
          style={{ ...primaryButtonStyle, marginTop: 14, opacity: exporting ? 0.6 : 1 }}
        >
          {exporting ? <Loader2 size={15} className="spin" /> : <Download size={15} />}
          {t('serverPage.transferDownload')}
        </button>
      </div>

      <div style={cardStyle}>
        <h2 style={{ margin: '0 0 8px', fontSize: 17, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Upload size={18} />
          {t('serverPage.transferImportTitle')}
        </h2>
        <div style={{ fontSize: 14, lineHeight: 1.6 }}>{t('serverPage.transferImportHint')}</div>
        <div style={hintStyle}>{t('serverPage.transferExistingNote')}</div>

        <input
          ref={inputRef}
          type="file"
          accept="application/json,.json"
          style={{ display: 'none' }}
          onChange={(e) => {
            const file = e.target.files?.[0]
            if (file) void pickFile(file)
          }}
        />

        <div style={{ display: 'flex', gap: 8, marginTop: 14, flexWrap: 'wrap' }}>
          <button onClick={() => inputRef.current?.click()} style={buttonStyle} disabled={busy}>
            <FileJson size={15} />
            {t('serverPage.transferChoose')}
          </button>

          {bundle && (
            <>
              <button onClick={() => void run(true)} style={buttonStyle} disabled={busy}>
                {busy ? <Loader2 size={15} className="spin" /> : <Info size={15} />}
                {t('serverPage.transferCheck')}
              </button>
              <button onClick={() => void run(false)} style={primaryButtonStyle} disabled={busy}>
                {busy ? <Loader2 size={15} className="spin" /> : <Upload size={15} />}
                {t('serverPage.transferApply')}
              </button>
              <button onClick={reset} style={buttonStyle} disabled={busy}>
                {t('serverPage.transferReset')}
              </button>
            </>
          )}
        </div>

        {bundle && (
          <div style={{ marginTop: 12, fontSize: 13 }}>
            <strong>{fileName}</strong>
            <div style={hintStyle}>
              {t('serverPage.transferFileInfo', {
                sections,
                rows: totals,
                date: bundle.exported_at ? new Date(bundle.exported_at).toLocaleString() : '—',
              })}
            </div>
          </div>
        )}

        {report && (
          <div style={{ marginTop: 16 }}>
            <div
              style={{
                display: 'flex', alignItems: 'center', gap: 8,
                color: report.preview ? 'var(--warning, #ff9f0a)' : 'var(--success, #34c759)',
                fontSize: 14, fontWeight: 500,
              }}
            >
              {report.preview ? <AlertTriangle size={16} /> : <CheckCircle2 size={16} />}
              {report.preview ? t('serverPage.transferPreviewReport') : t('serverPage.transferReport')}
            </div>

            <div style={{ ...hintStyle, marginTop: 4 }}>
              {t('serverPage.transferTotals', {
                added: report.added,
                updated: report.updated,
                replaced: report.replaced,
              })}
            </div>

            <table style={tableStyle}>
              <thead>
                <tr>
                  <th style={cellStyle}>{t('serverPage.transferTable')}</th>
                  <th style={numberCellStyle}>{t('serverPage.transferInFile')}</th>
                  <th style={numberCellStyle}>{t('serverPage.transferAdded')}</th>
                  <th style={numberCellStyle}>{t('serverPage.transferUpdated')}</th>
                  <th style={numberCellStyle}>{t('serverPage.transferReplaced')}</th>
                </tr>
              </thead>
              <tbody>
                {report.tables.map((row) => (
                  <tr key={row.name}>
                    <td style={cellStyle}>{row.name}</td>
                    <td style={numberCellStyle}>{row.in_file}</td>
                    <td style={numberCellStyle}>{row.added}</td>
                    <td style={numberCellStyle}>{row.updated}</td>
                    <td style={numberCellStyle}>{row.replaced}</td>
                  </tr>
                ))}
              </tbody>
            </table>

            {report.warnings?.length ? (
              <div style={{ ...warningStyle, marginTop: 14, marginBottom: 0 }}>
                <XCircle size={18} style={{ flexShrink: 0, marginTop: 1 }} />
                <div>
                  {report.warnings.map((w) => (
                    <div key={w}>{w}</div>
                  ))}
                </div>
              </div>
            ) : null}

            {/* Учётные записи переносятся по своим идентификаторам: если
                вход был под перенесённой записью, текущий токен может
                перестать находить её. Это не сбой переноса, и человек
                должен знать об этом заранее, а не удивляться выходу. */}
            {!report.preview && (
              <div style={{ ...hintStyle, marginTop: 12 }}>{t('serverPage.transferReloginNote')}</div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
