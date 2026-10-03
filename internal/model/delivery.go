package model

import "time"

// DeliveryStatus es el estado operativo de la entrega del código al huésped.
//
//	pending ──intento──▶ sent ──ack──▶ acked
//	                      │  ▲
//	                      └──┘ sin ack → reintento con backoff
type DeliveryStatus string

const (
	// DeliveryPending: nunca se intentó entregar.
	DeliveryPending DeliveryStatus = "pending"
	// DeliverySent: se intentó al menos una vez, esperando el ack del dispositivo.
	DeliverySent DeliveryStatus = "sent"
	// DeliveryAcked: el dispositivo confirmó. Estado terminal.
	DeliveryAcked DeliveryStatus = "acked"
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
