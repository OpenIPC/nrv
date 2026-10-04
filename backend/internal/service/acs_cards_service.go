package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service/acs"
	"github.com/rs/zerolog/log"
)

// ErrCardsUnsupported — контроллер не умеет управлять картами.
//
// Отдельная ошибка нужна, чтобы интерфейс отличал «контроллер этого не
// умеет» от сбоя связи: в первом случае список карт с устройства не
// показывают вовсе, во втором — показывают сообщение об ошибке. Сверять
// по тексту нельзя: текст переводится, и признак отвалился бы при смене
// языка интерфейса, а сам список молча считался бы неподдерживаемым.
var ErrCardsUnsupported = errors.New("контроллер не поддерживает управление картами")

// Управление картами доступа СКУД.
//
// Роли сервера и контроллера разделены намеренно:
//   - сервер хранит общий справочник карт (acs_cards) и является источником
//     истины — так одну карту можно выдать сразу на несколько контроллеров,
//     а при замене устройства база не теряется;
//   - контроллер хранит свою копию и умеет работать автономно, если связь
//     с сервером пропала.
//
// Поэтому у карт два независимых хранилища, и операции их синхронизации
// вынесены в отдельные методы (SyncCardsToController, ImportCardsFromController).

// ListCards возвращает карты. controllerID = nil — все контроллеры.
func (s *ACSService) ListCards(ctx context.Context, controllerID *uuid.UUID) ([]domain.ACSCard, error) {
	return s.cardRepo.ListCards(ctx, controllerID)
}

// ListControllerCards читает карты напрямую с контроллера.
//
// Нужно, чтобы показать реальное содержимое устройства: там могут
// оказаться карты, заведённые в обход сервера (например, режимом обучения
// или оставшиеся от прежнего владельца).
func (s *ACSService) ListControllerCards(ctx context.Context, controllerID uuid.UUID) ([]domain.ACSCard, error) {
	adapter, err := s.cardAdapter(ctx, controllerID)
	if err != nil {
		return nil, err
	}
	return adapter.ListCards(ctx)
}

// cardAdapter находит контроллер и проверяет, что адаптер умеет работать
// с картами.
func (s *ACSService) cardAdapter(ctx context.Context, controllerID uuid.UUID) (acs.CardManager, error) {
	ctrl, err := s.repo.GetByID(ctx, controllerID)
	if err != nil {
		return nil, fmt.Errorf("контроллер не найден: %w", err)
	}

	adapter, err := s.manager.GetAdapter(ctrl.Vendor, ctrl)
	if err != nil {
		return nil, fmt.Errorf("нет адаптера для %s: %w", ctrl.Vendor, err)
	}

	cm, ok := acs.CardsFor(adapter)
	if !ok {
		return nil, fmt.Errorf("%s: %w", ctrl.Vendor, ErrCardsUnsupported)
	}
	return cm, nil
}

// CreateCard заводит карту на сервере и, если задан контроллер, сразу
// выдаёт её на устройство.
//
// Порядок важен: сначала сервер, потом устройство. Если выдачу на
// контроллер выполнить не удалось, карта остаётся на сервере, и оператор
// увидит ошибку — данные не потеряются, а расхождение лечится повторной
// синхронизацией.
func (s *ACSService) CreateCard(ctx context.Context, card domain.ACSCard) (*domain.ACSCard, error) {
	if err := validateCard(card); err != nil {
		return nil, err
	}

	if err := s.cardRepo.UpsertCard(ctx, &card); err != nil {
		return nil, fmt.Errorf("сохранить карту: %w", err)
	}

	if card.ControllerID != uuid.Nil {
		adapter, err := s.cardAdapter(ctx, card.ControllerID)
		if err != nil {
			return &card, fmt.Errorf("карта сохранена на сервере, но не выдана: %w", err)
		}
		// AddCard на контроллере отвергает уже существующую карту. Это не
		// ошибка для оператора: карту могли просто завести повторно с
		// новым именем. Поэтому при отказе «уже есть» переходим на update.
		if err := adapter.AddCard(ctx, card); err != nil {
			if err := adapter.UpdateCard(ctx, card); err != nil {
				return &card, fmt.Errorf("карта сохранена на сервере, но не выдана: %w", err)
			}
		}
	}

	log.Info().Int("facility", card.Facility).Int64("card", card.CardNumber).
		Str("имя", card.Name).Msg("карта СКУД добавлена")
	return &card, nil
}

// UpdateCard изменяет карту на сервере и на контроллере.
func (s *ACSService) UpdateCard(ctx context.Context, card domain.ACSCard) (*domain.ACSCard, error) {
	if card.ID == uuid.Nil {
		return nil, fmt.Errorf("не указан идентификатор карты")
	}
	if err := validateCard(card); err != nil {
		return nil, err
	}

	// Прежнее состояние нужно, чтобы понять, меняется ли номер карты:
	// на контроллере карта адресуется парой facility+card, и при смене
	// номера старую запись надо удалить, иначе на устройстве останутся
	// обе карты.
	old, err := s.cardRepo.GetCard(ctx, card.ID)
	if err != nil {
		return nil, fmt.Errorf("карта не найдена: %w", err)
	}

	if err := s.cardRepo.UpsertCard(ctx, &card); err != nil {
		return nil, fmt.Errorf("сохранить карту: %w", err)
	}

	adapter, err := s.cardAdapter(ctx, card.ControllerID)
	if err != nil {
		return &card, fmt.Errorf("карта обновлена на сервере, но не на контроллере: %w", err)
	}

	numberChanged := old.Facility != card.Facility || old.CardNumber != card.CardNumber
	if numberChanged {
		if err := adapter.RemoveCard(ctx, old.Facility, old.CardNumber); err != nil {
			log.Warn().Err(err).Int("facility", old.Facility).
				Int64("card", old.CardNumber).
				Msg("не удалось удалить прежний номер карты с контроллера")
		}
	}

	if err := adapter.UpdateCard(ctx, card); err != nil {
		// Если карты с новым номером на устройстве ещё нет, update вернёт
		// «не найдена» — тогда добавляем.
		if addErr := adapter.AddCard(ctx, card); addErr != nil {
			return &card, fmt.Errorf("карта обновлена на сервере, но не на контроллере: %w", err)
		}
	}

	return &card, nil
}

// DeleteCard удаляет карту с сервера и с контроллера.
func (s *ACSService) DeleteCard(ctx context.Context, id uuid.UUID) error {
	card, err := s.cardRepo.GetCard(ctx, id)
	if err != nil {
		return fmt.Errorf("карта не найдена: %w", err)
	}

	if err := s.cardRepo.DeleteCard(ctx, id); err != nil {
		return fmt.Errorf("удалить карту: %w", err)
	}

	if card.ControllerID != uuid.Nil {
		adapter, err := s.cardAdapter(ctx, card.ControllerID)
		if err != nil {
			return fmt.Errorf("карта удалена на сервере, но не на контроллере: %w", err)
		}
		if err := adapter.RemoveCard(ctx, card.Facility, card.CardNumber); err != nil {
			return fmt.Errorf("карта удалена на сервере, но не на контроллере: %w", err)
		}
	}
	return nil
}

// SyncCardsToController выдаёт серверную базу карт на контроллер.
//
// Полная перезапись: сначала очищаем базу устройства, затем загружаем
// серверную одним запросом. Так на контроллере не остаётся карт, удалённых
// на сервере, — иначе удалённый сотрудник сохранил бы доступ.
func (s *ACSService) SyncCardsToController(ctx context.Context, controllerID uuid.UUID) (int, error) {
	adapter, err := s.cardAdapter(ctx, controllerID)
	if err != nil {
		return 0, err
	}

	cards, err := s.cardRepo.ListCards(ctx, &controllerID)
	if err != nil {
		return 0, fmt.Errorf("прочитать карты с сервера: %w", err)
	}

	if err := adapter.ClearCards(ctx); err != nil {
		return 0, fmt.Errorf("очистить базу карт контроллера: %w", err)
	}

	n, err := adapter.ImportCards(ctx, cards)
	if err != nil {
		return 0, fmt.Errorf("загрузить карты на контроллер: %w", err)
	}

	log.Info().Str("контроллер", controllerID.String()).Int("карт", n).
		Msg("база карт выдана на контроллер СКУД")
	return n, nil
}

// ImportCardsFromController переносит карты с контроллера на сервер.
//
// Нужно при подключении уже работающего устройства: карты обычно заводят
// на нём, и их проще забрать, чем вводить заново. Серверная база при этом
// заменяется целиком — источником истины остаётся сервер.
func (s *ACSService) ImportCardsFromController(ctx context.Context, controllerID uuid.UUID) (int, error) {
	adapter, err := s.cardAdapter(ctx, controllerID)
	if err != nil {
		return 0, err
	}

	cards, err := adapter.ListCards(ctx)
	if err != nil {
		return 0, fmt.Errorf("прочитать карты с контроллера: %w", err)
	}

	if err := s.cardRepo.ReplaceAllForController(ctx, controllerID, cards); err != nil {
		return 0, fmt.Errorf("сохранить карты на сервере: %w", err)
	}

	log.Info().Str("контроллер", controllerID.String()).Int("карт", len(cards)).
		Msg("база карт перенесена с контроллера на сервер")
	return len(cards), nil
}

// StartCardLearn включает на контроллере режим обучения.
//
// Оператору не нужно вводить номер карты вручную: контроллер запомнит
// номер с поднесённого носителя. Имя сохраняется на сервере, чтобы
// добавленную карту было чем подписать.
func (s *ACSService) StartCardLearn(ctx context.Context, controllerID uuid.UUID, name string) error {
	adapter, err := s.cardAdapter(ctx, controllerID)
	if err != nil {
		return err
	}
	return adapter.SetCardMode(ctx, name)
}

// CancelCardLearn отменяет режим обучения.
func (s *ACSService) CancelCardLearn(ctx context.Context, controllerID uuid.UUID) error {
	adapter, err := s.cardAdapter(ctx, controllerID)
	if err != nil {
		return err
	}
	return adapter.CancelCardMode(ctx)
}

// GetCardLearnState сообщает, ждёт ли контроллер карту.
func (s *ACSService) GetCardLearnState(ctx context.Context, controllerID uuid.UUID) (bool, error) {
	adapter, err := s.cardAdapter(ctx, controllerID)
	if err != nil {
		return false, err
	}
	return adapter.GetCardMode(ctx)
}

// ListDoors возвращает двери контроллера.
//
// Нужно интерфейсу, чтобы открывать дверь по её идентификатору, а не по
// зашитой строке: у разных вендоров идентификаторы дверей отличаются.
func (s *ACSService) ListDoors(ctx context.Context, ctrl *domain.ACSController) ([]acs.Door, error) {
	adapter, err := s.manager.GetAdapter(ctrl.Vendor, ctrl)
	if err != nil {
		return nil, fmt.Errorf("нет адаптера для %s: %w", ctrl.Vendor, err)
	}
	return adapter.ListDoors(ctx)
}

// Пределы полей карты.
//
// Значения взяты из формата контроллера Z5R WEB BT: он работает с
// шестибайтовым кодом, где два старших байта — facility, четыре младших —
// номер карты. Прежние пределы Wiegand-26 (facility 8 бит, номер 16 бит)
// отсекали почти все реальные карты: на живом парке из 21 карты ни одна
// не помещалась в 16 бит.
const (
	maxCardFacility = 65535
	maxCardNumber   = 4294967295
)

// validateCard проверяет карту до обращения к хранилищу.
//
// Проверка общая для всех контроллеров: она отсекает значения, которые
// не примет ни одно устройство. Свои, более узкие пределы проверяет
// адаптер конкретного вендора — например, SKUD работает по Wiegand-26
// и откажет в карте с большим номером сам.
func validateCard(card domain.ACSCard) error {
	if card.Facility < 0 || card.Facility > maxCardFacility {
		return fmt.Errorf("facility должен быть от 0 до %d", maxCardFacility)
	}
	if card.CardNumber < 0 || card.CardNumber > maxCardNumber {
		return fmt.Errorf("номер карты должен быть от 0 до %d", maxCardNumber)
	}
	if card.ControllerID == uuid.Nil {
		return fmt.Errorf("не указан контроллер")
	}
	return nil
}
