// Package repository define la persistencia del servicio detrás de interfaces,
// más el store en memoria que comparten las implementaciones memory y file.
package repository

import (
	"context"
	"time"

	"pentice-challenge/internal/model"
)

// BookingRepository persiste las reservas.
type BookingRepository interface {
	// CreateWithDelivery crea el booking y su delivery de forma atómica
	// (transactional outbox: si se guarda uno, se guarda el otro).
	//   - Si reservationID ya existe → devuelve el booking existente y created=false.
	//   - Si el código ya pertenece a otra reserva → model.ErrCodeCollision.
	CreateWithDelivery(ctx context.Context, b model.Booking, d model.Delivery) (existing model.Booking, created bool, err error)
	// GetByCode devuelve el booking o model.ErrNotFound.
	GetByCode(ctx context.Context, code string) (model.Booking, error)
}

// DeliveryRepository persiste el estado de las entregas.
type DeliveryRepository interface {
	// Due devuelve las entregas no-acked con NextAttemptAt <= now, hasta limit
	// (las más vencidas primero). limit <= 0 significa sin tope.
	Due(ctx context.Context, now time.Time, limit int) ([]model.Delivery, error)
	// RecordAttempt registra un intento y agenda el próximo.
	// No pisa una entrega ya acked. model.ErrNotFound si el código no existe.
	RecordAttempt(ctx context.Context, code string, nextAttemptAt time.Time, lastErr string) error
	// MarkAcked es idempotente: si ya está acked no hace nada y no falla.
	// model.ErrNotFound si el código no existe.
	MarkAcked(ctx context.Context, code string, at time.Time) error
	// Get devuelve la entrega o model.ErrNotFound.
	Get(ctx context.Context, code string) (model.Delivery, error)
}

// Repository es lo que consume el service: un único store implementa ambas
// interfaces, y ese lock compartido es lo que hace atómico booking + delivery.
type Repository interface {
	BookingRepository
	DeliveryRepository
}
