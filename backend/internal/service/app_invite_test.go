package service

import (
	"strings"
	"testing"
)

// Код подключения показывают на экране и набирают руками: проверяем, что он
// состоит только из символов алфавита и не повторяется.
func TestNewInviteCodeUsesSafeAlphabet(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := newInviteCode()
		if err != nil {
			t.Fatalf("код не выдан: %v", err)
		}
		if len(code) != inviteCodeLength {
			t.Fatalf("длина кода %d, ожидалась %d", len(code), inviteCodeLength)
		}
		for _, r := range code {
			if !strings.ContainsRune(inviteAlphabet, r) {
				t.Fatalf("в коде символ %q, которого нет в алфавите", r)
			}
		}
		if seen[code] {
			t.Fatalf("код %q выдан дважды за 200 попыток", code)
		}
		seen[code] = true
	}
}

// Код вводят с разделителем, пробелами и в нижнем регистре — всё это должно
// приводить к одному и тому же коду, иначе верный код выглядит как неверный.
func TestNormalizeInviteCode(t *testing.T) {
	cases := map[string]string{
		"ABCD2345":    "ABCD2345",
		"abcd2345":    "ABCD2345",
		"ABCD-2345":   "ABCD2345",
		" abcd 2345 ": "ABCD2345",
		"":            "",
	}
	for input, want := range cases {
		if got := normalizeInviteCode(input); got != want {
			t.Errorf("normalizeInviteCode(%q) = %q, ожидалось %q", input, got, want)
		}
	}
}

// Разделитель ставится только при показе и только для кода нужной длины:
// иначе он попадал бы в код произвольной строки, которую ввёл человек.
func TestFormatInviteCode(t *testing.T) {
	if got := FormatInviteCode("ABCD2345"); got != "ABCD-2345" {
		t.Errorf("FormatInviteCode = %q, ожидалось ABCD-2345", got)
	}
	if got := FormatInviteCode("короткий"); got != "короткий" {
		t.Errorf("FormatInviteCode испортил строку: %q", got)
	}
}
