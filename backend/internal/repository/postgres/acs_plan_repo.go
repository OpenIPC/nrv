package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
)

// ACSPlanRepo — хранилище планов помещений и расстановки устройств.
type ACSPlanRepo struct {
	db *pgxpool.Pool
}

func NewACSPlanRepo(db *pgxpool.Pool) *ACSPlanRepo {
	return &ACSPlanRepo{db: db}
}

// planColumns — список колонок плана в порядке чтения.
//
// Вынесен в константу, потому что список повторяется в трёх запросах:
// при добавлении поля достаточно поправить здесь и в scanPlan, иначе
// новое поле легко забыть в одном месте, и план читался бы неполным.
const planColumns = `id, name, description, image_path, sort_order, created_at, updated_at`

func scanPlan(row interface {
	Scan(dest ...any) error
}) (domain.ACSPlan, error) {
	var p domain.ACSPlan
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.ImagePath,
		&p.SortOrder, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// ListPlans возвращает планы без расстановки.
//
// Точки не читаем: в списке они не нужны, а на объекте с десятком этажей
// это лишние сотни строк при каждом открытии страницы.
func (r *ACSPlanRepo) ListPlans(ctx context.Context) ([]domain.ACSPlan, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+planColumns+` FROM acs_plans ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	plans := make([]domain.ACSPlan, 0)
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

// GetPlan возвращает план вместе с точками.
func (r *ACSPlanRepo) GetPlan(ctx context.Context, id uuid.UUID) (*domain.ACSPlan, error) {
	p, err := scanPlan(r.db.QueryRow(ctx,
		`SELECT `+planColumns+` FROM acs_plans WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrPlanNotFound
		}
		return nil, err
	}

	points, err := r.ListPoints(ctx, id)
	if err != nil {
		return nil, err
	}
	p.Points = points
	return &p, nil
}

// SavePlan создаёт или обновляет план.
//
// Подложку не трогает: изображение загружается отдельным запросом, после
// того как план создан. Иначе при неудачной загрузке остался бы план без
// имени — сначала запись, потом файл.
func (r *ACSPlanRepo) SavePlan(ctx context.Context, p *domain.ACSPlan) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}

	return r.db.QueryRow(ctx, `
		INSERT INTO acs_plans (id, name, description, sort_order)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE
		SET name = EXCLUDED.name,
		    description = EXCLUDED.description,
		    sort_order = EXCLUDED.sort_order,
		    updated_at = now()
		RETURNING created_at, updated_at, image_path
	`, p.ID, p.Name, p.Description, p.SortOrder).Scan(&p.CreatedAt, &p.UpdatedAt, &p.ImagePath)
}

// SetPlanImage записывает путь к подложке.
//
// Отдельный метод, а не поле в SavePlan: изображение загружается уже после
// создания плана, и передавать его каждый раз вместе с формой значило бы
// затирать картинку при правке одного названия.
func (r *ACSPlanRepo) SetPlanImage(ctx context.Context, id uuid.UUID, path string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE acs_plans SET image_path = $2, updated_at = now() WHERE id = $1`, id, path)
	return err
}

// DeletePlan удаляет план вместе с точками.
//
// Точки уходят каскадом по внешнему ключу. Ошибку удаления подложки из
// хранилища не возвращаем: файл мог быть недоступен, а план удалить всё
// равно нужно, иначе он останется висеть в списке.
func (r *ACSPlanRepo) DeletePlan(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM acs_plans WHERE id = $1`, id)
	return err
}

// pointColumns — список колонок точки в порядке чтения.
const pointColumns = `id, plan_id, kind, device_id, x, y, rotation, label`

func scanPoint(row interface {
	Scan(dest ...any) error
}) (domain.ACSPlanPoint, error) {
	var p domain.ACSPlanPoint
	err := row.Scan(&p.ID, &p.PlanID, &p.Kind, &p.DeviceID, &p.X, &p.Y,
		&p.Rotation, &p.Label)
	return p, err
}

// ListPoints возвращает точки плана.
func (r *ACSPlanRepo) ListPoints(ctx context.Context, planID uuid.UUID) ([]domain.ACSPlanPoint, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+pointColumns+` FROM acs_plan_points
		 WHERE plan_id = $1
		 ORDER BY kind, label`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	points := make([]domain.ACSPlanPoint, 0)
	for rows.Next() {
		p, err := scanPoint(rows)
		if err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// GetPoint возвращает одну точку.
func (r *ACSPlanRepo) GetPoint(ctx context.Context, id uuid.UUID) (*domain.ACSPlanPoint, error) {
	p, err := scanPoint(r.db.QueryRow(ctx,
		`SELECT `+pointColumns+` FROM acs_plan_points WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrPlanPointNotFound
		}
		return nil, err
	}
	return &p, nil
}

// SavePoint создаёт или обновляет точку на плане.
//
// Конфликт по (plan_id, kind, device_id) разрешаем обновлением: повторное
// перетаскивание того же устройства на плане должно двигать существующую
// точку, а не создавать вторую. Иначе двойной клик по кнопке «добавить»
// оставил бы на плане две точки в одном месте.
func (r *ACSPlanRepo) SavePoint(ctx context.Context, p *domain.ACSPlanPoint) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}

	var deviceID any
	if p.DeviceID != nil && *p.DeviceID != uuid.Nil {
		deviceID = *p.DeviceID
	} else {
		// Пустой идентификатор означает точку без устройства: например,
		// метку «пост охраны». Сохраняем как NULL, а не как нулевой UUID:
		// иначе уникальный индекс посчитал бы все такие точки одной.
		deviceID = nil
	}

	return r.db.QueryRow(ctx, `
		INSERT INTO acs_plan_points (id, plan_id, kind, device_id, x, y, rotation, label)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (plan_id, kind, device_id) WHERE device_id IS NOT NULL
		DO UPDATE
		SET x = EXCLUDED.x,
		    y = EXCLUDED.y,
		    rotation = EXCLUDED.rotation,
		    label = EXCLUDED.label
		RETURNING id
	`, p.ID, p.PlanID, p.Kind, deviceID, p.X, p.Y, p.Rotation, p.Label).Scan(&p.ID)
}

// DeletePoint убирает точку с плана.
func (r *ACSPlanRepo) DeletePoint(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM acs_plan_points WHERE id = $1`, id)
	return err
}

// DeletePointsForDevice убирает точки удалённого устройства со всех планов.
//
// Вызывается при удалении камеры, двери или контроллера. Без этого точки
// остались бы на схемах и показывали состояние несуществующего устройства,
// а оператор искал бы причину в оборудовании.
func (r *ACSPlanRepo) DeletePointsForDevice(ctx context.Context, deviceID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM acs_plan_points WHERE device_id = $1`, deviceID)
	return err
}

// DeviceExists проверяет, существует ли устройство указанного вида.
//
// Экспортированная обёртка: сервису нужно убедиться, что устройство
// существует, до создания точки. Иначе на плане появился бы значок,
// всегда показывающий «нет связи», и оператор искал бы неисправность
// в исправном оборудовании.
func (r *ACSPlanRepo) DeviceExists(ctx context.Context, kind domain.ACSPlanPointKind, id uuid.UUID) (bool, error) {
	return r.deviceExists(ctx, kind, id)
}

// DoorsByIDs читает двери по списку идентификаторов.
//
// Нужен плану помещений: дверь на плане показывается вместе с состоянием
// контроллера, к которому подключена, а это требует её проекции.
// Выборка по списку, а не по одной: на плане большого объекта десятки
// дверей, и запрос на каждую превратил бы отрисовку в серию обращений.
func (r *ACSPlanRepo) DoorsByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.ACSDoor, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	rows, err := r.db.Query(ctx, `
		SELECT id, controller_id, name, direction, location, enabled, created_at
		FROM acs_doors
		WHERE id = ANY($1)
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	doors := make([]domain.ACSDoor, 0, len(ids))
	for rows.Next() {
		var d domain.ACSDoor
		if err := rows.Scan(&d.ID, &d.ControllerID, &d.Name, &d.Direction,
			&d.Location, &d.Enabled, &d.CreatedAt); err != nil {
			return nil, err
		}
		doors = append(doors, d)
	}
	return doors, rows.Err()
}

// deviceExists проверяет, существует ли устройство указанного вида.
func (r *ACSPlanRepo) deviceExists(ctx context.Context, kind domain.ACSPlanPointKind, id uuid.UUID) (bool, error) {
	var table string
	switch kind {
	case domain.PlanPointCamera:
		table = "cameras"
	case domain.PlanPointDoor:
		table = "acs_doors"
	case domain.PlanPointController:
		table = "acs_controllers"
	case domain.PlanPointReader:
		// Считыватели отдельной таблицей у нас не заведены: считыватель
		// — это часть двери с направлением. Поэтому проверяем по двери:
		// считыватель без двери существовать не может.
		table = "acs_doors"
	default:
		return false, fmt.Errorf("неизвестный вид точки: %s", kind)
	}

	var exists bool
	// Имя таблицы подставляется из закрытого списка выше, а не из запроса,
	// поэтому подстановка безопасна — параметризовать имя таблицы нельзя.
	err := r.db.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id = $1)`, table), id).Scan(&exists)
	return exists, err
}
