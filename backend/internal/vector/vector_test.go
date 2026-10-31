package vector

import (
	"math"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	source := []float32{0, 1, -1, 0.5, -0.25, 3.14159}
	decoded := Decode(Encode(source))

	if len(decoded) != len(source) {
		t.Fatalf("длина %d, ожидалось %d", len(decoded), len(source))
	}
	for i := range source {
		// float32 ↔ byte — точное преобразование, поэтому сравниваем как есть.
		if decoded[i] != source[i] {
			t.Errorf("элемент %d: получено %v, ожидалось %v", i, decoded[i], source[i])
		}
	}
}

func TestDecodeRejectsBrokenData(t *testing.T) {
	// Битая строка не должна ронять поиск по всему архиву: такой вектор
	// просто пропускается.
	for _, raw := range [][]byte{nil, {}, {1, 2, 3}, {1, 2, 3, 4, 5}} {
		if got := Decode(raw); got != nil {
			t.Errorf("Decode(%v) = %v, ожидалось nil", raw, got)
		}
	}
}

func TestCosine(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"одинаковые векторы", []float32{1, 0, 0}, []float32{1, 0, 0}, 1},
		{"перпендикулярные", []float32{1, 0}, []float32{0, 1}, 0},
		{"противоположные", []float32{1, 0}, []float32{-1, 0}, -1},
		{"разной длины", []float32{1, 0, 0}, []float32{1, 0}, 0},
		{"нулевой вектор", []float32{0, 0}, []float32{1, 1}, 0},
		{"немасштабированные", []float32{2, 0}, []float32{5, 0}, 1},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := float64(Cosine(testCase.a, testCase.b))
			if math.Abs(got-testCase.want) > 1e-6 {
				t.Errorf("Cosine = %v, ожидалось %v", got, testCase.want)
			}
		})
	}
}
