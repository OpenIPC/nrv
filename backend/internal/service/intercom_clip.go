package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// IntercomClipRecorder записывает вызов домофонии со звуком.
//
// Работает через ту же сегментную запись, что и события камер: клип
// склеивается из сегментов буфера вокруг момента звонка. Отдельного способа
// записи для домофона нет намеренно — у панели вызова это обычная камера,
// и делать для неё второй механизм означало бы две разные записи одного
// и того же.
type IntercomClipRecorder struct {
	cameras  *CameraService
	recorder *RecorderService
	// pre и post — сколько секунд вокруг звонка попадает в запись.
	pre, post int
}

// NewIntercomClipRecorder собирает запись вызовов.
func NewIntercomClipRecorder(cameras *CameraService, recorder *RecorderService) *IntercomClipRecorder {
	return &IntercomClipRecorder{
		cameras:  cameras,
		recorder: recorder,
		// Пять секунд до и после: этого хватает, чтобы увидеть, кто подошёл
		// к панели после звонка, и не перекодировать лишнего.
		pre:  5,
		post: 8,
	}
}

// EnsureRecording просит начать запись с камеры устройства.
func (r *IntercomClipRecorder) EnsureRecording(ctx context.Context, cameraID uuid.UUID) error {
	if r.recorder == nil || r.cameras == nil {
		return errors.New("запись недоступна: сервис выключен")
	}
	if r.recorder.IsWriting(cameraID) {
		return nil
	}

	url, err := r.cameras.StreamURLForRecord(ctx, cameraID)
	if err != nil {
		return fmt.Errorf("получить адрес потока камеры: %w", err)
	}
	if url == "" {
		return errors.New("у камеры нет адреса потока для записи")
	}
	// Режим «event» — запись по событию: она нужна как источник сегментов
	// для клипа, а не как постоянный архив.
	return r.recorder.StartWriting(cameraID, url, "event")
}

// Collect собирает клип вокруг момента вызова и сохраняет его в хранилище.
func (r *IntercomClipRecorder) Collect(
	ctx context.Context, cameraID uuid.UUID, at time.Time,
) (string, error) {
	if r.recorder == nil {
		return "", errors.New("запись недоступна: сервис выключен")
	}

	clip, _, _, err := r.recorder.CollectClip(cameraID, at, r.pre, r.post)
	if err != nil {
		return "", err
	}
	if clip == "" {
		return "", errors.New("сегментов для записи вызова не нашлось")
	}
	// Буферный файл удаляем в любом случае: он занимает место, а данные
	// после сохранения уже в хранилище.
	defer removeFile(clip)

	stored, _, err := r.recorder.SaveRecording(ctx, clip)
	if err != nil {
		return "", fmt.Errorf("сохранить запись вызова: %w", err)
	}
	return stored, nil
}

// Убеждаемся, что адаптер подходит наблюдателю за вызовами.
var _ CallClipRecorder = (*IntercomClipRecorder)(nil)
