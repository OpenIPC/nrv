package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
)

// Ошибки хранилища коммутаторов.
var (
	// ErrSwitchNotFound — коммутатор не найден по идентификатору.
	ErrSwitchNotFound = errors.New("коммутатор не найден")
	// ErrSwitchExists — коммутатор с таким серийным номером уже добавлен.
	// Отдельная ошибка нужна, потому что при повторном поиске устройство
	// находится снова, и это не должно выглядеть как сбой.
	ErrSwitchExists = errors.New("коммутатор уже добавлен")
)

// SwitchRepo — хранилище PoE-коммутаторов, их портов и привязок камер.
type SwitchRepo struct {
	db *pgxpool.Pool
}

func NewSwitchRepo(db *pgxpool.Pool) *SwitchRepo {
	return &SwitchRepo{db: db}
}

// switchColumns — колонки коммутатора в порядке чтения.
//
// Вынесены в константу, потому что повторяются в нескольких запросах.
// При добавлении поля достаточно поправить здесь и в scanSwitch, иначе
// поле легко забыть в одном из мест, и коммутатор читался бы неполным.
const switchColumns = `id, sn, mac, ip, model, firmware, name, location,
	port_count, ports_reversed, password, online, last_seen_at, last_error,
	voltage, temperature, created_at, updated_at`

// scanSwitch читает строку коммутатора.
//
// voltage и temperature хранятся отдельными столбцами, хотя приходят в
// составе общего ответа: они нужны в списке и на дашборде, а разбирать
// ради двух чисел весь JSON на каждом кадре интерфейса расточительно.
func scanSwitch(row interface {
	Scan(dest ...any) error
}) (domain.Switch, error) {
	var s domain.Switch
	err := row.Scan(&s.ID, &s.SN, &s.MAC, &s.IP, &s.Model, &s.Firmware,
		&s.Name, &s.Location, &s.PortCount, &s.PortsReversed, &s.Password,
		&s.Online, &s.LastSeenAt, &s.LastError, &s.Voltage, &s.Temperature,
		&s.CreatedAt, &s.UpdatedAt)
	// Наличие пароля вычисляем здесь, а не в базе: булево поле в выборке
	// было бы лишним столбцом ради одного бита, а решение лежит ровно в
	// одном месте.
	s.HasPassword = s.Password != ""
	return s, err
}

// portColumns — колонки порта в порядке чтения.
const portColumns = `id, switch_id, port_number, link_up, speed_mbps,
	poe_enabled, poe_watts, poe_capable, is_uplink, extend_mode, isolated,
	tx_mb, rx_mb, last_power_cycle_at, updated_at`

// scanPort читает строку порта.
func scanPort(row interface {
	Scan(dest ...any) error
}) (domain.SwitchPort, error) {
	var p domain.SwitchPort
	err := row.Scan(&p.ID, &p.SwitchID, &p.PortNumber, &p.LinkUp, &p.SpeedMbps,
		&p.PoeEnabled, &p.PoeWatts, &p.PoeCapable, &p.IsUplink, &p.ExtendMode, &p.Isolated,
		&p.TxMB, &p.RxMB, &p.LastPowerCycleAt, &p.UpdatedAt)
	fillPowerControl(&p)
	return p, err
}

// fillPowerControl вычисляет, можно ли управлять питанием порта.
//
// Правило собрано в одном месте, потому что применяется и в списке
// портов, и в карточке камеры: разойдись оно, и интерфейс предлагал бы
// кнопку там, где сервер откажет.
func fillPowerControl(p *domain.SwitchPort) {
	switch {
	case !p.PoeCapable:
		p.CanControlPower = false
		p.PowerControlNote = "порт не поддерживает питание PoE"
	case p.IsUplink:
		// Запрет на выключение транзитного порта — не перестраховка:
		// через него идёт канал связи с сервером, и сняв питание, мы
		// потеряем управление всем коммутатором, включая возможность
		// включить питание обратно.
		p.CanControlPower = false
		p.PowerControlNote = "транзитный порт: отключение лишит связи весь коммутатор"
	default:
		p.CanControlPower = true
	}
}

// ListSwitches возвращает коммутаторы без портов.
//
// Порты не читаем: список нужен для обзора, а полная таблица портов —
// только в карточке. На объекте с несколькими коммутаторами это заметная
// разница в объёме ответа.
func (r *SwitchRepo) ListSwitches(ctx context.Context) ([]domain.Switch, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+switchColumns+`,
		        (SELECT count(*) FROM camera_switch_port csp
		          WHERE csp.switch_id = switches.id) AS camera_count
		   FROM switches
		  ORDER BY name, model, sn`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]domain.Switch, 0)
	for rows.Next() {
		s, err := scanSwitchWithCameraCount(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// scanSwitchWithCameraCount читает коммутатор вместе со счётчиком камер.
func scanSwitchWithCameraCount(row interface {
	Scan(dest ...any) error
}) (domain.Switch, error) {
	var s domain.Switch
	err := row.Scan(&s.ID, &s.SN, &s.MAC, &s.IP, &s.Model, &s.Firmware,
		&s.Name, &s.Location, &s.PortCount, &s.PortsReversed, &s.Password,
		&s.Online, &s.LastSeenAt, &s.LastError, &s.Voltage, &s.Temperature,
		&s.CreatedAt, &s.UpdatedAt, &s.CameraCount)
	s.HasPassword = s.Password != ""
	return s, err
}

// GetSwitch возвращает коммутатор целиком: с портами и привязками камер.
func (r *SwitchRepo) GetSwitch(ctx context.Context, id uuid.UUID) (*domain.Switch, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+switchColumns+`,
		        (SELECT count(*) FROM camera_switch_port csp
		          WHERE csp.switch_id = switches.id) AS camera_count,
		        detail
		   FROM switches WHERE id = $1`, id)

	var s domain.Switch
	var detail []byte
	err := row.Scan(&s.ID, &s.SN, &s.MAC, &s.IP, &s.Model, &s.Firmware,
		&s.Name, &s.Location, &s.PortCount, &s.PortsReversed, &s.Password,
		&s.Online, &s.LastSeenAt, &s.LastError, &s.Voltage, &s.Temperature,
		&s.CreatedAt, &s.UpdatedAt, &s.CameraCount, &detail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSwitchNotFound
	}
	if err != nil {
		return nil, err
	}
	s.HasPassword = s.Password != ""

	if len(detail) > 0 {
		var m map[string]any
		if err := json.Unmarshal(detail, &m); err == nil {
			s.Detail = m
		}
		// Ошибка разбора не прерывает чтение: состояние портов и привязки
		// важнее полного ответа устройства, и из-за повреждённого JSON
		// оператор не должен терять доступ к управлению питанием.
	}

	ports, err := r.ListPorts(ctx, id)
	if err != nil {
		return nil, err
	}
	s.Ports = ports
	return &s, nil
}

// ListPorts возвращает порты коммутатора вместе с привязанными камерами.
//
// Привязка подтягивается соединением, а не отдельным запросом на каждый
// порт: портов на коммутаторе до 24, и построчные запросы превратились бы
// в десятки обращений к базе на одно открытие карточки.
func (r *SwitchRepo) ListPorts(ctx context.Context, switchID uuid.UUID) ([]domain.SwitchPort, error) {
	rows, err := r.db.Query(ctx,
		`SELECT p.id, p.switch_id, p.port_number, p.link_up, p.speed_mbps,
		        p.poe_enabled, p.poe_watts, p.poe_capable, p.is_uplink, p.extend_mode, p.isolated,
		        p.tx_mb, p.rx_mb, p.last_power_cycle_at, p.updated_at,
		        c.id, c.name, c.status = 'online'
		   FROM switch_ports p
		   LEFT JOIN camera_switch_port csp
		          ON csp.switch_id = p.switch_id AND csp.port_number = p.port_number
		   LEFT JOIN cameras c ON c.id = csp.camera_id
		  WHERE p.switch_id = $1
		  ORDER BY p.port_number`, switchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ports := make([]domain.SwitchPort, 0)
	for rows.Next() {
		var p domain.SwitchPort
		var camID *uuid.UUID
		var camName *string
		var camOnline *bool
		err := rows.Scan(&p.ID, &p.SwitchID, &p.PortNumber, &p.LinkUp, &p.SpeedMbps,
			&p.PoeEnabled, &p.PoeWatts, &p.PoeCapable, &p.IsUplink, &p.ExtendMode, &p.Isolated,
			&p.TxMB, &p.RxMB, &p.LastPowerCycleAt, &p.UpdatedAt,
			&camID, &camName, &camOnline)
		if err != nil {
			return nil, err
		}
		fillPowerControl(&p)
		if camID != nil {
			p.CameraID = camID
			if camName != nil {
				p.CameraName = *camName
			}
			if camOnline != nil {
				p.CameraOnline = *camOnline
			}
		}
		ports = append(ports, p)
	}
	return ports, rows.Err()
}

// GetPort возвращает один порт коммутатора.
func (r *SwitchRepo) GetPort(ctx context.Context, switchID uuid.UUID, portNumber int) (*domain.SwitchPort, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+portColumns+` FROM switch_ports
		  WHERE switch_id = $1 AND port_number = $2`, switchID, portNumber)

	p, err := scanPort(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("порт %d не найден", portNumber)
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpsertSwitch создаёт или обновляет коммутатор по серийному номеру.
//
// Обновление по SN, а не по идентификатору: коммутатор обнаруживается
// поиском до того, как о нём что-либо известно, и SN — единственное
// устойчивое поле. Адрес и модель при этом перезаписываются, а имя и
// расположение, заданные оператором, сохраняются: их потеря при каждом
// опросе была бы недопустима.
func (r *SwitchRepo) UpsertSwitch(ctx context.Context, s *domain.Switch) (uuid.UUID, error) {
	// Пустой объект вместо nil: столбец объявлен NOT NULL, а Go передаёт
	// неинициализированную карту как JSON-значение null, и вставка падала
	// бы на ограничении. Пустой объект честнее отсутствия значения —
	// «ответа ещё не было» против «ответ не сохранён».
	detail := s.Detail
	if detail == nil {
		detail = map[string]any{}
	}

	var id uuid.UUID
	err := r.db.QueryRow(ctx,
		`INSERT INTO switches (sn, mac, ip, model, firmware, name, location, password,
		                       port_count, ports_reversed, online, last_seen_at,
		                       last_error, voltage, temperature, detail)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		 ON CONFLICT (sn) DO UPDATE SET
		     mac = EXCLUDED.mac,
		     ip = EXCLUDED.ip,
		     model = CASE WHEN EXCLUDED.model <> '' THEN EXCLUDED.model ELSE switches.model END,
		     firmware = EXCLUDED.firmware,
		     port_count = EXCLUDED.port_count,
		     online = EXCLUDED.online,
		     last_seen_at = EXCLUDED.last_seen_at,
		     last_error = EXCLUDED.last_error,
		     voltage = EXCLUDED.voltage,
		     temperature = EXCLUDED.temperature,
		     detail = EXCLUDED.detail,
		     updated_at = now()
		 RETURNING id`,
		s.SN, s.MAC, s.IP, s.Model, s.Firmware, s.Name, s.Location, s.Password,
		s.PortCount, s.PortsReversed, s.Online, s.LastSeenAt,
		s.LastError, s.Voltage, s.Temperature, detail).Scan(&id)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// UpdateSwitchMeta сохраняет поля, которые задаёт оператор.
//
// Отдельный метод, потому что эти поля не должны перезаписываться при
// опросе: имя и расположение задаёт человек, и синхронизация состояния не
// имеет права их затирать.
func (r *SwitchRepo) UpdateSwitchMeta(ctx context.Context, id uuid.UUID, name, location string, portsReversed bool) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE switches SET name = $2, location = $3, ports_reversed = $4, updated_at = now()
		  WHERE id = $1`, id, name, location, portsReversed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSwitchNotFound
	}
	return nil
}

// DeleteSwitch удаляет коммутатор вместе с портами и привязками.
//
// Привязки камер снимаются каскадом: связь «камера — порт» описывает
// физическое подключение к конкретному устройству, и после его удаления
// она теряет смысл. Сами камеры при этом остаются — они не являются
// частью коммутатора.
func (r *SwitchRepo) DeleteSwitch(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM switches WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSwitchNotFound
	}
	return nil
}

// GetSwitchBySN находит коммутатор по серийному номеру.
func (r *SwitchRepo) GetSwitchBySN(ctx context.Context, sn string) (*domain.Switch, error) {
	row := r.db.QueryRow(ctx, `SELECT `+switchColumns+` FROM switches WHERE sn = $1`, sn)
	s, err := scanSwitch(row)
	// pgx.Row не является error: ошибку отсутствия строки отдаёт Scan,
	// поэтому проверять её нужно здесь, а не у результата QueryRow.
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSwitchNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}
// SavePorts сохраняет состояние портов, полученное при опросе.
//
// Работает в транзакции: порт, обновлённый наполовину, выглядел бы как
// исправный, и на его основании можно было бы принять неверное решение о
// перезагрузке. Частичное состояние хуже отсутствия состояния.
//
// Число портов может уменьшиться, если модель отдала меньше данных —
// тогда лишние строки удаляются, чтобы в интерфейсе не оставалось портов,
// которых на устройстве нет.
func (r *SwitchRepo) SavePorts(ctx context.Context, switchID uuid.UUID, ports []domain.SwitchPort) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, p := range ports {
		_, err := tx.Exec(ctx,
			`INSERT INTO switch_ports (switch_id, port_number, link_up, speed_mbps,
			                           poe_enabled, poe_watts, poe_capable, is_uplink,
			                           extend_mode, isolated, tx_mb, rx_mb, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
			 ON CONFLICT (switch_id, port_number) DO UPDATE SET
			     link_up = EXCLUDED.link_up,
			     speed_mbps = EXCLUDED.speed_mbps,
			     poe_enabled = EXCLUDED.poe_enabled,
			     poe_watts = EXCLUDED.poe_watts,
			     poe_capable = EXCLUDED.poe_capable,
			     is_uplink = EXCLUDED.is_uplink,
			     extend_mode = EXCLUDED.extend_mode,
			     isolated = EXCLUDED.isolated,
			     tx_mb = EXCLUDED.tx_mb,
			     rx_mb = EXCLUDED.rx_mb,
			     updated_at = now()`,
			switchID, p.PortNumber, p.LinkUp, p.SpeedMbps,
			p.PoeEnabled, p.PoeWatts, p.PoeCapable, p.IsUplink, p.ExtendMode,
			p.Isolated, p.TxMB, p.RxMB)
		if err != nil {
			return err
		}
	}

	// Порта с номером больше, чем сообщило устройство, на нём уже нет.
	// last_power_cycle_at при этом не трогаем — история действий должна
	// переживать пропажу порта из ответа: питание могли снять именно
	// из-за неисправности.
	if len(ports) > 0 {
		maxPort := 0
		for _, p := range ports {
			if p.PortNumber > maxPort {
				maxPort = p.PortNumber
			}
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM switch_ports WHERE switch_id = $1 AND port_number > $2`,
			switchID, maxPort); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// MarkPowerCycle отмечает, что на порту выполнена перезагрузка питания.
func (r *SwitchRepo) MarkPowerCycle(ctx context.Context, switchID uuid.UUID, portNumber int) error {
	_, err := r.db.Exec(ctx,
		`UPDATE switch_ports SET last_power_cycle_at = now()
		  WHERE switch_id = $1 AND port_number = $2`, switchID, portNumber)
	return err
}

// SetPortState обновляет состояние одного порта после команды.
//
// Нужно, чтобы интерфейс сразу показал результат, не дожидаясь
// следующего опроса: коммутатор опрашивается раз в минуту, и всё это
// время оператор видел бы старое состояние и мог решить, что команда не
// сработала.
func (r *SwitchRepo) SetPortState(ctx context.Context, switchID uuid.UUID, portNumber int, poeEnabled, extendMode bool) error {
	_, err := r.db.Exec(ctx,
		`UPDATE switch_ports SET poe_enabled = $3, extend_mode = $4, updated_at = now()
		  WHERE switch_id = $1 AND port_number = $2`,
		switchID, portNumber, poeEnabled, extendMode)
	return err
}

// LogEvent записывает действие в журнал.
func (r *SwitchRepo) LogEvent(ctx context.Context, e *domain.SwitchPortEvent) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO switch_port_events (switch_id, switch_sn, port_number,
		                                 action, result, message, actor)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.SwitchID, e.SwitchSN, e.PortNumber, e.Action, e.Result, e.Message, e.Actor)
	return err
}

// ListEvents возвращает журнал действий с портами.
//
// Ограничение выборки здесь обязательно: журнал растёт с каждым действием,
// и без предела страница со временем перестала бы открываться.
func (r *SwitchRepo) ListEvents(ctx context.Context, switchID *uuid.UUID, limit int) ([]domain.SwitchPortEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := `SELECT e.id, e.switch_id, e.switch_sn, COALESCE(s.name, ''), e.port_number,
	                 COALESCE(c.name, ''), e.action, e.result, e.message, e.actor, e.created_at
	            FROM switch_port_events e
	            LEFT JOIN switches s ON s.id = e.switch_id
	            LEFT JOIN camera_switch_port csp
	                   ON csp.switch_id = e.switch_id AND csp.port_number = e.port_number
	            LEFT JOIN cameras c ON c.id = csp.camera_id`
	args := []any{}
	if switchID != nil {
		query += ` WHERE e.switch_id = $1`
		args = append(args, *switchID)
	}
	query += ` ORDER BY e.created_at DESC LIMIT ` + fmt.Sprintf("%d", limit)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]domain.SwitchPortEvent, 0)
	for rows.Next() {
		var e domain.SwitchPortEvent
		if err := rows.Scan(&e.ID, &e.SwitchID, &e.SwitchSN, &e.SwitchName,
			&e.PortNumber, &e.CameraName, &e.Action, &e.Result,
			&e.Message, &e.Actor, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// BindCamera привязывает камеру к порту коммутатора.
//
// Порт обязан существовать. Проверка не формальность: привязка к
// несуществующему порту привела бы к тому, что перезагрузка ушла бы «в
// пустоту», а оператор считал бы камеру перезагруженной и ждал результата.
func (r *SwitchRepo) BindCamera(ctx context.Context, cameraID, switchID uuid.UUID, portNumber int) error {
	var exists bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM switch_ports WHERE switch_id = $1 AND port_number = $2)`,
		switchID, portNumber).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("порт %d не найден на коммутаторе", portNumber)
	}

	_, err = r.db.Exec(ctx,
		`INSERT INTO camera_switch_port (camera_id, switch_id, port_number)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (camera_id) DO UPDATE SET
		     switch_id = EXCLUDED.switch_id,
		     port_number = EXCLUDED.port_number,
		     updated_at = now()`,
		cameraID, switchID, portNumber)
	return err
}

// UnbindCamera снимает привязку камеры к порту.
func (r *SwitchRepo) UnbindCamera(ctx context.Context, cameraID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM camera_switch_port WHERE camera_id = $1`, cameraID)
	return err
}

// CameraPortLink — сведения о подключении камеры.
type CameraPortLink struct {
	SwitchID   uuid.UUID `json:"switch_id"`
	SwitchName string    `json:"switch_name"`
	SwitchSN   string    `json:"switch_sn"`
	SwitchIP   string    `json:"switch_ip"`
	SwitchModel string   `json:"switch_model"`
	PortNumber int       `json:"port_number"`
	// Состояние порта: нужно в карточке камеры, чтобы сразу показать,
	// есть ли питание и линк — это и есть ответ на вопрос «камера
	// действительно зависла или у неё отвалился кабель».
	LinkUp     bool    `json:"link_up"`
	PoeEnabled bool    `json:"poe_enabled"`
	PoeWatts   float64 `json:"poe_watts"`
	PoeCapable bool    `json:"poe_capable"`
	SpeedMbps  int     `json:"speed_mbps"`
	SwitchOnline bool  `json:"switch_online"`
}

// CameraLink возвращает подключение камеры к порту коммутатора.
//
// Возвращает nil, если привязки нет: это не ошибка, а обычное состояние —
// камера может быть подключена напрямую или через неуправляемый
// коммутатор.
func (r *SwitchRepo) CameraLink(ctx context.Context, cameraID uuid.UUID) (*CameraPortLink, error) {
	row := r.db.QueryRow(ctx,
		`SELECT s.id, s.name, s.sn, s.ip, s.model, csp.port_number,
		        COALESCE(p.link_up, false), COALESCE(p.poe_enabled, false),
		        COALESCE(p.poe_watts, 0), COALESCE(p.poe_capable, false),
		        COALESCE(p.speed_mbps, 0), s.online
		   FROM camera_switch_port csp
		   JOIN switches s ON s.id = csp.switch_id
		   LEFT JOIN switch_ports p
		          ON p.switch_id = csp.switch_id AND p.port_number = csp.port_number
		  WHERE csp.camera_id = $1`, cameraID)

	var l CameraPortLink
	err := row.Scan(&l.SwitchID, &l.SwitchName, &l.SwitchSN, &l.SwitchIP, &l.SwitchModel,
		&l.PortNumber, &l.LinkUp, &l.PoeEnabled, &l.PoeWatts, &l.PoeCapable,
		&l.SpeedMbps, &l.SwitchOnline)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// CameraLinks возвращает подключения для набора камер.
//
// Пакетное чтение нужно для списка камер: запрос на каждую камеру в списке
// из сотни дал бы сотню обращений к базе на одно открытие страницы.
func (r *SwitchRepo) CameraLinks(ctx context.Context, cameraIDs []uuid.UUID) (map[uuid.UUID]*CameraPortLink, error) {
	out := map[uuid.UUID]*CameraPortLink{}
	if len(cameraIDs) == 0 {
		return out, nil
	}

	rows, err := r.db.Query(ctx,
		`SELECT csp.camera_id, s.id, s.name, s.sn, s.ip, s.model, csp.port_number,
		        COALESCE(p.link_up, false), COALESCE(p.poe_enabled, false),
		        COALESCE(p.poe_watts, 0), COALESCE(p.poe_capable, false),
		        COALESCE(p.speed_mbps, 0), s.online
		   FROM camera_switch_port csp
		   JOIN switches s ON s.id = csp.switch_id
		   LEFT JOIN switch_ports p
		          ON p.switch_id = csp.switch_id AND p.port_number = csp.port_number
		  WHERE csp.camera_id = ANY($1)`, cameraIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var camID uuid.UUID
		var l CameraPortLink
		if err := rows.Scan(&camID, &l.SwitchID, &l.SwitchName, &l.SwitchSN, &l.SwitchIP,
			&l.SwitchModel, &l.PortNumber, &l.LinkUp, &l.PoeEnabled, &l.PoeWatts,
			&l.PoeCapable, &l.SpeedMbps, &l.SwitchOnline); err != nil {
			return nil, err
		}
		link := l
		out[camID] = &link
	}
	return out, rows.Err()
}

// SetPassword сохраняет пароль коммутатора.
//
// Отдельный метод, а не часть общего обновления: пароль задаёт оператор,
// и синхронизация состояния не должна его затирать. Пустое значение
// допустимо — так снимается пароль у открытых моделей.
func (r *SwitchRepo) SetPassword(ctx context.Context, id uuid.UUID, password string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE switches SET password = $2, updated_at = now() WHERE id = $1`, id, password)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSwitchNotFound
	}
	return nil
}

// ListOnlineSwitches возвращает коммутаторы, доступные для опроса.
func (r *SwitchRepo) ListOnlineSwitches(ctx context.Context) ([]domain.Switch, error) {
	return r.ListSwitches(ctx)
}

// CountSwitchPorts возвращает число привязанных камер на коммутаторе.
func (r *SwitchRepo) CountCameras(ctx context.Context, switchID uuid.UUID) (int, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM camera_switch_port WHERE switch_id = $1`, switchID).Scan(&n)
	return n, err
}
