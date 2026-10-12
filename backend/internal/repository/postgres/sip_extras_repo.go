package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nvr/backend/internal/domain"
)

// SetAccountSwitch привязывает абонента к порту коммутатора.
//
// Порт проверяется на существование в таблице портов — как и у камер.
// Без этой проверки можно было бы связать трубку с портом, которого на
// коммутаторе нет: в интерфейсе связь выглядела бы рабочей, а питание
// и отключение порта уходили бы «в пустоту».
func (r *SipRepo) SetAccountSwitch(ctx context.Context, accountID, switchID uuid.UUID, port int) error {
	// Существует ли такой порт. Проверяем отдельным запросом, чтобы
	// отличить причину отказа: «порта нет» и «абонента нет» оператору
	// нужно видеть по-разному.
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM switch_ports WHERE switch_id = $1 AND port_number = $2
		)`, switchID, port).Scan(&exists)
	if err != nil {
		return fmt.Errorf("проверить порт коммутатора: %w", err)
	}
	if !exists {
		// У только что добавленного коммутатора список портов может быть
		// ещё не заполнен: он приходит при первом опросе. Тогда порт
		// принимаем без проверки — иначе оператор не сможет указать
		// место устройства до окончания опроса.
		var known bool
		if err := r.db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM switch_ports WHERE switch_id = $1)`, switchID,
		).Scan(&known); err != nil {
			return fmt.Errorf("проверить порты коммутатора: %w", err)
		}
		if known {
			return fmt.Errorf("на коммутаторе нет порта %d", port)
		}
	}

	tag, err := r.db.Exec(ctx, `
		INSERT INTO sip_switch_port (account_id, switch_id, port_number)
		VALUES ($1, $2, $3)
		ON CONFLICT (account_id) DO UPDATE SET
			switch_id = EXCLUDED.switch_id,
			port_number = EXCLUDED.port_number,
			updated_at = now()`,
		accountID, switchID, port)
	if err != nil {
		return fmt.Errorf("привязать абонента к порту: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearAccountSwitch снимает привязку абонента к порту.
//
// Отсутствие привязки ошибкой не считается: оператор мог нажать «отвязать»
// второй раз, и отказ на этом выглядел бы как сбой.
func (r *SipRepo) ClearAccountSwitch(ctx context.Context, accountID uuid.UUID) error {
	if _, err := r.db.Exec(ctx,
		`DELETE FROM sip_switch_port WHERE account_id = $1`, accountID); err != nil {
		return fmt.Errorf("снять привязку абонента к порту: %w", err)
	}
	return nil
}

// GetSettings читает настройки телефонии сервера.
func (r *SipRepo) GetSettings(ctx context.Context) (*domain.SipSettings, error) {
	var s domain.SipSettings
	err := r.db.QueryRow(ctx, `
		SELECT external_address, local_net, video_enabled, video_codec, ring_timeout,
		       stun_server, turn_server, turn_port, turn_tls_port, turn_username,
		       turn_password, turn_cert_domain, duckdns_token, acme_email,
		       turn_cert_error, rtp_port_start, rtp_port_end, updated_at
		FROM sip_settings WHERE id = 1`).
		Scan(&s.ExternalAddress, &s.LocalNet, &s.VideoEnabled, &s.VideoCodec,
			&s.RingTimeout, &s.StunServer, &s.TurnServer, &s.TurnPort, &s.TurnTLSPort,
			&s.TurnUsername, &s.TurnPassword, &s.TurnCertDomain, &s.DuckDNSToken,
			&s.ACMEEmail, &s.TurnCertError, &s.RtpPortStart, &s.RtpPortEnd, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Строку создаёт миграция, но пустая таблица не должна валить
		// страницу настроек: отдаём значения по умолчанию.
		return &domain.SipSettings{
			VideoCodec:   "vp8",
			RingTimeout:  30,
			StunServer:   "stun.sipnet.ru:3478",
			TurnPort:     3478,
			TurnTLSPort:  5349,
			RtpPortStart: 10000,
			RtpPortEnd:   10100,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sip settings: %w", err)
	}
	return &s, nil
}

// UpdateSettings сохраняет настройки телефонии.
func (r *SipRepo) UpdateSettings(ctx context.Context, s *domain.SipSettings) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO sip_settings (id, external_address, local_net, video_enabled, video_codec,
		                         ring_timeout, stun_server, turn_server, turn_port, turn_tls_port,
		                         turn_username, turn_password, turn_cert_domain, duckdns_token,
		                         acme_email, rtp_port_start, rtp_port_end, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, now())
		ON CONFLICT (id) DO UPDATE SET
			external_address = EXCLUDED.external_address,
			local_net = EXCLUDED.local_net,
			video_enabled = EXCLUDED.video_enabled,
			video_codec = EXCLUDED.video_codec,
			ring_timeout = EXCLUDED.ring_timeout,
			stun_server = EXCLUDED.stun_server,
			turn_server = EXCLUDED.turn_server,
			turn_port = EXCLUDED.turn_port,
			turn_tls_port = EXCLUDED.turn_tls_port,
			turn_username = EXCLUDED.turn_username,
			turn_password = EXCLUDED.turn_password,
			turn_cert_domain = EXCLUDED.turn_cert_domain,
			duckdns_token = EXCLUDED.duckdns_token,
			acme_email = EXCLUDED.acme_email,
			rtp_port_start = EXCLUDED.rtp_port_start,
			rtp_port_end = EXCLUDED.rtp_port_end,
			updated_at = now()`,
		s.ExternalAddress, s.LocalNet, s.VideoEnabled, s.VideoCodec, s.RingTimeout,
		s.StunServer, s.TurnServer, s.TurnPort, s.TurnTLSPort, s.TurnUsername,
		s.TurnPassword, s.TurnCertDomain, s.DuckDNSToken, s.ACMEEmail,
		s.RtpPortStart, s.RtpPortEnd)
	if err != nil {
		return fmt.Errorf("update sip settings: %w", err)
	}
	return nil
}

// SetTurnCertError записывает итог последней попытки выпустить сертификат.
//
// Отдельным методом, а не через UpdateSettings: выпуск запускается своей
// кнопкой и не должен затирать настройки, которые оператор правит в этот
// момент. Пустая строка означает, что ошибки нет.
func (r *SipRepo) SetTurnCertError(ctx context.Context, message string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE sip_settings SET turn_cert_error = $1, updated_at = now()
		WHERE id = 1`, message)
	if err != nil {
		return fmt.Errorf("записать ошибку выпуска сертификата: %w", err)
	}
	return nil
}
