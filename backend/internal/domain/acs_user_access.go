package domain

import "github.com/google/uuid"

// UserACSAccess — доступ учётной записи к СКУД.
//
// Единица выдачи — контроллер: команда открытия уходит контроллеру с его
// собственным обозначением двери, а связь этого обозначения с дверью в базе
// нигде не зафиксирована, и проверить права на дверь в момент открытия было
// бы нечем. Выдача контроллера покрывает и его двери, и его события.
type UserACSAccess struct {
	// AllControllers — «видит и открывает все контроллеры», включая
	// добавленные позже. Нужен дежурному.
	AllControllers bool `json:"all_acs_controllers"`
	// ControllerIDs — контроллеры, выданные лично.
	ControllerIDs []uuid.UUID `json:"controller_ids"`
}
