package model

import "errors"

var (
	ErrNotFound      = errors.New("not found")
	ErrCodeCollision = errors.New("confirmation code collision")
	ErrInvalidInput  = errors.New("invalid input")
)
