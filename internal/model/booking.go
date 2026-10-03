package model

import "time"

// Booking es el dato de la reserva: qué se reservó y bajo qué código.
// Es inmutable una vez creado.
type Booking struct {
	Code          string    `json:"confirmationCode"`
	ReservationID string    `json:"reservationId"`
	Guest         string    `json:"guest"`
	CreatedAt     time.Time `json:"createdAt"`
}
