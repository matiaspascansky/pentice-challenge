package service

import (
	"crypto/rand"
	"strings"
)

// CodeLength es el largo del código de confirmación.
const CodeLength = 5

// codeAlphabet: minúsculas + dígitos (36^5 ≈ 60M combinaciones).
const codeAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// CodeGenerator genera códigos de confirmación. Es una interfaz para poder
// inyectar un generador determinista (o uno que colisione) en los tests.
type CodeGenerator interface {
	Generate() (string, error)
}

// CryptoCodeGenerator genera códigos con crypto/rand.
type CryptoCodeGenerator struct{}

// maxUnbiasedByte es el byte más alto que podemos mapear al alfabeto sin sesgo:
// 256 no es múltiplo de 36, así que descartamos los últimos 256%36 valores.
const maxUnbiasedByte = 255 - 256%len(codeAlphabet)

// Generate devuelve un código de CodeLength caracteres del alfabeto, usando
// rejection sampling para que todos los caracteres sean equiprobables.
func (CryptoCodeGenerator) Generate() (string, error) {
	out := make([]byte, 0, CodeLength)
	buf := make([]byte, CodeLength)
	for len(out) < CodeLength {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) > maxUnbiasedByte {
				continue // sesgaría el resultado: pedimos otro byte
			}
			out = append(out, codeAlphabet[int(b)%len(codeAlphabet)])
			if len(out) == CodeLength {
				break
			}
		}
	}
	return string(out), nil
}

// NormalizeCode deja el código en la forma canónica con la que se almacena:
// sin espacios alrededor y en minúsculas (los códigos son case-insensitive).
func NormalizeCode(code string) string {
	return strings.ToLower(strings.TrimSpace(code))
}

// ValidCode indica si code ya normalizado cumple el formato ^[a-z0-9]{5}$.
func ValidCode(code string) bool {
	if len(code) != CodeLength {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
