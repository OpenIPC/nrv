// Package vector — хранение и сравнение векторов событий.
//
// Векторы лежат в базе как bytea (float32 little-endian), а сравниваются
// в памяти приложения. Для нашего объёма — сотни событий в сутки, тысячи
// за месяц — это быстрее и проще, чем поднимать отдельный индекс pgvector:
// кандидаты всё равно ограничены днём и камерой, а перебор десятков тысяч
// чисел занимает миллисекунды.
package vector

import (
	"encoding/binary"
	"math"
)

// Encode упаковывает вектор в двоичный вид для хранения.
func Encode(vec []float32) []byte {
	out := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(v))
	}
	return out
}

// Decode разбирает вектор из двоичного вида.
//
// Битые данные не считаются ошибкой выше по стеку: такой вектор просто
// пропускается, а поиск работает по остальным. Иначе одна испорченная
// строка ломала бы весь поиск по архиву.
func Decode(raw []byte) []float32 {
	if len(raw) < 4 || len(raw)%4 != 0 {
		return nil
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out
}

// Cosine считает косинусное сходство двух векторов.
//
// Векторы из модели уже нормированы, но норму всё равно считаем: если
// раздача изменится (другая модель, обрезка), сравнение останется верным,
// а не будет молча выдавать мусор.
func Cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		normA += x * x
		normB += y * y
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}
