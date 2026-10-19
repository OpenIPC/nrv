package service

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

// Realtime-конфигурация Asterisk: настройки абонентов и диалплан читаются
// прямо из нашей базы, а не из собранных сервером файлов.
//
// Зачем это нужно. Раньше сервер пересобирал файлы в каталоге `accounts` и
// перезагружал конфигурацию целиком. Выглядит безобидно, но перезагрузка
// модуля `res_pjsip` разрывает соединения приложений: телефон, который в этот
// момент разговаривает, теряет линию, а на крупном объекте правка одного
// абонента затрагивает всех. С realtime такой перезагрузки не нужно вовсе:
// Asterisk спрашивает параметры у базы при каждом запросе и регистрации.
//
// Файлы, которые здесь создаются, — только «провода» к базе: адрес и учётные
// данные PostgreSQL, соответствие объектов pjsip таблицам и указание
// диалплану брать правила из базы. Самих абонентов в файлах больше нет.

// realtimeFileNames — файлы, которые сервер пишет в каталог конфигурации.
//
// Список нужен для уборки: если установка обновляется с версии, где файлы
// назывались иначе, старые надо удалить, иначе Asterisk станет читать два
// источника сразу.
var realtimeFileNames = []string{
	"res_pgsql.conf",
	"sorcery.conf",
	"extconfig.conf",
}

// WriteRealtimeConfigs записывает конфигурацию доступа Asterisk к базе.
//
// databaseURL — та же строка подключения, которой пользуется сервер: это
// снимает целый класс ошибок «сервер пишет в одну базу, станция читает из
// другой». Пароль попадает в файл, поэтому каталог конфигурации Asterisk
// должен быть доступен только серверу и станции — так и есть: он лежит в
// каталоге установки, а не в репозитории.
func WriteRealtimeConfigs(configDir, databaseURL string) error {
	conn, err := parseDatabaseURL(databaseURL)
	if err != nil {
		return err
	}

	files := map[string]string{
		"res_pgsql.conf": renderResPgsql(conn),
		"sorcery.conf":   renderSorcery(),
		"extconfig.conf": renderExtConfig(),
	}

	for _, name := range realtimeFileNames {
		content, ok := files[name]
		if !ok {
			continue
		}
		path := filepath.Join(configDir, name)
		// Права 0644 и явный Chmod — по той же причине, что и у файлов
		// абонентов: сервер пишет файл из своего контейнера (от root), а
		// читает его Asterisk в другом контейнере и от другого пользователя.
		// Файл, созданный ранее с 0640, иначе так и остался бы недоступен.
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("записать %s: %w", name, err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			return fmt.Errorf("выставить права на %s: %w", name, err)
		}
	}

	log.Info().Str("каталог", configDir).Msg("конфигурация realtime записана")
	return nil
}

// pgConn — параметры подключения к PostgreSQL для Asterisk.
type pgConn struct {
	host     string
	port     int
	name     string
	user     string
	password string
}

// parseDatabaseURL разбирает строку подключения сервера в параметры, которые
// понимает драйвер Asterisk.
//
// Разбираем сами, а не пишем в файл строку целиком: `res_pgsql.conf` строку
// подключения не понимает — драйвер ждёт отдельные `dbhost`, `dbname` и
// прочие поля. Схема (`sslmode`) из строки отбрасывается: станция стоит на
// том же хосте, что и база, и ходит к ней по локальной сети.
func parseDatabaseURL(databaseURL string) (*pgConn, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("разобрать строку подключения к базе: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return nil, fmt.Errorf("ожидалась строка подключения PostgreSQL, получена схема %q", u.Scheme)
	}

	// Asterisk работает в контейнере с сетью хоста, и `localhost` резолвится
	// там в IPv6 ::1 — это первая строка /etc/hosts. База публикуется только
	// на IPv4 (`127.0.0.1:5434` в ports), поэтому подключение по имени падало
	// с «PostgreSQL RealTime: Failed to connect database»: станция не читала
	// абонентов из базы, и домофония не работала вовсе — ни из дома, ни
	// снаружи. Пишем адрес явно: он одинаков на любой установке.
	host := u.Hostname()
	if host == "localhost" || host == "::1" {
		host = "127.0.0.1"
	}

	conn := &pgConn{host: host, port: 5432, name: strings.TrimPrefix(u.Path, "/")}
	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("разобрать порт базы %q: %w", p, err)
		}
		conn.port = port
	}
	if u.User != nil {
		conn.user = u.User.Username()
		conn.password, _ = u.User.Password()
	}
	if conn.host == "" || conn.name == "" || conn.user == "" {
		return nil, fmt.Errorf("в строке подключения не хватает хоста, имени базы или пользователя")
	}

	// Asterisk хранит параметры подключения в буферах по 64 байта
	// (MAX_DB_OPTION_SIZE в res_config_pgsql.c) и обрезает строку до 63
	// символов. Пароль из 64 символов — ровно такой даёт генерация 32 байт
	// в hex — станция получает усечённым и подключиться не может. При этом
	// причину она не печатает: в сообщении стоит PQresultErrorMessage(NULL),
	// поэтому в журнале видно только «Failed to connect database» без
	// объяснения, а не работающая база выглядит как «станция не читает
	// абонентов» — так домофония отказывала целиком, ни из дома, ни снаружи.
	// Проверяем длину здесь, пока причина ещё видна.
	if len(conn.password) > 63 {
		log.Error().
			Int("длина", len(conn.password)).
			Msg("пароль базы длиннее 63 символов: Asterisk его обрежет и не подключится — сократите пароль в DATABASE_URL и смените его в базе")
	}

	return conn, nil
}

// renderResPgsql пишет подключение станции к базе.
//
// Требования (`requirements`) включать не ставим: на пустой таблице Asterisk
// с ними отвечает ошибкой, а не пустым списком, и станция перестаёт поднимать
// абонентов вовсе. Пустая таблица у нас — нормальное состояние новой
// установки, поэтому поведение по умолчанию («модуль не найден — значит,
// объекта нет») подходит лучше.
func renderResPgsql(conn *pgConn) string {
	var out strings.Builder
	out.WriteString("; Файл собран сервером из строки подключения к базе. Правки будут перезаписаны.\n")
	out.WriteString("; Здесь только доступ к базе: сами настройки абонентов и диалплан\n")
	out.WriteString("; Asterisk читает из таблиц (см. sorcery.conf и extconfig.conf).\n\n")
	out.WriteString("[general]\n")
	fmt.Fprintf(&out, "dbhost=%s\n", conn.host)
	fmt.Fprintf(&out, "dbport=%d\n", conn.port)
	fmt.Fprintf(&out, "dbname=%s\n", conn.name)
	fmt.Fprintf(&out, "dbuser=%s\n", conn.user)
	fmt.Fprintf(&out, "dbpass=%s\n", conn.password)
	return out.String()
}

// renderSorcery задаёт соответствие объектов pjsip таблицам базы.
//
// Контакты (`contact`) описаны в основной секции `[res_pjsip]`, а не в
// отдельной `[res_pjsip_registrar]`. Это проверено на живом запуске: с
// отдельной секцией регистрации проходили, но в таблицу `ps_contacts` не
// попадали вовсе — то есть после перезапуска станции все устройства
// оказывались «не на связи», пока не зарегистрируются заново. Секции в этом
// файле называются по объекту sorcery, а не по модулю, который им пользуется.
func renderSorcery() string {
	var out strings.Builder
	out.WriteString("; Файл собран сервером. Правки будут перезаписаны.\n")
	out.WriteString("; Соответствие объектов pjsip таблицам базы: только те объекты, которые\n")
	out.WriteString("; собраны здесь, станция читает из базы, остальные продолжают брать\n")
	out.WriteString("; из файлов (транспорты — из pjsip.conf).\n\n")
	out.WriteString("[res_pjsip]\n")
	out.WriteString("endpoint=realtime,ps_endpoints\n")
	out.WriteString("auth=realtime,ps_auths\n")
	out.WriteString("aor=realtime,ps_aors\n")
	out.WriteString("contact=realtime,ps_contacts\n")
	return out.String()
}

// renderExtConfig связывает имена таблиц, которыми оперирует sorcery, с
// драйвером базы.
//
// Без этого файла sorcery знает слово `realtime`, но не знает, какую таблицу
// за ним искать, и отвечает «объект не найден» — со стороны это выглядит так,
// будто абонент не заведён.
//
// Диалплан (`extensions`) подключается тем же способом: Asterisk читает
// правила вызова прямо из таблицы, и правка группы вызова в интерфейсе
// действует без перезагрузки плана набора.
func renderExtConfig() string {
	var out strings.Builder
	out.WriteString("; Файл собран сервером. Правки будут перезаписаны.\n")
	out.WriteString("; Какая таблица стоит за именем объекта: `ps_endpoints` — абоненты,\n")
	out.WriteString("; `extensions` — правила набора (см. switch => Realtime в extensions.conf).\n\n")
	out.WriteString("[settings]\n")
	out.WriteString("ps_endpoints => pgsql,ps_endpoints\n")
	out.WriteString("ps_auths => pgsql,ps_auths\n")
	out.WriteString("ps_aors => pgsql,ps_aors\n")
	out.WriteString("ps_contacts => pgsql,ps_contacts\n")
	out.WriteString("extensions => pgsql,extensions\n")
	return out.String()
}
