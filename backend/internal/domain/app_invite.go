package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// AppInvite — одноразовый код для подключения мобильного приложения.
//
// Показывается администратором в виде QR-кода в карточке пользователя.
// Приложение меняет код на обычный токен сессии, поэтому в квадратике нет
// ни пароля, ни долгоживущего секрета: код живёт минуты и срабатывает один
// раз.
type AppInvite struct {
	ID     uuid.UUID `json:"id"`
	UserID uuid.UUID `json:"user_id"`
	// Code — то, что попадает в QR. Короткий: его набирают и руками, если
	// камера телефона не читает экран.
	Code      string     `json:"code"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// ErrInviteInvalid — код не найден, уже использован или просрочен.
//
// На три случая намеренно одна ошибка. Если сервер отвечает по-разному,
// по его ответам можно перебором выяснять, какие коды существуют, а какие
// нет. Приложению разница не нужна: во всех случаях нужен новый код.
var ErrInviteInvalid = errors.New("код приглашения недействителен")
