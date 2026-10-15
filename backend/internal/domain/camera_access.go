package domain

import (
	"time"

	"github.com/google/uuid"
)

// CameraGroup — именованный набор камер, который выдаётся доступом целиком.
//
// Группа нужна там, где камер много, а выдача одна и та же: «двор»,
// «подъезд 2», «склад». Состав группы меняет администратор один раз, и он же
// действует сразу для всех, кому группа выдана.
type CameraGroup struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Note string    `json:"note"`
	// CameraIDs — состав группы. Пустой состав означает, что группа пока
	// никого ни к чему не допускает: это честнее, чем считать пустую группу
	// выдачей всего парка.
	CameraIDs []uuid.UUID `json:"camera_ids"`
	CreatedAt time.Time   `json:"created_at"`
}

// UserCameraAccess — доступ пользователя к камерам одним документом.
//
// Документ, а не набор отдельных операций: интерфейс правит галочки и
// сохраняет результат целиком, поэтому промежуточного состояния «половина
// выдана, половина нет» после сбоя не бывает.
type UserCameraAccess struct {
	// AllCameras — «видит все камеры, включая новые».
	AllCameras bool `json:"all_cameras"`
	// CameraIDs — камеры, выданные лично.
	CameraIDs []uuid.UUID `json:"camera_ids"`
	// GroupIDs — группы камер, выданные целиком.
	GroupIDs []uuid.UUID `json:"group_ids"`
}
