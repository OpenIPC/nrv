import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ActivityIndicator,
  FlatList,
  Image,
  Pressable,
  RefreshControl,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { useApp } from '../state/AppContext';
import { describeNetworkError } from '../net/api';
import { joinUrl } from '../net/address';
import { colors, radius, spacing } from '../theme';
import type { Camera } from '../types';

/**
 * Интервал обновления кадров в списке камер.
 *
 * Кадр обновляется по таймеру, а не видеопотоком: держать десяток HLS-сессий
 * ради превью нельзя — камеры ограничены по числу одновременных подключений
 * и начнут отказывать. Кадр стоит одного HTTP-запроса.
 */
const REFRESH_MS = 10000;

/** Ширина запрашиваемого кадра: для плитки этого достаточно. */
const THUMB_WIDTH = 480;

/**
 * Сколько кадров запрашивать одновременно.
 *
 * Ограничение вынужденное: камеры OpenIPC отказывают, если запросов
 * слишком много, и в списке появлялись бы пустые плитки. Остальные
 * запросы встают в очередь.
 *
 * Очередь нужна именно до момента получения картинки: если освобождать
 * место сразу после подстановки адреса в Image, все запросы уйдут
 * одновременно и ограничение не сработает.
 */
const PREVIEW_CONCURRENCY = 3;

let previewRunning = 0;
const previewQueue: Array<() => void> = [];

/** Занимает место в очереди. Возвращает функцию освобождения. */
function acquirePreviewSlot(): Promise<() => void> {
  return new Promise((resolve) => {
    const release = () => {
      previewRunning = Math.max(0, previewRunning - 1);
      const next = previewQueue.shift();
      if (next) next();
    };

    if (previewRunning < PREVIEW_CONCURRENCY) {
      previewRunning += 1;
      resolve(release);
    } else {
      // Запрос ждёт своей очереди и получает место уже внутри неё.
      previewQueue.push(() => {
        previewRunning += 1;
        resolve(release);
      });
    }
  });
}

/** Плитка камеры. */
function CameraCard({
  camera,
  onPress,
  token,
  baseUrl,
}: {
  camera: Camera;
  onPress: () => void;
  token: string | null;
  baseUrl: string;
}) {
  const [src, setSrc] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  const [tick, setTick] = useState(0);
  // Освобождение места в очереди превью. Храним в ref: функция нужна в
  // обработчиках загрузки картинки, которые создаются один раз.
  const releaseRef = useRef<(() => void) | null>(null);

  // Адрес кадра собираем из адреса сервера: сервер отдаёт относительный
  // путь, а токен идёт в query — тег Image не умеет слать заголовки.
  const previewUrl = useMemo(() => {
    if (!token) return null;
    const path = `/api/v1/cameras/${camera.id}/preview?w=${THUMB_WIDTH}&jwt=${encodeURIComponent(token)}`;
    // Параметр _r сбрасывает кэш картинки: без него React Native покажет
    // тот же кадр и обновления не будет видно.
    return `${joinUrl(baseUrl, path)}&_r=${tick}`;
  }, [camera.id, token, baseUrl, tick]);

  useEffect(() => {
    if (!previewUrl) return;
    let cancelled = false;

    // Проходим очередь перед запросом кадра и ждём картинку: место
    // освобождается только в onLoad или onError.
    void (async () => {
      const release = await acquirePreviewSlot();
      if (cancelled) {
        release();
        return;
      }
      releaseRef.current = release;
      setSrc(previewUrl);
      setFailed(false);
    })();

    return () => {
      cancelled = true;
    };
  }, [previewUrl]);

  /** Освобождает место в очереди один раз, чтобы не уйти в минус. */
  const finish = useCallback(() => {
    if (releaseRef.current) {
      releaseRef.current();
      releaseRef.current = null;
    }
  }, []);

  // Страховка: если камера не ответила ни картинкой, ни ошибкой, место
  // в очереди всё равно освобождается — иначе список постепенно встанет.
  useEffect(() => {
    const timer = setTimeout(finish, 20000);
    return () => {
      clearTimeout(timer);
      finish();
    };
  }, [finish, src]);

  useEffect(() => {
    const timer = setInterval(() => setTick((value) => value + 1), REFRESH_MS);
    return () => clearInterval(timer);
  }, []);

  const isOnline = camera.status !== 'offline';

  return (
    <Pressable style={styles.card} onPress={onPress}>
      <View style={styles.thumbBox}>
        {src && !failed ? (
          <Image
            source={{ uri: src }}
            style={styles.thumb}
            resizeMode="cover"
            onLoad={finish}
            onError={() => {
              finish();
              setFailed(true);
            }}
          />
        ) : (
          <View style={styles.thumbPlaceholder}>
            <Text style={styles.thumbPlaceholderText}>
              {failed ? 'Кадр недоступен' : 'Загрузка…'}
            </Text>
          </View>
        )}

        <View
          style={[
            styles.statusPill,
            { backgroundColor: isOnline ? colors.success : colors.danger },
          ]}
        >
          <Text style={styles.statusPillText}>
            {isOnline ? 'онлайн' : 'офлайн'}
          </Text>
        </View>
      </View>

      <View style={styles.cardBody}>
        <Text style={styles.cardName} numberOfLines={1}>
          {camera.name}
        </Text>
        <Text style={styles.cardMeta} numberOfLines={1}>
          {camera.ip || camera.rtsp_url || 'адрес не указан'}
        </Text>
      </View>
    </Pressable>
  );
}

/**
 * Список камер.
 */
export default function CamerasScreen({
  onOpenCamera,
  onOpenArchive,
  onOpenAccess,
  onOpenServers,
}: {
  onOpenCamera: (camera: Camera) => void;
  onOpenArchive: () => void;
  /** Контроль доступа: двери и журнал проходов. */
  onOpenAccess: () => void;
  onOpenServers: () => void;
}) {
  const { client, current, token } = useApp();
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(
    async (silent = false) => {
      if (!client) return;
      if (!silent) setLoading(true);
      try {
        const list = await client.listCameras();
        setCameras(list);
        setError(null);
      } catch (err) {
        setError(describeNetworkError(err));
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [client],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const onlineCount = cameras.filter((c) => c.status !== 'offline').length;

  if (loading && cameras.length === 0) {
    return (
      <View style={styles.centered}>
        <ActivityIndicator color={colors.primary} size="large" />
        <Text style={styles.centeredText}>Читаю список камер…</Text>
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <View style={styles.headerTop}>
          <View style={styles.headerText}>
            <Text style={styles.title}>Камеры</Text>
            <Text style={styles.subtitle}>
              {current?.name} · {onlineCount} из {cameras.length} онлайн
            </Text>
          </View>
          <Pressable style={styles.headerButton} onPress={onOpenServers}>
            <Text style={styles.headerButtonText}>Серверы</Text>
          </Pressable>
        </View>

        <View style={styles.headerRow}>
          <Pressable style={styles.archiveButton} onPress={onOpenArchive}>
            <Text style={styles.archiveButtonText}>Архив записей</Text>
          </Pressable>
          {/* Доступ рядом с архивом: обе кнопки ведут в разделы, куда
              оператор заходит регулярно, и прятать их в меню незачем. */}
          <Pressable style={styles.archiveButton} onPress={onOpenAccess}>
            <Text style={styles.archiveButtonText}>Доступ</Text>
          </Pressable>
        </View>
      </View>

      {error && (
        <View style={styles.errorBox}>
          <Text style={styles.errorText}>{error}</Text>
        </View>
      )}

      <FlatList
        data={cameras}
        keyExtractor={(item) => item.id}
        numColumns={2}
        columnWrapperStyle={styles.row}
        contentContainerStyle={styles.list}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={() => {
              setRefreshing(true);
              void load(true);
            }}
            tintColor={colors.textSecondary}
          />
        }
        ListEmptyComponent={
          <View style={styles.centered}>
            <Text style={styles.centeredText}>Камер пока нет</Text>
          </View>
        }
        renderItem={({ item }) => (
          <CameraCard
            camera={item}
            token={token}
            baseUrl={current?.baseUrl ?? ''}
            onPress={() => onOpenCamera(item)}
          />
        )}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: colors.background },
  centered: { flex: 1, alignItems: 'center', justifyContent: 'center', gap: spacing.md },
  centeredText: { color: colors.textSecondary, fontSize: 14 },

  header: { padding: spacing.lg, paddingBottom: spacing.sm },
  headerTop: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    justifyContent: 'space-between',
    gap: spacing.md,
  },
  headerText: { flex: 1 },
  title: { fontSize: 26, fontWeight: '700', color: colors.text },
  subtitle: { fontSize: 13, color: colors.textSecondary, marginTop: spacing.xs },
  headerButton: {
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.md,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
  },
  headerButtonText: { color: colors.text, fontSize: 13, fontWeight: '500' },
  headerRow: {
    flexDirection: 'row',
    gap: spacing.sm,
    marginTop: spacing.md,
  },
  archiveButton: {
    backgroundColor: colors.surface,
    borderRadius: radius.md,
    borderWidth: 1,
    borderColor: colors.border,
    paddingVertical: spacing.md,
    alignItems: 'center',
  },
  archiveButtonText: { color: colors.text, fontSize: 15, fontWeight: '500' },

  list: { padding: spacing.lg, paddingTop: spacing.sm, gap: spacing.md },
  row: { gap: spacing.md },

  card: {
    flex: 1,
    backgroundColor: colors.surface,
    borderRadius: radius.lg,
    borderWidth: 1,
    borderColor: colors.border,
    overflow: 'hidden',
  },
  thumbBox: { aspectRatio: 16 / 9, backgroundColor: colors.surfaceAlt },
  thumb: { width: '100%', height: '100%' },
  thumbPlaceholder: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
  },
  thumbPlaceholderText: { color: colors.textMuted, fontSize: 12 },
  statusPill: {
    position: 'absolute',
    top: spacing.sm,
    left: spacing.sm,
    borderRadius: radius.sm,
    paddingHorizontal: spacing.sm,
    paddingVertical: 2,
  },
  statusPillText: { color: '#fff', fontSize: 10, fontWeight: '600' },
  cardBody: { padding: spacing.md },
  cardName: { fontSize: 14, fontWeight: '600', color: colors.text },
  cardMeta: { fontSize: 11, color: colors.textMuted, marginTop: 2 },

  errorBox: {
    marginHorizontal: spacing.lg,
    marginBottom: spacing.sm,
    backgroundColor: 'rgba(239,68,68,0.12)',
    borderWidth: 1,
    borderColor: colors.danger,
    borderRadius: radius.md,
    padding: spacing.md,
  },
  errorText: { color: colors.text, fontSize: 13 },
});
