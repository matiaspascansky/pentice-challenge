package model

import "time"

type Booking struct {
	Code          string    `json:"confirmationCode"`
	ReservationID string    `json:"reservationId"`
	Guest         string    `json:"guest"`
	CreatedAt     time.Time `json:"createdAt"`
}
