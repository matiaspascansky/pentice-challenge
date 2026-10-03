package service_test

import (
	"strings"
	"testing"

	"pentice-challenge/internal/service"
)

func TestCryptoCodeGeneratorFormat(t *testing.T) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var gen service.CryptoCodeGenerator

	seen := make(map[string]bool)
	for i := 0; i < 20_000; i++ {
		code, err := gen.Generate()
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if len(code) != service.CodeLength {
			t.Fatalf("Generate() = %q, want length %d", code, service.CodeLength)
		}
		for _, c := range code {
			if !strings.ContainsRune(alphabet, c) {
				t.Fatalf("Generate() = %q, contains %q outside the alphabet", code, c)
			}
		}
		seen[code] = true
	}

	// No es un test de aleatoriedad, solo una red de seguridad: un generador
	// roto (constante, o con un alfabeto diminuto) colisionaría muchísimo.
	if len(seen) < 19_900 {
		t.Errorf("got %d unique codes out of 20000, suspiciously few", len(seen))
	}
}

func TestNormalizeCode(t *testing.T) {
	tests := map[string]string{
		"a2b34":     "a2b34",
		"A2B34":     "a2b34",
		"  a2b34  ": "a2b34",
		"A2b34\n":   "a2b34",
	}
	for in, want := range tests {
		if got := service.NormalizeCode(in); got != want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidCode(t *testing.T) {
	tests := map[string]bool{
		"a2b34":  true,
		"00000":  true,
		"zzzzz":  true,
		"a2b3":   false, // corto
		"a2b345": false, // largo
		"a2b3!":  false, // símbolo
		"a2b3-":  false,
		"A2B34":  false, // debe normalizarse antes
		"":       false,
	}
	for in, want := range tests {
		if got := service.ValidCode(in); got != want {
			t.Errorf("ValidCode(%q) = %v, want %v", in, got, want)
		}
	}
}
