package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
)

// UserAccessRepo — права учётных записей на объекты: камеры, группы камер и
// контроллеры СКУД.
//
// Один репозиторий на оба вида объектов, потому что форма выдачи у них одна:
// администратор отмечает галочки в одном окне и сохраняет одним нажатием.
// Разные репозитории дали бы две транзакции на одно действие, и тогда сбой
// второй записи оставил бы половину галочек применённой — именно то, чего
// быть не должно.
//
// Чтение для проверок (CameraAccess, ACSControllerAccess) отделено от чтения
// для формы (Read): проверка идёт на каждом запросе к списку и к потоку и
// должна отдавать только нужное, а форме нужен весь набор выдачи сразу.
type UserAccessRepo struct {
	db *pgxpool.Pool
}

func NewUserAccessRepo(db *pgxpool.Pool) *UserAccessRepo {
	return &UserAccessRepo{db: db}
}

// CameraAccess возвращает «видит все камеры» и список разрешённых камер.
//
// Два источника объединены через UNION, а не UNION ALL: камера может быть
// выдана и лично, и через группу — дубликаты в наборе не нужны.
func (r *UserAccessRepo) CameraAccess(ctx context.Context, userID uuid.UUID) (bool, []uuid.UUID, error) {
	var all bool
	var ids []uuid.UUID

	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(u.all_cameras, false),
		       ARRAY(
		           SELECT uc.camera_id FROM user_cameras uc WHERE uc.user_id = u.id
		           UNION
		           SELECT m.camera_id
		             FROM user_camera_groups g
		             JOIN camera_group_members m ON m.group_id = g.group_id
		            WHERE g.user_id = u.id
		       )
		  FROM users u
		 WHERE u.id = $1`, userID).Scan(&all, &ids)
	if err != nil {
		return false, nil, err
	}
	return all, ids, nil
}

// ACSControllerAccess возвращает «видит все контроллеры» и список разрешённых.
func (r *UserAccessRepo) ACSControllerAccess(ctx context.Context, userID uuid.UUID) (bool, []uuid.UUID, error) {
	var all bool
	var ids []uuid.UUID

	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(u.all_acs_controllers, false),
		       ARRAY(SELECT c.controller_id FROM user_acs_controllers c WHERE c.user_id = u.id)
		  FROM users u
		 WHERE u.id = $1`, userID).Scan(&all, &ids)
	if err != nil {
		return false, nil, err
	}
	return all, ids, nil
}

// Read отдаёт всё, что выдано пользователю — для формы выдачи.
func (r *UserAccessRepo) Read(
	ctx context.Context,
	userID uuid.UUID,
) (domain.UserCameraAccess, domain.UserACSAccess, error) {
	cameras := domain.UserCameraAccess{CameraIDs: []uuid.UUID{}, GroupIDs: []uuid.UUID{}}
	acs := domain.UserACSAccess{ControllerIDs: []uuid.UUID{}}

	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(u.all_cameras, false),
		       COALESCE(u.all_acs_controllers, false),
		       ARRAY(SELECT camera_id FROM user_cameras WHERE user_id = u.id),
		       ARRAY(SELECT group_id FROM user_camera_groups WHERE user_id = u.id),
		       ARRAY(SELECT controller_id FROM user_acs_controllers WHERE user_id = u.id)
		  FROM users u
		 WHERE u.id = $1`,
		userID,
	).Scan(
		&cameras.AllCameras, &acs.AllControllers,
		&cameras.CameraIDs, &cameras.GroupIDs, &acs.ControllerIDs,
	)
	if err != nil {
		return cameras, acs, err
	}
	return cameras, acs, nil
}

// Replace записывает выдачу целиком: что перечислено — то и доступно.
//
// Перезапись, а не добавление: интерфейс присылает итоговый набор галочек,
// и разница между «забыли снять» и «забыли поставить» одинаково опасна —
// в одном случае доступ остаётся у того, кого ограничили, в другом его нет
// у того, кому выдали.
//
// Все четыре таблицы и оба флажка меняются в одной транзакции: сохраняется
// одна форма, и половина применённых галочек означала бы, что администратор
// не знает, что именно сохранилось.
func (r *UserAccessRepo) Replace(
	ctx context.Context,
	userID uuid.UUID,
	cameras domain.UserCameraAccess,
	acs domain.UserACSAccess,
) error {
	// nil драйвер передаёт как NULL; приводим к пустому массиву, чтобы
	// unnest не зависел от того, как вызывающий код собрал пустой список.
	cameraIDs := orEmpty(cameras.CameraIDs)
	groupIDs := orEmpty(cameras.GroupIDs)
	controllerIDs := orEmpty(acs.ControllerIDs)

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE users SET all_cameras = $2, all_acs_controllers = $3 WHERE id = $1`,
		userID, cameras.AllCameras, acs.AllControllers); err != nil {
		return err
	}

	if err := replaceLinks(ctx, tx, "user_cameras", "camera_id", userID, cameraIDs); err != nil {
		return err
	}
	if err := replaceLinks(ctx, tx, "user_camera_groups", "group_id", userID, groupIDs); err != nil {
		return err
	}
	if err := replaceLinks(ctx, tx, "user_acs_controllers", "controller_id", userID, controllerIDs); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// replaceLinks переписывает связи пользователя с объектами одного вида.
//
// Имена таблицы и колонки подставляются в текст запроса, а не передаются
// параметрами: SQL не позволяет параметризовать идентификаторы. Значения
// здесь — только из кода этого файла, из запроса они прийти не могут.
func replaceLinks(
	ctx context.Context,
	tx pgx.Tx,
	table string,
	column string,
	userID uuid.UUID,
	ids []uuid.UUID,
) error {
	_, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE user_id = $1`, userID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO `+table+` (user_id, `+column+`) SELECT $1, x FROM unnest($2::uuid[]) AS x
		 ON CONFLICT DO NOTHING`, userID, ids)
	return err
}

// ListGroups отдаёт группы камер вместе с составом.
func (r *UserAccessRepo) ListGroups(ctx context.Context) ([]domain.CameraGroup, error) {
	rows, err := r.db.Query(ctx, `
		SELECT g.id, g.name, g.note, g.created_at,
		       COALESCE(array_agg(m.camera_id) FILTER (WHERE m.camera_id IS NOT NULL),
		                ARRAY[]::uuid[])
		  FROM camera_groups g
		  LEFT JOIN camera_group_members m ON m.group_id = g.id
		 GROUP BY g.id
		 ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := make([]domain.CameraGroup, 0)
	for rows.Next() {
		var g domain.CameraGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Note, &g.CreatedAt, &g.CameraIDs); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// SaveGroup создаёт группу (id == nil) или переписывает существующую вместе
// с составом.
func (r *UserAccessRepo) SaveGroup(
	ctx context.Context,
	id *uuid.UUID,
	name string,
	note string,
	cameraIDs []uuid.UUID,
) (*domain.CameraGroup, error) {
	if cameraIDs == nil {
		cameraIDs = []uuid.UUID{}
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	groupID := uuid.New()
	if id != nil {
		groupID = *id
		if _, err := tx.Exec(ctx,
			`UPDATE camera_groups SET name = $2, note = $3 WHERE id = $1`,
			groupID, name, note); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.Exec(ctx,
			`INSERT INTO camera_groups (id, name, note) VALUES ($1, $2, $3)`,
			groupID, name, note); err != nil {
			return nil, err
		}
	}

	// Состав переписываем целиком — как и выдачу: интерфейс присылает
	// итоговый список камер группы.
	if _, err := tx.Exec(ctx,
		`DELETE FROM camera_group_members WHERE group_id = $1`, groupID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO camera_group_members (group_id, camera_id)
		SELECT $1, x FROM unnest($2::uuid[]) AS x
		ON CONFLICT DO NOTHING`, groupID, cameraIDs); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &domain.CameraGroup{
		ID:        groupID,
		Name:      name,
		Note:      note,
		CameraIDs: cameraIDs,
	}, nil
}

// DeleteGroup удаляет группу. Выдачи этой группы уходят каскадом: удалённая
// группа не должна оставлять после себя доступ, состав которого уже никто
// не видит.
func (r *UserAccessRepo) DeleteGroup(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM camera_groups WHERE id = $1`, id)
	return err
}

// orEmpty превращает nil в пустой массив: unnest с NULL не вернул бы строк,
// но полагаться на это в условии «ничего не выдано» не стоит.
func orEmpty(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
