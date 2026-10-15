import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  accessAPI,
  cameraGroupsAPI,
  usersAPI,
  type AccessPreview,
  type CameraGroup,
  type CatalogCamera,
  type CatalogController,
  type UserAccess,
  type UserAccount,
} from '../api/client'
import { useToast } from '../context/ToastContext'

/**
 * Выдача прав на камеры: какие камеры видит эта учётная запись.
 *
 * Отдельное окно, а не ещё один блок в карточке пользователя, потому что
 * документ здесь другой: карточка отвечает на «что человек может делать»
 * (права разделов), а это окно — на «что он видит» (объекты). Смешивать их
 * в одной форме значило бы сохранять одним нажатием две несвязанные вещи.
 *
 * Главное правило на экране: пусто — значит не видно НИЧЕГО. Так новый
 * сотрудник после создания не получает доступ ко всему парку случайно, а
 * «видно всё» ставится отдельным флажком осознанно.
 */
interface Props {
  user: UserAccount
  onClose: () => void
  /** Вызывается после сохранения: список пользователей перечитывает данные. */
  onSaved?: () => void
}

const emptyAccess: UserAccess = {
  all_cameras: false,
  camera_ids: [],
  group_ids: [],
  all_acs_controllers: false,
  controller_ids: [],
}

export default function UserAccessEditor({ user, onClose, onSaved }: Props) {
  const { t } = useTranslation()
  const toast = useToast()

  const [cameras, setCameras] = useState<CatalogCamera[]>([])
  const [groups, setGroups] = useState<CameraGroup[]>([])
  const [controllers, setControllers] = useState<CatalogController[]>([])
  const [access, setAccess] = useState<UserAccess>(emptyAccess)
  // Куда этой учётной записи можно входить. Живёт здесь, а не только в
  // карточке пользователя: администратор выдаёт доступ в одном окне и не
  // должен гадать, где искать вторую половину настроек.
  const [webAccess, setWebAccess] = useState(user.web_access !== false)
  const [appAccess, setAppAccess] = useState(user.app_access !== false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [preview, setPreview] = useState<AccessPreview | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  // Редактор состава группы: null — закрыт. Без id означает «новая группа».
  const [groupForm, setGroupForm] = useState<{
    id?: string
    name: string
    note: string
    cameraIds: string[]
  } | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [catalog, mine] = await Promise.all([accessAPI.catalog(), accessAPI.ofUser(user.id)])
      setCameras(catalog.data.cameras)
      setGroups(catalog.data.groups)
      setControllers(catalog.data.controllers ?? [])
      setAccess({
        all_cameras: mine.data.all_cameras,
        camera_ids: mine.data.camera_ids ?? [],
        group_ids: mine.data.group_ids ?? [],
        all_acs_controllers: mine.data.all_acs_controllers,
        controller_ids: mine.data.controller_ids ?? [],
      })
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('access.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [t, toast, user.id])

  useEffect(() => {
    void load()
  }, [load])

  /**
   * Какие камеры приходят через выданные группы.
   *
   * Нужно, чтобы показать это словами: камера, выданная группой, не должна
   * выглядеть как «не выдана» — иначе администратор поставит на ней личную
   * галочку и потом будет удивляться, почему снятие галочки ничего не дало.
   */
  const groupedCameras = useMemo(() => {
    const result = new Map<string, string[]>()
    for (const group of groups) {
      if (!access.group_ids.includes(group.id)) continue
      for (const cameraId of group.camera_ids) {
        const names = result.get(cameraId) ?? []
        names.push(group.name)
        result.set(cameraId, names)
      }
    }
    return result
  }, [access.group_ids, groups])

  const toggleGroup = (groupId: string) => {
    setAccess((prev) => ({
      ...prev,
      group_ids: prev.group_ids.includes(groupId)
        ? prev.group_ids.filter((id) => id !== groupId)
        : [...prev.group_ids, groupId],
    }))
    // Предпросмотр относился к прежнему набору галочек — показываем его
    // заново только по кнопке, чтобы на экране не оставалось устаревшего.
    setPreview(null)
  }

  const toggleCamera = (cameraId: string) => {
    setAccess((prev) => ({
      ...prev,
      camera_ids: prev.camera_ids.includes(cameraId)
        ? prev.camera_ids.filter((id) => id !== cameraId)
        : [...prev.camera_ids, cameraId],
    }))
    setPreview(null)
  }

  const toggleController = (controllerId: string) => {
    setAccess((prev) => ({
      ...prev,
      controller_ids: prev.controller_ids.includes(controllerId)
        ? prev.controller_ids.filter((id) => id !== controllerId)
        : [...prev.controller_ids, controllerId],
    }))
    setPreview(null)
  }

  const save = async () => {
    setSaving(true)
    try {
      // Сначала доступ по клиентам, потом объекты: если первое не пройдёт
      // (например, закрыты оба доступа), объекты не тронем — иначе выдача
      // камер применилась бы, а окно сказало об ошибке.
      await usersAPI.update(user.id, { web_access: webAccess, app_access: appAccess })
      const res = await accessAPI.save(user.id, access)
      setAccess(res.data)
      toast.success(t('access.saved'))
      onSaved?.()
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('access.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  /** «Показать как он видит» — спрашиваем сервер, а не считаем сами. */
  const runPreview = async () => {
    setPreviewLoading(true)
    try {
      const res = await accessAPI.preview(user.id)
      setPreview(res.data)
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('access.previewFailed'))
    } finally {
      setPreviewLoading(false)
    }
  }

  const saveGroup = async () => {
    if (!groupForm) return
    if (!groupForm.name.trim()) {
      toast.error(t('access.groupNameRequired'))
      return
    }
    try {
      const payload = {
        name: groupForm.name.trim(),
        note: groupForm.note,
        camera_ids: groupForm.cameraIds,
      }
      const res = groupForm.id
        ? await cameraGroupsAPI.update(groupForm.id, payload)
        : await cameraGroupsAPI.create(payload)
      toast.success(t('access.groupSaved'))
      setGroupForm(null)
      // Группу выдали сразу после создания — иначе её пришлось бы искать
      // в списке и отмечать вручную.
      if (!groupForm.id) {
        setAccess((prev) => ({ ...prev, group_ids: [...prev.group_ids, res.data.id] }))
      }
      setGroups((prev) => {
        const rest = prev.filter((g) => g.id !== res.data.id)
        return [...rest, res.data].sort((a, b) => a.name.localeCompare(b.name))
      })
      setPreview(null)
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('access.groupSaveFailed'))
    }
  }

  const removeGroup = async (group: CameraGroup) => {
    if (!window.confirm(t('access.confirmGroupDelete', { name: group.name }))) return
    try {
      await cameraGroupsAPI.remove(group.id)
      toast.success(t('access.groupDeleted'))
      setGroups((prev) => prev.filter((g) => g.id !== group.id))
      // Удалённая группа исчезает и из выдачи — убираем её из галочек,
      // иначе сохранение отправило бы ссылку на несуществующую группу.
      setAccess((prev) => ({
        ...prev,
        group_ids: prev.group_ids.filter((id) => id !== group.id),
      }))
      setPreview(null)
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('access.groupDeleteFailed'))
    }
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div
        className="modal"
        onClick={(e) => e.stopPropagation()}
        style={{ maxWidth: 860, width: '100%', maxHeight: '90vh', overflowY: 'auto' }}
      >
        <h2 style={{ marginTop: 0, marginBottom: 4 }}>
          {t('access.title', { name: user.username })}
        </h2>
        <p style={{ color: 'var(--text-muted, #888)', marginTop: 0 }}>{t('access.hint')}</p>

        {/* Куда можно входить — наверху, до списков объектов: если закрыт сам
            вход, выдача камер ничего не значит. */}
        <div
          style={{
            border: '1px solid var(--border, #333)',
            borderRadius: 12,
            padding: 12,
            marginTop: 8,
          }}
        >
          <div style={{ fontWeight: 600, marginBottom: 8 }}>{t('access.clients')}</div>
          <div className="grid grid-2">
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
              <input
                type="checkbox"
                checked={webAccess}
                onChange={(e) => setWebAccess(e.target.checked)}
              />
              <span>{t('access.clientWeb')}</span>
            </label>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
              <input
                type="checkbox"
                checked={appAccess}
                onChange={(e) => setAppAccess(e.target.checked)}
              />
              <span>{t('access.clientApp')}</span>
            </label>
          </div>
          {!webAccess && !appAccess && (
            <p style={{ color: 'var(--danger)', fontSize: 13, margin: '8px 0 0' }}>
              {t('access.clientNone')}
            </p>
          )}
        </div>

        {loading ? (
          <div style={{ padding: 24, textAlign: 'center' }}>
            <span className="spinner" />
          </div>
        ) : (
          <>
            <label
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                padding: '8px 0',
                fontWeight: 600,
              }}
            >
              <input
                type="checkbox"
                checked={access.all_cameras}
                onChange={(e) => {
                  setAccess((prev) => ({ ...prev, all_cameras: e.target.checked }))
                  setPreview(null)
                }}
              />
              <span>{t('access.allCameras')}</span>
            </label>
            <p style={{ color: 'var(--text-muted, #888)', marginTop: 0, fontSize: 13 }}>
              {t('access.allCamerasHint')}
            </p>

            {/* Пока стоит «все камеры», списки ниже ничего не решают: сервер
                в этом случае не смотрит ни на группы, ни на личные галочки.
                Показываем их неактивными, чтобы это было видно, а не
                выяснялось опытом. */}
            <fieldset
              disabled={access.all_cameras}
              style={{
                border: 'none',
                padding: 0,
                margin: 0,
                opacity: access.all_cameras ? 0.5 : 1,
              }}
            >
              <div style={{ marginTop: 16 }}>
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 8,
                    marginBottom: 8,
                  }}
                >
                  <div style={{ fontWeight: 600, flex: 1 }}>{t('access.groups')}</div>
                  <button
                    className="btn btn-outline btn-sm"
                    onClick={() => setGroupForm({ name: '', note: '', cameraIds: [] })}
                  >
                    {t('access.addGroup')}
                  </button>
                </div>

                {groups.length === 0 ? (
                  <p style={{ color: 'var(--text-muted, #888)', margin: 0 }}>
                    {t('access.noGroups')}
                  </p>
                ) : (
                  groups.map((group) => (
                    <div
                      key={group.id}
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: 8,
                        padding: '6px 0',
                        borderTop: '1px solid var(--border, #333)',
                      }}
                    >
                      <label style={{ display: 'flex', alignItems: 'center', gap: 8, flex: 1 }}>
                        <input
                          type="checkbox"
                          checked={access.group_ids.includes(group.id)}
                          onChange={() => toggleGroup(group.id)}
                        />
                        <span>{group.name}</span>
                        <span style={{ color: 'var(--text-muted, #888)', fontSize: 13 }}>
                          {t('access.cameraCount', { count: group.camera_ids.length })}
                        </span>
                      </label>
                      <button
                        className="btn btn-outline btn-sm"
                        onClick={() =>
                          setGroupForm({
                            id: group.id,
                            name: group.name,
                            note: group.note,
                            cameraIds: [...group.camera_ids],
                          })
                        }
                      >
                        {t('access.editGroup')}
                      </button>
                      <button
                        className="btn btn-outline btn-sm"
                        onClick={() => removeGroup(group)}
                      >
                        {t('access.deleteGroup')}
                      </button>
                    </div>
                  ))
                )}
              </div>

              <div style={{ marginTop: 20 }}>
                <div style={{ fontWeight: 600, marginBottom: 8 }}>
                  {t('access.cameras')}
                  <span
                    style={{ fontWeight: 400, color: 'var(--text-muted, #888)', marginLeft: 8 }}
                  >
                    {t('access.selected', {
                      count:
                        access.camera_ids.length + groupedCameras.size,
                    })}
                  </span>
                </div>

                {cameras.length === 0 ? (
                  <p style={{ color: 'var(--text-muted, #888)', margin: 0 }}>
                    {t('access.noCameras')}
                  </p>
                ) : (
                  <div className="grid grid-2">
                    {cameras.map((camera) => {
                      const viaGroups = groupedCameras.get(camera.id) ?? []
                      const fromGroup = viaGroups.length > 0
                      return (
                        <label
                          key={camera.id}
                          style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: 8,
                            padding: '4px 0',
                            cursor: fromGroup ? 'default' : 'pointer',
                            color: fromGroup ? 'var(--text-muted, #888)' : undefined,
                          }}
                        >
                          <input
                            type="checkbox"
                            checked={fromGroup || access.camera_ids.includes(camera.id)}
                            disabled={fromGroup}
                            onChange={() => toggleCamera(camera.id)}
                          />
                          <span>{camera.name}</span>
                          {fromGroup && (
                            <span style={{ fontSize: 12 }}>
                              {t('access.viaGroup', { name: viaGroups.join(', ') })}
                            </span>
                          )}
                        </label>
                      )
                    })}
                  </div>
                )}
              </div>
            </fieldset>

            {/* СКУД — второй раздел того же документа. Единица выдачи здесь
                контроллер: выдача открывает его двери и показывает его
                проходы в журнале. Двери перечислены справочно — выдать
                права на отдельную дверь нельзя, команда открытия уходит
                контроллеру с его собственным обозначением двери. */}
            <div style={{ marginTop: 24, borderTop: '1px solid var(--border, #333)', paddingTop: 16 }}>
              <label
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  padding: '8px 0',
                  fontWeight: 600,
                }}
              >
                <input
                  type="checkbox"
                  checked={access.all_acs_controllers}
                  onChange={(e) => {
                    setAccess((prev) => ({ ...prev, all_acs_controllers: e.target.checked }))
                    setPreview(null)
                  }}
                />
                <span>{t('access.allControllers')}</span>
              </label>
              <p style={{ color: 'var(--text-muted, #888)', marginTop: 0, fontSize: 13 }}>
                {t('access.allControllersHint')}
              </p>

              {controllers.length === 0 ? (
                <p style={{ color: 'var(--text-muted, #888)', margin: 0 }}>
                  {t('access.noControllers')}
                </p>
              ) : (
                <fieldset
                  disabled={access.all_acs_controllers}
                  style={{
                    border: 'none',
                    padding: 0,
                    margin: 0,
                    opacity: access.all_acs_controllers ? 0.5 : 1,
                  }}
                >
                  <div style={{ fontWeight: 600, marginBottom: 8 }}>
                    {t('access.controllers')}
                    <span
                      style={{ fontWeight: 400, color: 'var(--text-muted, #888)', marginLeft: 8 }}
                    >
                      {t('access.selectedControllers', { count: access.controller_ids.length })}
                    </span>
                  </div>

                  {controllers.map((ctrl) => {
                    const checked = access.controller_ids.includes(ctrl.id)
                    return (
                      <div
                        key={ctrl.id}
                        style={{
                          padding: '6px 0',
                          borderTop: '1px solid var(--border, #333)',
                        }}
                      >
                        <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                          <input
                            type="checkbox"
                            checked={checked}
                            onChange={() => toggleController(ctrl.id)}
                          />
                          <span>{ctrl.name}</span>
                          <span style={{ color: 'var(--text-muted, #888)', fontSize: 13 }}>
                            {ctrl.vendor}
                          </span>
                        </label>
                        {ctrl.doors.length > 0 && (
                          <div
                            style={{
                              marginLeft: 26,
                              fontSize: 13,
                              color: 'var(--text-muted, #888)',
                            }}
                          >
                            {t('access.controllerDoors', {
                              list: ctrl.doors.map((d) => d.name).join(', '),
                            })}
                          </div>
                        )}
                      </div>
                    )
                  })}
                </fieldset>
              )}
            </div>

            {/* Предпросмотр: администратор не должен догадываться, что вышло
                из набора галочек — выдать доступ не тому слишком легко. */}
            <div style={{ marginTop: 20 }}>
              <button className="btn btn-outline" onClick={runPreview} disabled={previewLoading}>
                {previewLoading ? t('access.previewLoading') : t('access.preview')}
              </button>
              {preview && (
                <div
                  className="card"
                  style={{ marginTop: 10, padding: 12, fontSize: 14 }}
                >
                  <div style={{ fontWeight: 600, marginBottom: 6 }}>
                    {preview.all_cameras
                      ? t('access.previewAll', { total: preview.total_cameras })
                      : t('access.previewCount', {
                          count: preview.cameras.length,
                          total: preview.total_cameras,
                        })}
                  </div>
                  {preview.cameras.length === 0 ? (
                    <div style={{ color: 'var(--text-muted, #888)' }}>
                      {t('access.previewEmpty')}
                    </div>
                  ) : (
                    <div style={{ color: 'var(--text-muted, #888)' }}>
                      {preview.cameras.map((c) => c.name).join(', ')}
                    </div>
                  )}

                  {/* СКУД показываем во всех случаях, даже когда контроллеров
                      в системе нет: иначе администратор решит, что проверка
                      их не касается. */}
                  <div style={{ fontWeight: 600, marginTop: 10, marginBottom: 6 }}>
                    {preview.all_controllers
                      ? t('access.previewControllersAll', { total: preview.total_controllers })
                      : t('access.previewControllersCount', {
                          count: preview.controllers.length,
                          total: preview.total_controllers,
                        })}
                  </div>
                  {preview.controllers.length === 0 ? (
                    <div style={{ color: 'var(--text-muted, #888)' }}>
                      {t('access.previewControllersEmpty')}
                    </div>
                  ) : (
                    <div style={{ color: 'var(--text-muted, #888)' }}>
                      {preview.controllers.map((c) => c.name).join(', ')}
                    </div>
                  )}
                </div>
              )}
            </div>

            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 20 }}>
              <button className="btn btn-outline" onClick={onClose}>
                {t('access.close')}
              </button>
              <button
                className="btn btn-primary"
                onClick={save}
                disabled={saving}
              >
                {saving ? t('access.saving') : t('access.save')}
              </button>
            </div>
          </>
        )}

        {/* Редактор состава группы: имя и список камер. Состав переписывается
            целиком — как и выдача, интерфейс присылает итоговый набор. */}
        {groupForm && (
          <div className="modal-overlay" onClick={() => setGroupForm(null)}>
            <div
              className="modal"
              onClick={(e) => e.stopPropagation()}
              style={{ maxWidth: 720, width: '100%', maxHeight: '85vh', overflowY: 'auto' }}
            >
              <h3 style={{ marginTop: 0 }}>
                {groupForm.id ? t('access.editGroupTitle') : t('access.addGroupTitle')}
              </h3>

              <div className="grid grid-2">
                <label>
                  <div style={{ marginBottom: 6 }}>{t('access.groupName')}</div>
                  <input
                    className="input"
                    value={groupForm.name}
                    onChange={(e) => setGroupForm({ ...groupForm, name: e.target.value })}
                    placeholder={t('access.groupNamePlaceholder')}
                  />
                </label>
                <label>
                  <div style={{ marginBottom: 6 }}>{t('access.groupNote')}</div>
                  <input
                    className="input"
                    value={groupForm.note}
                    onChange={(e) => setGroupForm({ ...groupForm, note: e.target.value })}
                  />
                </label>
              </div>

              <div style={{ marginTop: 14, marginBottom: 6, fontWeight: 600 }}>
                {t('access.groupCameras')}
              </div>
              <div className="grid grid-2">
                {cameras.map((camera) => (
                  <label
                    key={camera.id}
                    style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '4px 0' }}
                  >
                    <input
                      type="checkbox"
                      checked={groupForm.cameraIds.includes(camera.id)}
                      onChange={() =>
                        setGroupForm({
                          ...groupForm,
                          cameraIds: groupForm.cameraIds.includes(camera.id)
                            ? groupForm.cameraIds.filter((id) => id !== camera.id)
                            : [...groupForm.cameraIds, camera.id],
                        })
                      }
                    />
                    <span>{camera.name}</span>
                  </label>
                ))}
              </div>

              <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 20 }}>
                <button className="btn btn-outline" onClick={() => setGroupForm(null)}>
                  {t('access.cancel')}
                </button>
                <button className="btn btn-primary" onClick={saveGroup}>
                  {t('access.save')}
                </button>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
