package model

import "errors"

var (
	// ErrNotFound: no existe un booking o delivery con ese código.
	ErrNotFound = errors.New("not found")
	// ErrCodeCollision: el código generado ya pertenece a otra reserva.
	// El service lo trata regenerando el código.
	ErrCodeCollision = errors.New("confirmation code collision")
	// ErrInvalidInput: request inválido (campos vacíos, formato de código incorrecto).
	ErrInvalidInput = errors.New("invalid input")
)
