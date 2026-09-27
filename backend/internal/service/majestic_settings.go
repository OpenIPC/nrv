package service

import (
	"context"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
)

// Настройки присмотра за Majestic берутся из общих настроек сервера —
// оттуда же, где лежат уведомления. Отдельного хранилища не нужно:
// настраиваются они на той же странице и сохраняются тем же запросом.
//
// Провайдер остаётся тонким намеренно: значения по умолчанию подставляет
// репозиторий при чтении. Иначе правило «что считать незаполненной
// настройкой» жило бы в двух местах и разошлось бы при первой правке.

// MajesticSettingsProvider читает и сохраняет настройки присмотра.
type MajesticSettingsProvider struct {
	repo *postgres.DetectionSettingsRepo
}

func NewMajesticSettingsProvider(repo *postgres.DetectionSettingsRepo) *MajesticSettingsProvider {
	return &MajesticSettingsProvider{repo: repo}
}

// MajesticWatchConfig возвращает настройки присмотра.
func (p *MajesticSettingsProvider) MajesticWatchConfig(ctx context.Context) (domain.MajesticWatchConfig, error) {
	settings, err := p.repo.GetServerSettings(ctx)
	if err != nil {
		// Настройки недоступны — работаем по умолчанию, а не выключаемся.
		// Присмотр нужен именно тогда, когда что-то идёт не так, и терять
		// его из-за сбоя чтения настроек было бы неправильно.
		return domain.DefaultMajesticWatchConfig(), err
	}
	return settings.Notifications.Majestic, nil
}

// UpdateMajesticWatchConfig сохраняет настройки присмотра.
func (p *MajesticSettingsProvider) UpdateMajesticWatchConfig(ctx context.Context, cfg domain.MajesticWatchConfig) (*domain.MajesticWatchConfig, error) {
	// Метод отдаёт все настройки сервера, а не только присмотр: они лежат
	// одним объектом. Возвращаем нужную часть — и заодно именно то, что
	// легло в базу, а не то, что мы отправили.
	settings, err := p.repo.UpdateServerSettings(ctx, domain.UpdateServerSettingsRequest{Majestic: &cfg})
	if err != nil {
		return nil, err
	}
	return &settings.Notifications.Majestic, nil
}
