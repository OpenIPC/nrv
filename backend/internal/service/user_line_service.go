package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/rs/zerolog/log"
)

// UserLineProvisioner выдаёт учётным записям сервера их внутренние номера.
//
// Интерфейс, а не конкретный сервис: на установке без телефонии (сервер
// поднят без `--sip`) линий нет вовсе, и работа с пользователями не должна
// от этого ломаться.
type UserLineProvisioner interface {
	// EnsureLine создаёт линию, если её ещё нет.
	EnsureLine(ctx context.Context, user *domain.User) error
	// RemoveLine убирает линию вместе с учётной записью.
	RemoveLine(ctx context.Context, userID uuid.UUID) error
	// RenameLine меняет подпись линии вслед за именем владельца.
	RenameLine(ctx context.Context, userID uuid.UUID, name string) error
}

// UserLineService — линии SIP у учётных записей сервера.
//
// Зачем это вообще: у каждого, кто входит в систему, должен быть свой
// внутренний номер. Иначе в журнале вызовов не видно, кто взял трубку, а
// два оператора не могут позвонить друг другу — оба звонили бы «с сервера».
type UserLineService struct {
	repo    *postgres.SipRepo
	builder *SipConfigBuilder
}

func NewUserLineService(repo *postgres.SipRepo, builder *SipConfigBuilder) *UserLineService {
	return &UserLineService{repo: repo, builder: builder}
}

// EnsureLine создаёт линию владельца, если её ещё нет.
//
// Идемпотентна: вызывается и при создании учётной записи, и при входе
// владельца — так линия появляется и у тех пользователей, которые были
// заведены до этой возможности.
func (s *UserLineService) EnsureLine(ctx context.Context, user *domain.User) error {
	if s == nil || s.repo == nil || user == nil {
		return nil
	}

	existing, err := s.repo.GetAccountByUser(ctx, user.ID)
	if err == nil {
		// Линия есть. Обновляем подпись, если владелец переименован: имя
		// видно в списке абонентов, и старое имя сбивало бы с толку.
		if existing.Name != user.Username {
			if err := s.repo.RenameUserLine(ctx, user.ID, user.Username); err != nil {
				return err
			}
			s.sync()
		}
		return nil
	}
	if !errors.Is(err, postgres.ErrNotFound) {
		return err
	}

	number, err := s.repo.NextAppNumber(ctx)
	if err != nil {
		return err
	}

	line := &domain.SipAccount{
		Number: number,
		Kind:   domain.SipKindSoftphone,
		Name:   user.Username,
		// Пароль свой, а не пароль учётной записи: он лежит в памяти
		// телефона и попадает в резервные копии, а пароль от входа в систему
		// даёт доступ ко всей установке. Смена пароля пользователя при этом
		// не рвёт уже настроенный звонок.
		Password: newLinePassword(),
		UserID:   &user.ID,
		Enabled:  true,
		// Пропущенный вызов — событие, о котором хотят знать; переписку в
		// мессенджерах владелец включит себе сам, если она нужна.
		NotifyMissed: true,
	}
	if err := s.repo.CreateAccount(ctx, line); err != nil {
		return err
	}

	log.Info().Str("number", number).Str("user", user.Username).
		Msg("учётной записи выдана линия SIP")
	s.sync()
	return nil
}

// RemoveLine убирает линию владельца.
func (s *UserLineService) RemoveLine(ctx context.Context, userID uuid.UUID) error {
	if s == nil || s.repo == nil {
		return nil
	}
	if err := s.repo.DeleteAccountByUser(ctx, userID); err != nil {
		return err
	}
	s.sync()
	return nil
}

// RenameLine обновляет подпись линии.
func (s *UserLineService) RenameLine(ctx context.Context, userID uuid.UUID, name string) error {
	if s == nil || s.repo == nil {
		return nil
	}
	if err := s.repo.RenameUserLine(ctx, userID, name); err != nil {
		return err
	}
	s.sync()
	return nil
}

// sync пересобирает конфигурацию Asterisk.
//
// Ошибку только пишем в журнал: учётная запись уже сохранена, и отменять её
// из-за недоступной телефонии нельзя. Конфигурацию можно пересобрать кнопкой
// «Применить в Asterisk» в разделе домофонии.
func (s *UserLineService) sync() {
	if s.builder == nil {
		return
	}
	if err := s.builder.Sync(context.Background()); err != nil {
		log.Error().Err(err).Msg("не удалось пересобрать конфигурацию SIP после правки учётной записи")
	}
}

// newLinePassword выдаёт пароль линии.
//
// 8 случайных байт (16 символов) — столько же, сколько у линий, заведённых
// вручную, и достаточно, чтобы пароль нельзя было подобрать, пока устройство
// на связи со своей АТС.
func newLinePassword() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// Отказ генератора случайных чисел — событие уровня ядра; пароль
		// всё равно нужен, и лучше предсказуемый, чем учётная запись без
		// линии, о которой потом никто не вспомнит.
		log.Error().Err(err).Msg("не удалось получить случайный пароль линии SIP")
		return "line" + uuid.NewString()[:12]
	}
	return hex.EncodeToString(buf)
}
