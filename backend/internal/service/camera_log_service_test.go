package service

import (
	"strings"
	"testing"
)

// Тесты разбора настроек syslog на камере.
//
// Содержимое файлов взято с живых камер, поэтому проверяется реальный
// формат, а не выдуманный: на новых сборках есть /etc/default/syslogd,
// на старых его нет и адрес зашит в init-скрипте.

func TestParseSyslogRemoteNewFirmware(t *testing.T) {
	// Новый формат: файл существует, строка раскомментирована.
	content := `# Settings for /etc/init.d/S01syslogd.
#
# This file ships with everything commented out.

#SYSLOG_SIZE=64
#SYSLOG_REMOTE=10.0.0.1:514

SYSLOG_REMOTE=192.168.1.111
`
	value, raw := parseSyslogRemote(content)

	if value != "192.168.1.111" {
		t.Errorf("адрес: %q, ждали %q", value, "192.168.1.111")
	}
	if raw != "SYSLOG_REMOTE=192.168.1.111" {
		t.Errorf("сырая строка: %q", raw)
	}
}

func TestParseSyslogRemoteIgnoresComments(t *testing.T) {
	// Комментарий с примером не должен приниматься за настройку:
	// иначе ненастроенная камера выглядела бы настроенной на чужой адрес.
	content := `#SYSLOG_REMOTE=10.0.0.1:514
`
	value, _ := parseSyslogRemote(content)
	if value != "" {
		t.Errorf("комментарий принят за настройку: %q", value)
	}
}

func TestParseSyslogRemoteQuoted(t *testing.T) {
	// Значение может быть закавычено: файл подключается через `source`,
	// и кавычки там допустимы.
	for _, content := range []string{
		`SYSLOG_REMOTE="192.168.1.111:514"`,
		`SYSLOG_REMOTE='192.168.1.111:514'`,
	} {
		value, _ := parseSyslogRemote(content)
		if value != "192.168.1.111:514" {
			t.Errorf("кавычки не убраны: %q из %q", value, content)
		}
	}
}

func TestParseSyslogRemoteEmpty(t *testing.T) {
	// Пустой файл — ненастроенная камера.
	value, _ := parseSyslogRemote("")
	if value != "" {
		t.Errorf("пустой файл дал значение: %q", value)
	}
}

func TestParseRemoteFromInitOldFirmware(t *testing.T) {
	// Адрес, подставляемый из переменной, — это выражение, а не адрес.
	// Настоящий адрес в таком случае лежит в /etc/default/syslogd,
	// поэтому из аргументов возвращается пусто, а не «$».
	initScript := `DAEMON_ARGS="-n -C${SYSLOG_SIZE:-64} -t${SYSLOG_REMOTE:+ -L -R ${SYSLOG_REMOTE}}"`
	if got := parseRemoteFromInit(initScript); got != "" {
		t.Errorf("подстановка из переменной принята за адрес: %q", got)
	}
}

func TestParseRemoteFromInitHardcoded(t *testing.T) {
	// Адрес зашит прямо в аргументах — встречается на камерах, которые
	// настраивали вручную.
	initScript := `DAEMON_ARGS="-n -C64 -t -L -R 192.168.1.111:514"`
	got := parseRemoteFromInit(initScript)
	if got != "192.168.1.111:514" {
		t.Errorf("адрес: %q, ждали %q", got, "192.168.1.111:514")
	}
}

func TestParseRemoteFromInitNoAddress(t *testing.T) {
	// Аргументов с -R нет: камера логи никуда не отправляет.
	initScript := `DAEMON_ARGS="-n -C${SYSLOG_SIZE:-64} -t"`
	if got := parseRemoteFromInit(initScript); got != "" {
		t.Errorf("найден адрес там, где его нет: %q", got)
	}
}

func TestSplitTwoParts(t *testing.T) {
	out := "SYSLOG_REMOTE=1.2.3.4\n---INIT---\nDAEMON_ARGS=\"-R 1.2.3.4\"\n"
	defaults, initScript := splitTwoParts(out)

	if !strings.Contains(defaults, "SYSLOG_REMOTE=1.2.3.4") {
		t.Errorf("файл настроек разобран неверно: %q", defaults)
	}
	if !strings.Contains(initScript, "DAEMON_ARGS") {
		t.Errorf("init-скрипт разобран неверно: %q", initScript)
	}
}

func TestSplitTwoPartsNoMarker(t *testing.T) {
	// Метки нет, если вторая команда ничего не вывела: это нормально
	// для камеры без init-скрипта.
	out := "SYSLOG_REMOTE=1.2.3.4\n"
	defaults, initScript := splitTwoParts(out)

	if defaults != out {
		t.Errorf("без метки всё должно попасть в первую часть: %q", defaults)
	}
	if initScript != "" {
		t.Errorf("вторая часть должна быть пустой: %q", initScript)
	}
}

func TestBuildSetSyslogScriptEnable(t *testing.T) {
	script := buildSetSyslogScript("192.168.1.111")

	// Скрипт должен уметь оба способа: файл для новых камер и правку
	// init-скрипта для старых.
	// Адрес прописывается через printf, а не готовой строкой: значение
	// приходит извне и подставляется как аргумент, а не в текст скрипта.
	if !strings.Contains(script, "SYSLOG_REMOTE=%s") || !strings.Contains(script, "'192.168.1.111'") {
		t.Error("адрес не прописывается в файл настроек")
	}
	if !strings.Contains(script, "mkdir -p /etc/default") {
		t.Error("каталог /etc/default не создаётся, на старых камерах его нет")
	}
	if !strings.Contains(script, "DAEMON_ARGS") {
		t.Error("init-скрипт не правится, на старых камерах настройка не сработает")
	}
	if !strings.Contains(script, "S01syslogd restart") {
		t.Error("служба не перезапускается, настройка не вступит в силу")
	}
	if !strings.Contains(script, "SYSLOG_SET_OK") {
		t.Error("нет метки успеха, отказ будет выглядеть как успех")
	}
}

func TestBuildSetSyslogScriptDisable(t *testing.T) {
	script := buildSetSyslogScript("")

	// Выключение должно убирать адрес из ОБОИХ мест: камера могла быть
	// настроена любым способом.
	if !strings.Contains(script, "sed -i") {
		t.Error("адрес не удаляется")
	}
	if strings.Contains(script, "-R ") {
		t.Error("адрес остался в аргументах запуска")
	}
	if !strings.Contains(script, "SYSLOG_SET_OK") {
		t.Error("нет метки успеха")
	}
}

func TestBuildSetSyslogScriptEscapesQuotes(t *testing.T) {
	// Значение приходит от сервера, но подставляется в shell-скрипт:
	// кавычка в адресе не должна разорвать команду.
	script := buildSetSyslogScript("10.0.0.1'; rm -rf /; echo '")

	if strings.Contains(script, "rm -rf") && !strings.Contains(script, `'\''`) {
		t.Error("кавычка не экранирована, команда может быть разорвана")
	}
}

func TestSeverityFromName(t *testing.T) {
	tests := []struct {
		name string
		want int
		ok   bool
	}{
		{"ошибка", SeverityError, true},
		{"ошибки", SeverityError, true},
		{"error", SeverityError, true},
		{"3", SeverityError, true},
		{"предупреждение", SeverityWarning, true},
		{"warning", SeverityWarning, true},
		{"отладка", SeverityDebug, true},
		{"сведения", SeverityInfo, true},
		{"авария", SeverityEmergency, true},
		{"чепуха", 0, false},
		{"", 0, false},
	}

	for _, tt := range tests {
		got, ok := SeverityFromName(tt.name)
		if ok != tt.ok || got != tt.want {
			t.Errorf("SeverityFromName(%q) = (%d, %v), ждали (%d, %v)",
				tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestFormatSeverity(t *testing.T) {
	if got := FormatSeverity(nil); got != "не указан" {
		t.Errorf("без уровня: %q", got)
	}
	sev := SeverityError
	if got := FormatSeverity(&sev); got != "Ошибка" {
		t.Errorf("уровень 3: %q", got)
	}
	unknown := 99
	if got := FormatSeverity(&unknown); got != "уровень 99" {
		t.Errorf("неизвестный уровень: %q", got)
	}
}
