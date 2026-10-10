package service

import (
	"testing"
	"time"
)

// Разбор события набора: номер звонящего лежит в CallerIDNum.
//
// Это неочевидно, и на этом уже была ошибка: у группового вызова поля
// DestCallerID* содержат номер ГРУППЫ (200), а не панели, и звонок
// записывался как «200 → 116». Поля ниже взяты из живого события
// Asterisk 18 при звонке панели 101 в группу.
func TestParseCallEventDialBegin(t *testing.T) {
	block := []string{
		"Event: DialBegin",
		"Privilege: call,all",
		"Channel: SIP/101-0000001a",
		"CallerIDNum: 101",
		"CallerIDName: panel 101",
		"DestChannel: PJSIP/300-0000001b",
		"DestCallerIDNum: 200",
		"DestCallerIDName: Все устройства",
		"DialString: 200",
		"DestLinkedid: 1791640345.229",
	}

	event, ok := parseCallEvent(block, time.Now())
	if !ok {
		t.Fatal("событие DialBegin не разобрано")
	}
	if event.Kind != "dial_begin" {
		t.Errorf("вид события = %q, ожидался dial_begin", event.Kind)
	}
	if event.CallerIDNum != "101" {
		t.Errorf("номер звонящего = %q, ожидался 101 (номер панели, а не группы)", event.CallerIDNum)
	}
	if event.CallerIDName != "panel 101" {
		t.Errorf("имя звонящего = %q", event.CallerIDName)
	}
	if event.DestChannel != "PJSIP/300-0000001b" {
		t.Errorf("канал получателя = %q", event.DestChannel)
	}
	if event.DialString != "200" {
		t.Errorf("набор = %q, ожидался 200", event.DialString)
	}
	if event.LinkedID != "1791640345.229" {
		t.Errorf("идентификатор вызова = %q", event.LinkedID)
	}
}

// Разбор окончания набора: именно по DialStatus решается, пропущен вызов.
func TestParseCallEventDialEnd(t *testing.T) {
	block := []string{
		"Event: DialEnd",
		"Privilege: call,all",
		"DestChannel: SIP/114-0000001b",
		"DestCallerIDNum: 101",
		"DialStatus: NOANSWER",
		"DestLinkedid: 1791640345.229",
	}

	event, ok := parseCallEvent(block, time.Now())
	if !ok {
		t.Fatal("событие DialEnd не разобрано")
	}
	if event.DialStatus != "NOANSWER" {
		t.Errorf("статус набора = %q, ожидался NOANSWER", event.DialStatus)
	}
	if event.LinkedID != "1791640345.229" {
		t.Errorf("идентификатор вызова = %q", event.LinkedID)
	}
}

// Заглушки Asterisk не должны попадать в журнал как имена.
func TestParseCallEventIgnoresUnknownCaller(t *testing.T) {
	block := []string{
		"Event: DialBegin",
		"DestChannel: Local/199@from-devices-00000001;1",
		"DestCallerIDNum: <unknown>",
		"DestCallerIDName: <unknown>",
		"DialString: 199@from-devices",
		"DestLinkedid: 1791639843.223",
	}

	event, ok := parseCallEvent(block, time.Now())
	if !ok {
		t.Fatal("событие не разобрано")
	}
	if event.CallerIDNum != "" || event.CallerIDName != "" {
		t.Errorf("заглушка <unknown> попала в событие: num=%q name=%q",
			event.CallerIDNum, event.CallerIDName)
	}
}

// События, которые нас не касаются, не должны превращаться в звонки.
func TestParseCallEventSkipsOtherEvents(t *testing.T) {
	block := []string{
		"Event: DeviceStateChange",
		"Device: Local/199@from-devices",
		"State: NOT_INUSE",
	}

	if _, ok := parseCallEvent(block, time.Now()); ok {
		t.Error("постороннее событие разобрано как звонок")
	}
}
