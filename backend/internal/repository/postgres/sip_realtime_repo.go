package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/nvr/backend/internal/domain"
)

// Запись абонентов в таблицы Asterisk Realtime.
//
// Таблицы `ps_endpoints`, `ps_auths` и `ps_aors` принадлежат Asterisk: они
// описаны в его официальной схеме (миграция 045). Здесь мы их только
// заполняем из наших данных, поэтому все три меняются одной транзакцией —
// иначе на короткое время у абонента нашлась бы пара «auth есть, endpoint
// нет», и регистрация отвалилась бы с «неверный пароль», хотя пароль верный.

// SyncRealtimeEndpoints приводит таблицы абонентов в соответствие с данными
// сервера: недостающие строки пишет, изменившиеся обновляет, лишние удаляет.
//
// Удаление обязательно, а не «когда-нибудь потом»: если абонента уволили из
// интерфейса, а строка в базе осталась, устройство продолжит регистрироваться
// и звонить — то есть отключение не сработает.
func (r *SipRepo) SyncRealtimeEndpoints(ctx context.Context, endpoints []domain.SipRealtimeEndpoint) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать транзакцию: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	numbers := make([]string, 0, len(endpoints))
	for _, e := range endpoints {
		numbers = append(numbers, e.Number)

		// ON CONFLICT — потому что у нас один и тот же код обслуживает и
		// первого абонента, и правку пароля у сотого: разделять создание и
		// обновление значило бы держать две ветки, которые расходятся.
		_, err = tx.Exec(ctx, `
			INSERT INTO ps_auths (id, auth_type, username, password)
			VALUES ($1, 'userpass', $1, $2)
			ON CONFLICT (id) DO UPDATE SET
				auth_type = 'userpass',
				username  = EXCLUDED.username,
				password  = EXCLUDED.password`,
			e.Number, e.Password)
		if err != nil {
			return fmt.Errorf("записать пароль абонента %s: %w", e.Number, err)
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO ps_aors (id, max_contacts, remove_existing, qualify_frequency)
			VALUES ($1, $2, 'yes', $3)
			ON CONFLICT (id) DO UPDATE SET
				max_contacts      = EXCLUDED.max_contacts,
				remove_existing   = 'yes',
				qualify_frequency = EXCLUDED.qualify_frequency`,
			e.Number, e.MaxContacts, e.QualifyFrequency)
		if err != nil {
			return fmt.Errorf("записать контакты абонента %s: %w", e.Number, err)
		}

		// Поля, которые у приложений и устройств различаются, пишем как есть:
		// пустая строка в колонке-перечислении для Asterisk означает «значение
		// не задано» — то же самое, чего мы достигаем отсутствием строки в
		// файле конфигурации. А вот булевы поля обязаны быть заполнены явно:
		// пустое значение Asterisk читает как «нет» и меняет поведение
		// молча, поэтому здесь всегда 'yes' или 'no'.
		_, err = tx.Exec(ctx, `
			INSERT INTO ps_endpoints (
				id, aors, auth, context, disallow, allow, dtmf_mode,
				direct_media, rtp_symmetric, callerid,
				webrtc, media_address, rtp_port_start, rtp_port_end
			) VALUES (
				$1, $1, $1, $2, 'all', $3, $4,
				'no', 'yes', $5,
				$6, $7, NULLIF($8, 0), NULLIF($9, 0)
			)
			ON CONFLICT (id) DO UPDATE SET
				aors           = EXCLUDED.aors,
				auth           = EXCLUDED.auth,
				context        = EXCLUDED.context,
				disallow       = EXCLUDED.disallow,
				allow          = EXCLUDED.allow,
				dtmf_mode      = EXCLUDED.dtmf_mode,
				direct_media   = EXCLUDED.direct_media,
				rtp_symmetric  = EXCLUDED.rtp_symmetric,
				callerid       = EXCLUDED.callerid,
				webrtc         = EXCLUDED.webrtc,
				media_address  = EXCLUDED.media_address,
				rtp_port_start = EXCLUDED.rtp_port_start,
				rtp_port_end   = EXCLUDED.rtp_port_end`,
			e.Number, e.Context, e.Allow, e.DtmfMode, e.CallerID,
			boolText(e.WebRTC), nullText(e.MediaAddress), e.RTPPortStart, e.RTPPortEnd)
		if err != nil {
			return fmt.Errorf("записать абонента %s: %w", e.Number, err)
		}
	}

	// Лишние строки — те, которых нет в списке сервера. Сравнение через
	// `<> ALL` вместо NOT IN: со списком из сотен номеров NOT IN с NULL
	// внутри вернул бы пустой результат и не удалил ничего.
	if len(numbers) == 0 {
		for _, table := range []string{"ps_endpoints", "ps_auths", "ps_aors"} {
			if _, err := tx.Exec(ctx, "DELETE FROM "+table); err != nil {
				return fmt.Errorf("очистить %s: %w", table, err)
			}
		}
	} else {
		for _, table := range []string{"ps_endpoints", "ps_auths", "ps_aors"} {
			if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE id <> ALL($1)", numbers); err != nil {
				return fmt.Errorf("удалить лишние строки из %s: %w", table, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("зафиксировать изменения: %w", err)
	}
	return nil
}

// RealtimeAccounts читает абонентов из таблиц Asterisk.
//
// Нужен не для работы, а для проверки: если в базу попало не то, что мы
// думали, это видно здесь, а не выясняется при звонке. Плюс по этой выборке
// тест проверяет, что запись и чтение сходятся.
func (r *SipRepo) RealtimeAccounts(ctx context.Context) ([]domain.SipRealtimeEndpoint, error) {
	rows, err := r.db.Query(ctx, `
		SELECT e.id, e.context, e.callerid, a.password, e.allow, e.dtmf_mode,
		       e.webrtc, e.media_address, e.rtp_port_start, e.rtp_port_end,
		       o.max_contacts, o.qualify_frequency
		FROM ps_endpoints e
		JOIN ps_auths a ON a.id = e.id
		JOIN ps_aors  o ON o.id = e.id
		ORDER BY e.id`)
	if err != nil {
		return nil, fmt.Errorf("прочитать абонентов из Realtime: %w", err)
	}
	defer rows.Close()

	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.SipRealtimeEndpoint, error) {
		var (
			e            domain.SipRealtimeEndpoint
			webrtc       *string
			mediaAddress *string
			portStart    *int
			portEnd      *int
		)
		if err := row.Scan(&e.Number, &e.Context, &e.CallerID, &e.Password, &e.Allow,
			&e.DtmfMode, &webrtc, &mediaAddress, &portStart, &portEnd,
			&e.MaxContacts, &e.QualifyFrequency); err != nil {
			return e, err
		}
		e.WebRTC = webrtc != nil && (*webrtc == "yes" || *webrtc == "true" || *webrtc == "1")
		if mediaAddress != nil {
			e.MediaAddress = *mediaAddress
		}
		if portStart != nil {
			e.RTPPortStart = *portStart
		}
		if portEnd != nil {
			e.RTPPortEnd = *portEnd
		}
		return e, nil
	})
	if err != nil {
		return nil, fmt.Errorf("разобрать абонентов из Realtime: %w", err)
	}
	return list, nil
}

// boolText переводит признак в значение, которое понимает Asterisk.
func boolText(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// nullText возвращает NULL для пустой строки.
//
// Так принято у Asterisk: пустая строка в колонке читается как значение "", и
// для адреса это означало бы «медиа адресуется в никуда». NULL же означает
// «параметр не задан» — ровно то, что нужно.
func nullText(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
