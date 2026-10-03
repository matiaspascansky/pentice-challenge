// Package service contiene la lógica de negocio del servicio.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentice-challenge/internal/model"
	"pentice-challenge/internal/repository"
)

// maxCodeAttempts es cuántas veces regeneramos el código ante una colisión
// antes de darnos por vencidos. Con 36^5 códigos, llegar a 10 significa que el
// espacio está casi lleno (o que el generador está roto): es un error real.
const maxCodeAttempts = 10

// BookingService crea reservas y resuelve códigos de confirmación.
type BookingService struct {
	repo  repository.BookingRepository
	codes CodeGenerator
}

// NewBookingService arma el service con sus dependencias.
func NewBookingService(repo repository.BookingRepository, codes CodeGenerator) *BookingService {
	return &BookingService{repo: repo, codes: codes}
}

// Create registra la reserva junto con la tarea de entrega, de forma atómica.
//
// Es idempotente por reservationID: una reserva repetida devuelve el booking
// que ya existía con created=false, y no genera un segundo código ni una
// segunda entrega (el huésped nunca recibe dos confirmaciones distintas).
//
// El código generado va en el Booking devuelto para poder loguearlo del lado
// del servidor; no debe viajar en la respuesta HTTP.
func (s *BookingService) Create(ctx context.Context, reservationID, guest string) (model.Booking, bool, error) {
	reservationID = strings.TrimSpace(reservationID)
	guest = strings.TrimSpace(guest)
	if reservationID == "" {
		return model.Booking{}, false, fmt.Errorf("%w: reservationId is required", model.ErrInvalidInput)
	}
	if guest == "" {
		return model.Booking{}, false, fmt.Errorf("%w: guest is required", model.ErrInvalidInput)
	}

	for attempt := 0; attempt < maxCodeAttempts; attempt++ {
		code, err := s.codes.Generate()
		if err != nil {
			return model.Booking{}, false, fmt.Errorf("generating code: %w", err)
		}

		now := time.Now().UTC()
		b := model.Booking{
			Code:          code,
			ReservationID: reservationID,
			Guest:         guest,
			CreatedAt:     now,
		}
		// NextAttemptAt=now: el worker la toma en el próximo tick.
		d := model.Delivery{
			Code:          code,
			Status:        model.DeliveryPending,
			Attempts:      0,
			NextAttemptAt: now,
		}

		existing, created, err := s.repo.CreateWithDelivery(ctx, b, d)
		if errors.Is(err, model.ErrCodeCollision) {
			continue // código ya usado por otra reserva: probamos con otro
		}
		if err != nil {
			return model.Booking{}, false, err
		}
		return existing, created, nil
	}

	return model.Booking{}, false, fmt.Errorf("could not generate a unique code after %d attempts", maxCodeAttempts)
}

// GetByCode resuelve un código de confirmación.
// Devuelve model.ErrInvalidInput si el formato no es válido y model.ErrNotFound
// si no existe.
func (s *BookingService) GetByCode(ctx context.Context, code string) (model.Booking, error) {
	code = NormalizeCode(code)
	if !ValidCode(code) {
		return model.Booking{}, fmt.Errorf("%w: confirmation code must match ^[a-z0-9]{5}$", model.ErrInvalidInput)
	}
	return s.repo.GetByCode(ctx, code)
}
