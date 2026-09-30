package acs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/nvr/backend/internal/domain"
)

// Работа с базой карт контроллера SKUD.
//
// Контроллер хранит до 20000 карт в собственной NVS и умеет отдавать их
// списком, добавлять, менять, удалять и импортировать пачкой. Когда сервер
// настроен как центральный (op_mode=1), именно он считается источником
// истины: сервер раздаёт карты контроллерам, а не наоборот.
//
// Формат карты взят из прошивки (components/card_db/include/card_db.h):
// facility — 8 бит, card — 16 бит. Это ограничение Wiegand-26: номера
// карт не могут быть произвольными, и слишком большие значения контроллер
// отклонит или обрежет.

// skudCard — карта в формате прошивки.
type skudCard struct {
	Facility int    `json:"facility"`
	Card     int    `json:"card"`
	Name     string `json:"name"`
	Access   int    `json:"access"`
	Group    string `json:"group"`
	Active   bool   `json:"active"`
}

// Ограничения прошивки на номера карт (Wiegand-26).
const (
	skudCardFacilityMax = 255
	skudCardNumberMax   = 65535
)

// Проверка номера карты до отправки на контроллер: иначе контроллер
// молча обрежет значение, и в базе окажется не та карта, что заводил
// оператор. Лучше отказать сразу и явно.
//
// card принимается как int64, а не int: в общей модели поле номера карты
// расширено под 32-битный код контроллеров Z5R. Для SKUD это ничего не
// меняет — пределы здесь всё равно свои, Wiegand-26.
func validateSkudCard(facility int, card int64) error {
	if facility < 0 || facility > skudCardFacilityMax {
		return fmt.Errorf("facility должен быть от 0 до %d (получено %d)",
			skudCardFacilityMax, facility)
	}
	if card < 0 || card > int64(skudCardNumberMax) {
		return fmt.Errorf("номер карты должен быть от 0 до %d (получено %d)",
			skudCardNumberMax, card)
	}
	return nil
}

// toDomain превращает карту контроллера в общую модель.
func (a *SkudAdapter) toDomain(c skudCard) domain.ACSCard {
	return domain.ACSCard{
		ControllerID: a.ctrl.ID,
		Facility:     c.Facility,
		CardNumber:   int64(c.Card),
		Name:         strings.TrimSpace(c.Name),
		Group:        strings.TrimSpace(c.Group),
		Access:       c.Access,
		Active:       c.Active,
	}
}

// checkOK разбирает ответ вида {"ok":true} и превращает его в ошибку.
//
// Контроллер сообщает о неудаче кодом 200 и флагом ok=false, поэтому
// проверять только HTTP-статус недостаточно: без этой проверки «карта уже
// есть» или «база заполнена» выглядели бы как успех.
func checkOK(data []byte, action string) error {
	var r struct {
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("разобрать ответ контроллера: %w", err)
	}
	if r.OK != nil && !*r.OK {
		switch r.Error {
		case "exists":
			return fmt.Errorf("такая карта уже есть на контроллере")
		case "full":
			return fmt.Errorf("база карт контроллера заполнена")
		case "not found":
			return fmt.Errorf("карта не найдена на контроллере")
		}
		if r.Error != "" {
			return fmt.Errorf("контроллер отклонил %s: %s", action, r.Error)
		}
		return fmt.Errorf("контроллер отклонил %s", action)
	}
	return nil
}

// ListCards возвращает все карты, хранящиеся на контроллере.
func (a *SkudAdapter) ListCards(ctx context.Context) ([]domain.ACSCard, error) {
	data, err := a.call(ctx, http.MethodGet, "/api/cards", nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Cards []skudCard `json:"cards"`
		Count int        `json:"count"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("разобрать список карт: %w", err)
	}

	cards := make([]domain.ACSCard, 0, len(resp.Cards))
	for _, c := range resp.Cards {
		cards = append(cards, a.toDomain(c))
	}
	return cards, nil
}

// AddCard добавляет карту на контроллер.
func (a *SkudAdapter) AddCard(ctx context.Context, card domain.ACSCard) error {
	if err := validateSkudCard(card.Facility, card.CardNumber); err != nil {
		return err
	}

	data, err := a.call(ctx, http.MethodPost, "/api/cards/add", skudCard{
		Facility: card.Facility,
		Card:     int(card.CardNumber),
		Name:     card.Name,
		Access:   card.Access,
		Group:    card.Group,
		Active:   card.Active,
	})
	if err != nil {
		return err
	}
	return checkOK(data, "добавление карты")
}

// UpdateCard изменяет карту. Ключ — facility+card, поэтому для смены
// самого номера карты нужно сначала удалить старую запись.
func (a *SkudAdapter) UpdateCard(ctx context.Context, card domain.ACSCard) error {
	if err := validateSkudCard(card.Facility, card.CardNumber); err != nil {
		return err
	}

	data, err := a.call(ctx, http.MethodPost, "/api/cards/update", skudCard{
		Facility: card.Facility,
		Card:     int(card.CardNumber),
		Name:     card.Name,
		Access:   card.Access,
		Group:    card.Group,
		Active:   card.Active,
	})
	if err != nil {
		return err
	}
	return checkOK(data, "изменение карты")
}

// RemoveCard удаляет карту по facility+card.
func (a *SkudAdapter) RemoveCard(ctx context.Context, facility int, card int64) error {
	data, err := a.call(ctx, http.MethodPost, "/api/cards/remove",
		map[string]int{"facility": facility, "card": int(card)})
	if err != nil {
		return err
	}
	return checkOK(data, "удаление карты")
}

// ClearCards удаляет все карты с контроллера.
func (a *SkudAdapter) ClearCards(ctx context.Context) error {
	data, err := a.call(ctx, http.MethodPost, "/api/cards/clear", nil)
	if err != nil {
		return err
	}
	return checkOK(data, "очистку базы карт")
}

// ImportCards загружает пачку карт одним запросом.
//
// Используется для первичной выдачи базы на новый контроллер: по одной
// карте это было бы слишком долго, а контроллер принимает массив.
//
// У прошивки нет отдельного режима очистки в этом эндпоинте: она сама
// обновляет совпадающие карты и добавляет новые. Для полной перезаписи
// сначала вызывается ClearCards.
func (a *SkudAdapter) ImportCards(ctx context.Context, cards []domain.ACSCard) (int, error) {
	if len(cards) == 0 {
		return 0, nil
	}

	payload := make([]skudCard, 0, len(cards))
	for _, c := range cards {
		if err := validateSkudCard(c.Facility, c.CardNumber); err != nil {
			return 0, fmt.Errorf("карта %d:%d: %w", c.Facility, c.CardNumber, err)
		}
		payload = append(payload, skudCard{
			Facility: c.Facility,
			Card:     int(c.CardNumber),
			Name:     c.Name,
			Access:   c.Access,
			Group:    c.Group,
			Active:   c.Active,
		})
	}

	data, err := a.call(ctx, http.MethodPost, "/api/cards/import", payload)
	if err != nil {
		return 0, err
	}
	if err := checkOK(data, "импорт карт"); err != nil {
		return 0, err
	}

	var r struct {
		Imported int `json:"imported"`
	}
	json.Unmarshal(data, &r)
	return r.Imported, nil
}

// SetCardMode включает режим обучения.
//
// В этом режиме контроллер не принимает решения о доступе, а запоминает
// следующую поднесённую карту. Так оператор заводит карту, не вводя её
// номер руками, — номер считывается с реального носителя.
func (a *SkudAdapter) SetCardMode(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("имя владельца карты не задано")
	}
	_, err := a.call(ctx, http.MethodPost, "/api/cards/learn", map[string]string{
		"name": name,
	})
	return err
}

// CancelCardMode отменяет режим обучения.
func (a *SkudAdapter) CancelCardMode(ctx context.Context) error {
	_, err := a.call(ctx, http.MethodPost, "/api/cards/learn/cancel", nil)
	return err
}

// GetCardMode сообщает, ждёт ли контроллер карту в режиме обучения.
//
// Имя владельца прошивка в статусе не отдаёт, поэтому возвращается только
// признак активности: имя оператор видит в интерфейсе сервера.
func (a *SkudAdapter) GetCardMode(ctx context.Context) (bool, error) {
	data, err := a.call(ctx, http.MethodGet, "/api/cards/learn", nil)
	if err != nil {
		return false, err
	}

	var st struct {
		LearnActive bool `json:"learn_active"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return false, fmt.Errorf("разобрать состояние обучения: %w", err)
	}
	return st.LearnActive, nil
}
