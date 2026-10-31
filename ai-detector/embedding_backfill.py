"""Досчёт векторов для событий, у которых их ещё нет.

Зачем. Векторы появляются только у новых событий: детектор считает их в
момент публикации. Всё, что записано до включения поиска по смыслу, осталось
без векторов, и поиск по такому архиву находил бы пустоту — хотя снимки
событий лежат на диске и посчитать по ним вектор ничего не стоит.

Как работает. Отдельный фоновый поток раз в несколько секунд забирает
несколько событий без векторов, вырезает объект по рамке из снимка и
сохраняет вектор в ту же таблицу, что и бэкенд. Поток одноразовый: когда
непосчитанных событий не остаётся, он завершается и больше не мешает.

Почему отдельный поток, а не пачка при старте. Событий может быть много
(месяцы архива), а CLIP на процессоре считает кадр 100–200 мс. Считать всё
сразу — значит на несколько минут занять процессор, который нужен детекции.
"""

from __future__ import annotations

import logging
import os
import time

logger = logging.getLogger("nvr.embeddings.backfill")


def _snapshot_file(snapshot_path: str) -> str | None:
    """Превращает путь из базы в путь к файлу.

    В базе путь записан как «local:/nvr/...» или «minio:bucket/key».
    Локальный снимок — обычный файл в смонтированном каталоге; объект MinIO
    этому потоку недоступен, такие события просто пропускаются.
    """
    if not snapshot_path or not snapshot_path.startswith("local:"):
        return None
    return snapshot_path[len("local:"):]


def _crop_for_event(image, bbox: dict | None, metadata: dict | None):
    """Вырезает объект по рамке, пересчитанной в координаты снимка.

    Рамка в событии задана в координатах кадра детекции (он ужат до 960 px),
    а снимок — кадр основного потока (1920 px). Без пересчёта вырез попал бы
    не туда, и вектор описывал бы случайный кусок кадра.
    """
    from embeddings import crop_with_padding

    if not bbox:
        return image

    frame_w = float((metadata or {}).get("frame_w") or 0)
    frame_h = float((metadata or {}).get("frame_h") or 0)
    height, width = image.shape[:2]
    if frame_w <= 0 or frame_h <= 0:
        # Размер кадра неизвестен — считаем, что рамка уже в координатах снимка.
        scale_x = scale_y = 1.0
    else:
        scale_x, scale_y = width / frame_w, height / frame_h

    scaled = {
        "x": float(bbox.get("x", 0)) * scale_x,
        "y": float(bbox.get("y", 0)) * scale_y,
        "w": float(bbox.get("w", 0)) * scale_x,
        "h": float(bbox.get("h", 0)) * scale_y,
    }
    return crop_with_padding(image, scaled) or image


def run_backfill(db_url: str, embedder, model_label: str, limit: int = 2000,
                 pause: float = 1.5, batch: int = 8) -> None:
    """Досчитывает векторы порциями, пока не закончатся события."""
    import cv2
    import psycopg2

    processed = 0
    empty_passes = 0

    while processed < limit:
        try:
            conn = psycopg2.connect(db_url)
        except Exception as e:  # noqa: BLE001
            logger.warning(f"досчёт векторов: нет соединения с базой: {e}")
            return

        try:
            with conn.cursor() as cur:
                cur.execute(
                    """
                    SELECT e.id, e.snapshot_path, e.bbox, e.metadata
                    FROM detection_events e
                    LEFT JOIN event_embeddings em
                           ON em.event_id = e.id AND em.model = %s
                    WHERE e.snapshot_path IS NOT NULL
                      AND em.event_id IS NULL
                    ORDER BY e.timestamp DESC
                    LIMIT %s
                    """,
                    (model_label, batch),
                )
                rows = cur.fetchall()

            if not rows:
                empty_passes += 1
                # Два пустых прохода подряд: всё посчитано. Пауза перед
                # повтором нужна, чтобы не заметить события, которые детектор
                # как раз публикует (они приходят с готовым вектором).
                if empty_passes >= 2:
                    logger.info(f"досчёт векторов завершён: посчитано {processed}")
                    return
                time.sleep(pause * 2)
                continue

            for event_id, snapshot_path, bbox, metadata in rows:
                path = _snapshot_file(snapshot_path or "")
                if not path or not os.path.exists(path):
                    # Объект в MinIO или файл уже удалён по сроку хранения:
                    # помечаем как «посчитано», иначе этот же выбор будет
                    # возвращаться на каждом проходе.
                    _store_vector(cur_conn=conn, event_id=event_id,
                                  model=model_label, vector=None)
                    continue

                image = cv2.imread(path)
                if image is None:
                    _store_vector(cur_conn=conn, event_id=event_id,
                                  model=model_label, vector=None)
                    continue

                crop = _crop_for_event(image, bbox, metadata)
                vector = embedder.embed_image(crop)
                _store_vector(cur_conn=conn, event_id=event_id,
                              model=model_label, vector=vector)
                processed += 1

            conn.commit()
        except Exception as e:  # noqa: BLE001
            logger.warning(f"досчёт векторов прерван: {e}")
            return
        finally:
            conn.close()

        logger.info(f"досчёт векторов: {processed} событий")
        time.sleep(pause)


def _store_vector(cur_conn, event_id, model: str, vector: list[float] | None) -> None:
    """Пишет вектор (или пустой вектор-заглушку) в event_embeddings.

    Пустой вектор — это «посчитано, но картинки не было». Без такой записи
    событие без снимка возвращалось бы в выборку бесконечно.
    """
    import struct

    import psycopg2
    import psycopg2.extensions

    payload = b"" if vector is None else struct.pack(f"<{len(vector)}f", *vector)
    try:
        with cur_conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO event_embeddings (event_id, model, dim, vector)
                VALUES (%s, %s, %s, %s)
                ON CONFLICT (event_id, model) DO NOTHING
                """,
                (event_id, model, len(vector or []), psycopg2.Binary(payload)),
            )
    except Exception as e:  # noqa: BLE001
        logger.debug(f"не удалось сохранить вектор {event_id}: {e}")
        cur_conn.rollback()


def run_color_backfill(db_url: str, limit: int = 2000, pause: float = 0.5) -> None:
    """Досчитывает цвет объекта для событий, записанных до этой возможности.

    Цвет хранится в metadata события, а не отдельной таблицей, поэтому
    досчёт — это UPDATE. Идём по событиям со снимком, у которых цвета ещё
    нет: без него поиск «красная машина» не нашёл бы ничего в старом архиве,
    хотя картинки на диске лежат.
    """
    import cv2
    import psycopg2

    from attributes import dominant_color

    processed = 0
    while processed < limit:
        try:
            conn = psycopg2.connect(db_url)
        except Exception as e:  # noqa: BLE001
            logger.warning(f"досчёт цвета: нет соединения с базой: {e}")
            return

        try:
            with conn.cursor() as cur:
                cur.execute(
                    """
                    SELECT id, snapshot_path, bbox, metadata
                    FROM detection_events
                    WHERE snapshot_path IS NOT NULL
                      AND (metadata->>'color') IS NULL
                    ORDER BY timestamp DESC
                    LIMIT 16
                    """
                )
                rows = cur.fetchall()

            if not rows:
                logger.info(f"досчёт цвета завершён: обработано {processed}")
                return

            for event_id, snapshot_path, bbox, metadata in rows:
                path = _snapshot_file(snapshot_path or "")
                image = cv2.imread(path) if path and os.path.exists(path) else None
                if image is None:
                    # Снимка нет: помечаем как «обработано», иначе это же
                    # событие вернётся в выборку на следующем проходе.
                    _mark_color_processed(conn, event_id)
                    continue

                color, color_hex, area = dominant_color(image, bbox)
                if color:
                    _store_color(conn, event_id, color, color_hex, area)
                    processed += 1
                else:
                    _mark_color_processed(conn, event_id)

            conn.commit()
        except Exception as e:  # noqa: BLE001
            logger.warning(f"досчёт цвета прерван: {e}")
            return
        finally:
            conn.close()

        logger.info(f"досчёт цвета: {processed} событий")
        time.sleep(pause)


def _store_color(conn, event_id, color: str, color_hex: str | None, area: float) -> None:
    """Дописывает признаки в metadata, не затирая остальное."""
    try:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE detection_events
                SET metadata = COALESCE(metadata, '{}'::jsonb)
                               || jsonb_build_object('color', %s::text,
                                                     'color_hex', %s::text,
                                                     'area', %s::double precision)
                WHERE id = %s
                """,
                (color, color_hex or "", area, event_id),
            )
    except Exception as e:  # noqa: BLE001
        logger.debug(f"не удалось сохранить цвет {event_id}: {e}")
        conn.rollback()


def _mark_color_processed(conn, event_id) -> None:
    """Ставит пустой цвет: «смотрели, определить не удалось»."""
    try:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE detection_events
                SET metadata = COALESCE(metadata, '{}'::jsonb) || '{"color": ""}'::jsonb
                WHERE id = %s
                """,
                (event_id,),
            )
    except Exception as e:  # noqa: BLE001
        logger.debug(f"не удалось пометить событие {event_id}: {e}")
        conn.rollback()
