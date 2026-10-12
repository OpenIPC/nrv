package service

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nvr/backend/internal/domain"
)

// TurnCertificateFile — имя файла, из которого coturn берёт сертификат.
const TurnCertificateFile = "turn.crt"

// ReadTurnCertificate читает сертификат TURN и описывает его состояние.
//
// Смотрим сам файл, а не базу: coturn пользуется именно файлом, и только он
// говорит правду о том, что сейчас на сервере. Сертификат могли выпустить
// скриптом из командной строки, и тогда запись в базе отстала бы.
//
// Отсутствие файла — не ошибка, а состояние «сертификата нет»: установка
// может работать и без TLS, на одном TURN по TCP.
func ReadTurnCertificate(dir string) (*domain.TurnCertificate, error) {
	cert := &domain.TurnCertificate{}
	if strings.TrimSpace(dir) == "" {
		return cert, nil
	}

	raw, err := os.ReadFile(filepath.Join(dir, TurnCertificateFile))
	if errors.Is(err, os.ErrNotExist) {
		return cert, nil
	}
	if err != nil {
		return nil, fmt.Errorf("прочитать сертификат: %w", err)
	}

	// Берём первый блок: в файле обычно лежит цепочка «наш сертификат +
	// промежуточный», и нужен именно первый.
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("файл сертификата не в формате PEM")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("разобрать сертификат: %w", err)
	}

	cert.Present = true
	cert.Domain = parsed.Subject.CommonName
	if len(parsed.DNSNames) > 0 {
		// SAN важнее CN: современные клиенты, включая WebRTC, смотрят
		// только на него, а CN может быть пустым.
		cert.Domain = parsed.DNSNames[0]
	}
	if len(parsed.Issuer.Organization) > 0 {
		cert.Issuer = parsed.Issuer.Organization[0]
	} else {
		cert.Issuer = parsed.Issuer.CommonName
	}
	cert.NotAfter = parsed.NotAfter
	// Дни считаем от текущего момента: дата без счётчика не говорит
	// оператору, пора ли что-то делать.
	cert.DaysLeft = int(time.Until(parsed.NotAfter).Hours() / 24)
	return cert, nil
}
