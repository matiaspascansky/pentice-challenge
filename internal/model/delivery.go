package model

import "time"

type DeliveryStatus string

const (
	DeliveryPending DeliveryStatus = "pending"
	DeliverySent    DeliveryStatus = "sent"
	DeliveryAcked   DeliveryStatus = "acked"
)

// Delivery es la tarea de avisarle al huésped. Cambia de estado hasta el ack.
type Delivery struct {
	Code          string         `json:"confirmationCode"`
	Status        DeliveryStatus `json:"status"`
	Attempts      int            `json:"attempts"`
	NextAttemptAt time.Time      `json:"nextAttemptAt"`
	LastError     string         `json:"lastError,omitempty"`
	AckedAt       *time.Time     `json:"ackedAt,omitempty"`
}

type Notification struct {
	Code          string `json:"confirmationCode"`
	ReservationID string `json:"reservationId"`
}
