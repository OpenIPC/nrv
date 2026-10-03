package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
)

// Хранение логов с камер.
//
// Логи нужны не для мгновенной реакции, а для разбора: открыть страницу,
// отфильтровать по камере и времени, найти причину. Поэтому репозиторий
// заточен под выборку с фильтрами, а не под потоковую обработку.

type CameraLogRepo struct {
	db *pgxpool.Pool
}

func NewCameraLogRepo(db *pgxpool.Pool) *CameraLogRepo {
	return &CameraLogRepo{db: db}
}

// SaveBatch сохраняет пачку строк.
//
// Одна вставка с несколькими наборами значений вместо запроса на каждую
// строку: при разборе инцидента строки приходят десятками, и отдельный
// запрос на каждую превратил бы приём в узкое место.
//
// Все значения подставляются параметрами, а не склейкой строки. Причина
// не только в защите от внедрения: тексты логов содержат кавычки, знаки
// процента и переводы строк, и склейка ломалась бы на них.
func (r *CameraLogRepo) SaveBatch(ctx context.Context, entries []domain.SyslogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(`INSERT INTO camera_logs
		(camera_id, source_ip, hostname, app, severity, facility,
		 message, logged_at, received_at, fingerprint, repeats)
		VALUES `)

	const cols = 11
	args := make([]any, 0, len(entries)*cols)
	for i, e := range entries {
		if i > 0 {
			sb.WriteByte(',')
		}
		// Номера параметров считаем вручную: pgx не поддерживает
		// автоматическую нумерацию для многострочных VALUES.
		base := i * cols
		fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6,
			base+7, base+8, base+9, base+10, base+11)

		args = append(args,
			e.CameraID, e.SourceIP, e.Hostname, e.App, e.Severity, e.Facility,
			e.Message, e.LoggedAt, e.ReceivedAt, e.Fingerprint, 1,
		)
	}

	if _, err := r.db.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("сохранение логов: %w", err)
	}
	return nil
}

// CameraIDByIP находит камеру по адресу источника.
//
// Датаграмма syslog не несёт имени хоста, поэтому единственная зацепка —
// адрес. Отсюда же требование к камерам иметь постоянный адрес: при смене
// адреса старые логи перестанут связываться с камерой.
//
// Кэша в памяти нет намеренно: запрос идёт на каждую принятую строку, но
// это дешёвый поиск по индексу, а кэш пришлось бы сбрасывать при каждой
// правке списка камер. При потоке в десятки строк в минуту выгода от кэша
// не оправдывает риска отдать устаревшую привязку.
func (r *CameraLogRepo) CameraIDByIP(ctx context.Context, ip string) string {
	var id string
	err := r.db.QueryRow(ctx,
		`SELECT id::text FROM cameras WHERE ip = $1 LIMIT 1`, ip).Scan(&id)
	if err != nil {
		// Камера не найдена — это не ошибка: строка сохранится без
		// привязки, и именно такие записи объясняют, почему устройство
		// не появилось в системе.
		return ""
	}
	return id
}

// List выбирает логи по условиям.
//
// Имя камеры подтягивается соединением, а не отдельным запросом на каждую
// строку: иначе на странице из сотни записей получилось бы сто лишних
// обращений к базе.
func (r *CameraLogRepo) List(ctx context.Context, f domain.LogFilter) ([]domain.LogEntry, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if f.CameraID != "" {
		where = append(where, "l.camera_id = "+arg(f.CameraID))
	}
	if f.SourceIP != "" {
		where = append(where, "l.source_ip = "+arg(f.SourceIP))
	}
	if f.App != "" {
		where = append(where, "l.app = "+arg(f.App))
	}
	if f.MaxSeverity != nil {
		// Уровень отсутствует у части строк — они в выборку не попадают.
		// Это правильно: если устройство не сообщило важность, считать
		// его сообщение ошибкой нельзя.
		where = append(where, "l.severity IS NOT NULL AND l.severity <= "+arg(*f.MaxSeverity))
	}
	if f.From != nil {
		where = append(where, "l.received_at >= "+arg(*f.From))
	}
	if f.To != nil {
		where = append(where, "l.received_at <= "+arg(*f.To))
	}
	if f.Search != "" {
		where = append(where, "l.message ILIKE "+arg("%"+f.Search+"%"))
	}

	query := `
		SELECT l.id, l.camera_id, COALESCE(c.name, '') AS camera_name,
		       COALESCE(c.ip, '') AS camera_ip,
		       l.source_ip::text, l.app, l.severity, l.message,
		       l.logged_at, l.received_at
		FROM camera_logs l
		LEFT JOIN cameras c ON c.id = l.camera_id`

	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY l.received_at DESC"

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		// Потолок в 500 строк: страница оператора не должна вытягивать
		// из базы десятки тысяч записей, а поиск по истории делается
		// через фильтры, а не прокруткой.
		limit = 100
	}
	query += " LIMIT " + arg(limit)
	if f.Offset > 0 {
		query += " OFFSET " + arg(f.Offset)
	}

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("выборка логов: %w", err)
	}
	defer rows.Close()

	var out []domain.LogEntry
	for rows.Next() {
		var e domain.LogEntry
		if err := rows.Scan(&e.ID, &e.CameraID, &e.CameraName, &e.CameraIP,
			&e.SourceIP, &e.App, &e.Severity, &e.Message,
			&e.LoggedAt, &e.ReceivedAt); err != nil {
			return nil, fmt.Errorf("чтение лога: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Summary считает сводку за период.
func (r *CameraLogRepo) Summary(ctx context.Context, since time.Time) (*domain.LogSummary, error) {
	summary := &domain.LogSummary{
		BySeverity: map[string]int64{},
		ByCamera:   map[string]int64{},
		ByApp:      map[string]int64{},
	}

	if err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM camera_logs WHERE received_at >= $1`, since).
		Scan(&summary.Total); err != nil {
		return nil, fmt.Errorf("подсчёт логов: %w", err)
	}

	if err := r.fillGrouped(ctx,
		`SELECT COALESCE(severity::text, 'не указан'), count(*)
		 FROM camera_logs WHERE received_at >= $1 GROUP BY 1`,
		since, summary.BySeverity); err != nil {
		return nil, err
	}

	// В сводке по камерам берём имя, а не идентификатор: оператору нужно
	// название, а не UUID. Группы без названия и без программы отдаём пустым
	// ключом, а не русской подписью: интерфейс переводится на четыре языка, и
	// русское слово из запроса показалось бы в сводке китайцу и корейцу.
	// Подпись к пустому ключу ставит сам интерфейс.
	if err := r.fillGrouped(ctx,
		`SELECT COALESCE(c.name, ''), count(*)
		 FROM camera_logs l LEFT JOIN cameras c ON c.id = l.camera_id
		 WHERE l.received_at >= $1 GROUP BY 1 ORDER BY 2 DESC LIMIT 20`,
		since, summary.ByCamera); err != nil {
		return nil, err
	}

	if err := r.fillGrouped(ctx,
		`SELECT COALESCE(app, ''), count(*)
		 FROM camera_logs WHERE received_at >= $1 GROUP BY 1 ORDER BY 2 DESC LIMIT 20`,
		since, summary.ByApp); err != nil {
		return nil, err
	}

	return summary, nil
}

// fillGrouped выполняет сгруппированный запрос и раскладывает результат в карту.
func (r *CameraLogRepo) fillGrouped(ctx context.Context, query string, since time.Time, into map[string]int64) error {
	rows, err := r.db.Query(ctx, query, since)
	if err != nil {
		return fmt.Errorf("сводка по логам: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var count int64
		if err := rows.Scan(&key, &count); err != nil {
			return fmt.Errorf("чтение сводки: %w", err)
		}
		into[key] = count
	}
	return rows.Err()
}

// DistinctApps отдаёт список программ, встречающихся в логах.
//
// Нужен для выпадающего списка фильтра: оператор не должен угадывать
// название программы по памяти.
func (r *CameraLogRepo) DistinctApps(ctx context.Context, since time.Time) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT DISTINCT app FROM camera_logs
		 WHERE received_at >= $1 AND app <> '' ORDER BY app`, since)
	if err != nil {
		return nil, fmt.Errorf("список программ: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var app string
		if err := rows.Scan(&app); err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

// DeleteOlder удаляет логи старше указанного момента и возвращает
// число удалённых строк.
//
// Глубина хранения ограничена намеренно: логи нужны для разбора свежих
// происшествий, а старая история только занимает место и замедляет поиск.
// Тревожные события при этом остаются — они попадают в журнал событий,
// который чистится по своим правилам.
func (r *CameraLogRepo) DeleteOlder(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM camera_logs WHERE received_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("удаление старых логов: %w", err)
	}
	return tag.RowsAffected(), nil
}
