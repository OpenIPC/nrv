package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
)

// ErrGroupNumberTaken — номер группы уже занят другой группой.
//
// Номер группы попадает в план набора Asterisk как extension, поэтому
// дубликат недопустим: два одинаковых номера означали бы, что вызов уйдёт
// случайной группе. Проверяет это уникальный индекс в базе, а здесь код
// ошибки драйвера превращается в понятный текст для интерфейса.
var ErrGroupNumberTaken = errors.New("номер группы уже занят")

// SipRepo — абоненты, группы вызова и правила SIP-домофонии.
type SipRepo struct {
	db *pgxpool.Pool
}

func NewSipRepo(db *pgxpool.Pool) *SipRepo {
	return &SipRepo{db: db}
}

// ErrNotFound объявлен рядом с другими репозиториями (recognition_repo.go)
// и используется здесь для ответа 404 в обработчиках.

const sipAccountColumns = `
	a.id, a.number, a.password, a.kind, a.display_name,
	a.camera_id, a.controller_id, COALESCE(host(a.host), ''),
	a.enabled, a.created_at, a.updated_at,
	a.notify_telegram, a.notify_max, a.notify_missed, a.record_missed, a.notes,
	a.vendor, a.user_id, COALESCE(u.username, '')`

// sipAccountSwitch — привязка к коммутатору, приклеенная к выборке абонента.
//
// Через LEFT JOIN, а не отдельным запросом: без привязки живёт большинство
// устройств, и второй запрос ради неё гонял бы базу на каждый список.
const sipAccountSwitch = `
	sp.switch_id, COALESCE(sp.port_number, 0), COALESCE(s.name, '')`

const sipAccountJoins = `
	LEFT JOIN sip_switch_port sp ON sp.account_id = a.id
	LEFT JOIN switches s ON s.id = sp.switch_id
	LEFT JOIN users u ON u.id = a.user_id`

// scanSipAccount читает строку выборки в абонента.
//
// Отдельная функция, потому что порядок полей повторяется в трёх местах
// (список, чтение одного, вставка), и расхождение в нём компилятор не ловит:
// оно вылезает ошибкой разбора уже на живом запросе.
func scanSipAccount(row interface {
	Scan(dest ...any) error
}) (*domain.SipAccount, error) {
	var a domain.SipAccount
	err := row.Scan(
		&a.ID, &a.Number, &a.Password, &a.Kind, &a.Name,
		&a.CameraID, &a.ControllerID, &a.Host,
		&a.Enabled, &a.CreatedAt, &a.UpdatedAt,
		&a.NotifyTelegram, &a.NotifyMax, &a.NotifyMissed, &a.RecordMissed, &a.Notes,
		&a.Vendor, &a.UserID, &a.Username,
		&a.SwitchID, &a.SwitchPort, &a.SwitchName,
	)
	if err != nil {
		return nil, err
	}
	a.Driver = a.Kind.Driver()
	return &a, nil
}

// ListAccounts возвращает абонентов вместе с состоянием регистрации.
//
// Пароли не выбираются: список нужен для показа, а пароль отдаётся только
// в ответ на создание или изменение абонента.
func (r *SipRepo) ListAccounts(ctx context.Context) ([]domain.SipAccount, error) {
	rows, err := r.db.Query(ctx, `
		SELECT a.id, a.number, a.kind, a.display_name,
		       a.camera_id, a.controller_id, COALESCE(host(a.host), ''),
		       a.enabled, a.created_at, a.updated_at,
		       a.notify_telegram, a.notify_max, a.notify_missed, a.record_missed, a.notes,
		       a.vendor, a.user_id, COALESCE(u.username, ''),
		       `+sipAccountSwitch+`
		FROM sip_accounts a`+sipAccountJoins+`
		ORDER BY a.number`)
	if err != nil {
		return nil, fmt.Errorf("list sip accounts: %w", err)
	}
	defer rows.Close()

	accounts := make([]domain.SipAccount, 0)
	for rows.Next() {
		var a domain.SipAccount
		if err := rows.Scan(&a.ID, &a.Number, &a.Kind, &a.Name,
			&a.CameraID, &a.ControllerID, &a.Host,
			&a.Enabled, &a.CreatedAt, &a.UpdatedAt,
			&a.NotifyTelegram, &a.NotifyMax, &a.NotifyMissed, &a.RecordMissed, &a.Notes,
			&a.Vendor, &a.UserID, &a.Username,
			&a.SwitchID, &a.SwitchPort, &a.SwitchName); err != nil {
			return nil, fmt.Errorf("scan sip account: %w", err)
		}
		a.Driver = a.Kind.Driver()
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// GetAccount возвращает абонента вместе с паролем — это нужно сборщику
// конфигурации Asterisk.
func (r *SipRepo) GetAccount(ctx context.Context, id uuid.UUID) (*domain.SipAccount, error) {
	row := r.db.QueryRow(ctx, `
		SELECT `+sipAccountColumns+`,
		       `+sipAccountSwitch+`
		FROM sip_accounts a`+sipAccountJoins+`
		WHERE a.id = $1`, id)

	a, err := scanSipAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get sip account: %w", err)
	}
	return a, nil
}

// GetAccountByUser возвращает линию владельца — учётной записи сервера.
//
// Отдельный метод, а не поиск в списке: линия выдаётся сама при входе
// владельца, и на этот запрос смотрит каждое подключение приложения.
func (r *SipRepo) GetAccountByUser(ctx context.Context, userID uuid.UUID) (*domain.SipAccount, error) {
	row := r.db.QueryRow(ctx, `
		SELECT `+sipAccountColumns+`,
		       `+sipAccountSwitch+`
		FROM sip_accounts a`+sipAccountJoins+`
		WHERE a.user_id = $1`, userID)

	a, err := scanSipAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get sip account by user: %w", err)
	}
	return a, nil
}

// NextAppNumber ищет свободный номер для линии приложения.
//
// Диапазон 3XX закреплён за приложениями (см. extensions.conf: `_3XX` звонит
// через chan_pjsip), поэтому номер берётся только из него. Перебираем
// занятые номера, а не храним счётчик: удалённую учётную запись освободившийся
// номер должен достаться следующей, иначе номера уползают вверх.
func (r *SipRepo) NextAppNumber(ctx context.Context) (string, error) {
	rows, err := r.db.Query(ctx, `SELECT number FROM sip_accounts WHERE number LIKE '3%'`)
	if err != nil {
		return "", fmt.Errorf("read used sip numbers: %w", err)
	}
	defer rows.Close()

	used := map[string]bool{}
	for rows.Next() {
		var number string
		if err := rows.Scan(&number); err != nil {
			return "", fmt.Errorf("scan sip number: %w", err)
		}
		used[number] = true
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	// 300 занят браузерным приложением из первой версии страницы (номер
	// введён руками при проверке WebRTC), поэтому пользователям отдаём
	// начиная с 301.
	for n := 301; n <= 399; n++ {
		candidate := strconv.Itoa(n)
		if !used[candidate] {
			return candidate, nil
		}
	}
	return "", errors.New("свободных номеров для приложений не осталось (301–399)")
}

// DeleteAccountByUser убирает линию владельца вместе с его учётной записью.
func (r *SipRepo) DeleteAccountByUser(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM sip_accounts WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete sip account by user: %w", err)
	}
	return nil
}

// RenameUserLine меняет подпись линии вслед за именем владельца.
//
// Именно подпись, а не номер: номер выдан один раз и уже прописан в
// устройствах и на телефонах, а имя — только то, как линия видна в списке.
func (r *SipRepo) RenameUserLine(ctx context.Context, userID uuid.UUID, name string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE sip_accounts SET display_name = $2, updated_at = now() WHERE user_id = $1`,
		userID, name)
	if err != nil {
		return fmt.Errorf("rename sip account by user: %w", err)
	}
	return nil
}

// CreateAccount заводит абонента.
func (r *SipRepo) CreateAccount(ctx context.Context, a *domain.SipAccount) error {
	err := r.db.QueryRow(ctx, `
		INSERT INTO sip_accounts (number, password, kind, display_name,
		                          camera_id, controller_id, host, enabled,
		                          notify_telegram, notify_max, notify_missed, record_missed, notes, vendor,
		                          user_id)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::inet, $8, $9, $10, $11, $12, $13, $14, $15)
		RETURNING id, created_at, updated_at`,
		a.Number, a.Password, a.Kind, a.Name,
		a.CameraID, a.ControllerID, a.Host, a.Enabled,
		a.NotifyTelegram, a.NotifyMax, a.NotifyMissed, a.RecordMissed, a.Notes, a.Vendor,
		a.UserID).
		Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create sip account: %w", err)
	}
	a.Driver = a.Kind.Driver()
	return nil
}

// UpdateAccount изменяет абонента.
//
// Пароль меняется только если передан непустым: в интерфейсе он не
// показывается, и «пустое поле» означает «оставить прежний», а не «стереть».
func (r *SipRepo) UpdateAccount(ctx context.Context, a *domain.SipAccount) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE sip_accounts SET
			number = $2,
			kind = $3,
			display_name = $4,
			camera_id = $5,
			controller_id = $6,
			-- Пустой адрес означает «оставить прежний»: он подставляется сам
			-- из регистрации Asterisk, и правка любого другого поля карточки
			-- не должна его стирать.
			host = COALESCE(NULLIF($7, '')::inet, host),
			enabled = $8,
			password = CASE WHEN $9 = '' THEN password ELSE $9 END,
			notify_telegram = $10,
			notify_max = $11,
			notify_missed = $12,
			record_missed = $13,
			notes = $14,
			vendor = $15,
			user_id = $16,
			updated_at = now()
		WHERE id = $1`,
		a.ID, a.Number, a.Kind, a.Name,
		a.CameraID, a.ControllerID, a.Host, a.Enabled, a.Password,
		a.NotifyTelegram, a.NotifyMax, a.NotifyMissed, a.RecordMissed, a.Notes, a.Vendor, a.UserID)
	if err != nil {
		return fmt.Errorf("update sip account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	a.Driver = a.Kind.Driver()
	return nil
}

// DeleteAccount удаляет абонента. Участие в группах уходит каскадом.
func (r *SipRepo) DeleteAccount(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM sip_accounts WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete sip account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListGroups возвращает группы вместе с составом.
func (r *SipRepo) ListGroups(ctx context.Context) ([]domain.SipGroup, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, number, name, strategy, ring_seconds, enabled, created_at
		FROM sip_groups ORDER BY number, name`)
	if err != nil {
		return nil, fmt.Errorf("list sip groups: %w", err)
	}
	defer rows.Close()

	groups := make([]domain.SipGroup, 0)
	index := map[uuid.UUID]int{}
	for rows.Next() {
		var g domain.SipGroup
		if err := rows.Scan(&g.ID, &g.Number, &g.Name, &g.Strategy, &g.RingSeconds,
			&g.Enabled, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan sip group: %w", err)
		}
		g.Members = make([]domain.SipGroupMember, 0)
		index[g.ID] = len(groups)
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Состав читаем одним запросом на все группы: групп немного, а
	// отдельный запрос на каждую — лишние обращения к базе.
	members, err := r.db.Query(ctx, `
		SELECT m.group_id, m.account_id, a.number, a.display_name, a.kind, m.position
		FROM sip_group_members m
		JOIN sip_accounts a ON a.id = m.account_id
		ORDER BY m.group_id, m.position`)
	if err != nil {
		return nil, fmt.Errorf("list sip group members: %w", err)
	}
	defer members.Close()

	for members.Next() {
		var groupID uuid.UUID
		var m domain.SipGroupMember
		if err := members.Scan(&groupID, &m.AccountID, &m.Number, &m.Name,
			&m.Kind, &m.Position); err != nil {
			return nil, fmt.Errorf("scan sip group member: %w", err)
		}
		if i, ok := index[groupID]; ok {
			groups[i].Members = append(groups[i].Members, m)
		}
	}
	return groups, members.Err()
}

// CreateGroup создаёт группу вызова.
func (r *SipRepo) CreateGroup(ctx context.Context, g *domain.SipGroup) error {
	return translateGroupError(r.db.QueryRow(ctx, `
		INSERT INTO sip_groups (number, name, strategy, ring_seconds, enabled)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`,
		g.Number, g.Name, g.Strategy, g.RingSeconds, g.Enabled).
		Scan(&g.ID, &g.CreatedAt))
}

// translateGroupError объясняет нарушение уникальности номера группы.
func translateGroupError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrGroupNumberTaken
	}
	return err
}

// UpdateGroup изменяет группу.
func (r *SipRepo) UpdateGroup(ctx context.Context, g *domain.SipGroup) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE sip_groups SET number = $2, name = $3, strategy = $4, ring_seconds = $5, enabled = $6
		WHERE id = $1`,
		g.ID, g.Number, g.Name, g.Strategy, g.RingSeconds, g.Enabled)
	if err != nil {
		return fmt.Errorf("update sip group: %w", translateGroupError(err))
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteGroup удаляет группу. Правила и состав уходят каскадом.
func (r *SipRepo) DeleteGroup(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM sip_groups WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete sip group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetGroupMembers заменяет состав группы целиком.
//
// Именно заменяет, а не добавляет: интерфейс присылает итоговый список
// (галочки у абонентов), и такой подход избавляет от разбора «кого убрали».
// В одной транзакции, чтобы не осталось группы без состава при сбое.
func (r *SipRepo) SetGroupMembers(ctx context.Context, groupID uuid.UUID, accountIDs []uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // откат после успешного коммита безвреден

	if _, err := tx.Exec(ctx, `DELETE FROM sip_group_members WHERE group_id = $1`, groupID); err != nil {
		return fmt.Errorf("clear members: %w", err)
	}

	for i, accountID := range accountIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO sip_group_members (group_id, account_id, position)
			VALUES ($1, $2, $3)`, groupID, accountID, i); err != nil {
			return fmt.Errorf("add member: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ListRules возвращает правила вместе с номерами источника и группы —
// в интерфейсе оператор оперирует номерами и названиями, а не uuid.
func (r *SipRepo) ListRules(ctx context.Context) ([]domain.SipRule, error) {
	rows, err := r.db.Query(ctx, `
		SELECT r.id, r.source_account_id, COALESCE(a.number, ''),
		       r.dialed_number, r.group_id, g.name, r.enabled, r.created_at
		FROM sip_rules r
		LEFT JOIN sip_accounts a ON a.id = r.source_account_id
		JOIN sip_groups g ON g.id = r.group_id
		ORDER BY r.dialed_number`)
	if err != nil {
		return nil, fmt.Errorf("list sip rules: %w", err)
	}
	defer rows.Close()

	rules := make([]domain.SipRule, 0)
	for rows.Next() {
		var rule domain.SipRule
		if err := rows.Scan(&rule.ID, &rule.SourceAccountID, &rule.SourceNumber,
			&rule.DialedNumber, &rule.GroupID, &rule.GroupName,
			&rule.Enabled, &rule.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan sip rule: %w", err)
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// CreateRule заводит правило вызова.
func (r *SipRepo) CreateRule(ctx context.Context, rule *domain.SipRule) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO sip_rules (source_account_id, dialed_number, group_id, enabled)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`,
		rule.SourceAccountID, rule.DialedNumber, rule.GroupID, rule.Enabled).
		Scan(&rule.ID, &rule.CreatedAt)
}

// UpdateRule изменяет правило.
func (r *SipRepo) UpdateRule(ctx context.Context, rule *domain.SipRule) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE sip_rules SET source_account_id = $2, dialed_number = $3,
		                     group_id = $4, enabled = $5
		WHERE id = $1`,
		rule.ID, rule.SourceAccountID, rule.DialedNumber, rule.GroupID, rule.Enabled)
	if err != nil {
		return fmt.Errorf("update sip rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRule удаляет правило.
func (r *SipRepo) DeleteRule(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM sip_rules WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete sip rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SipSnapshot — полный набор данных для сборки конфигурации Asterisk.
type SipSnapshot struct {
	Accounts []domain.SipAccount
	Groups   []domain.SipGroup
	Rules    []domain.SipRule
}

// Snapshot собирает всё, что нужно сборщику конфигурации.
//
// Читается единым набором запросов, а не отдельными вызовами из сборщика:
// конфигурация должна соответствовать одному состоянию базы, иначе можно
// собрать sip.conf по одному набору абонентов, а dialplan — по другому.
func (r *SipRepo) Snapshot(ctx context.Context) (*SipSnapshot, error) {
	accounts, err := r.listEnabledAccountsWithSecrets(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := r.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := r.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	return &SipSnapshot{Accounts: accounts, Groups: groups, Rules: rules}, nil
}

// listEnabledAccountsWithSecrets читает включённых абонентов вместе с
// паролями — только для сборки конфигурации.
func (r *SipRepo) listEnabledAccountsWithSecrets(ctx context.Context) ([]domain.SipAccount, error) {
	// Привязку к коммутатору здесь не читаем: она не нужна для сборки
	// конфигурации Asterisk, а лишний JOIN замедлял бы старт сервера.
	// Поля уведомлений — тоже: в конфигурации им места нет.
	rows, err := r.db.Query(ctx, `
		SELECT a.id, a.number, a.password, a.kind, a.display_name,
		       a.camera_id, a.controller_id, COALESCE(host(a.host), ''),
		       a.enabled, a.created_at, a.updated_at,
		       a.notify_telegram, a.notify_max, a.notify_missed, a.record_missed, a.notes,
		       a.vendor, a.user_id, ''::text,
		       NULL::uuid, 0::smallint, ''::text
		FROM sip_accounts a
		WHERE a.enabled
		ORDER BY a.number`)
	if err != nil {
		return nil, fmt.Errorf("list enabled sip accounts: %w", err)
	}
	defer rows.Close()

	accounts := make([]domain.SipAccount, 0)
	for rows.Next() {
		a, err := scanSipAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sip account: %w", err)
		}
		accounts = append(accounts, *a)
	}
	return accounts, rows.Err()
}
