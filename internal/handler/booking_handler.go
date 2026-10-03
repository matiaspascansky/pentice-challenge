package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"pentice-challenge/internal/model"
	"pentice-challenge/internal/service"
)

// maxBodyBytes acota el cuerpo que aceptamos: los requests son diminutos.
const maxBodyBytes = 64 << 10

// BookingHandler expone la creación de reservas y la consulta por código.
type BookingHandler struct {
	bookings *service.BookingService
}

// NewBookingHandler arma el handler.
func NewBookingHandler(bookings *service.BookingService) *BookingHandler {
	return &BookingHandler{bookings: bookings}
}

type createBookingRequest struct {
	ReservationID string `json:"reservationId"`
	Guest         string `json:"guest"`
}

type createBookingResponse struct {
	ReservationID string `json:"reservationId"`
	Status        string `json:"status"`
}

// Create maneja POST /bookings.
//
// Responde 202 Accepted sin el código de confirmación: el código se entrega
// out-of-band al dispositivo del huésped, nunca en esta respuesta.
func (h *BookingHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createBookingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	booking, created, err := h.bookings.Create(r.Context(), req.ReservationID, req.Guest)
	switch {
	case errors.Is(err, model.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		slog.Error("creating booking", "reservationId", req.ReservationID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}

	// El código se loguea del lado del servidor, no se devuelve.
	slog.Info("booking accepted",
		"reservationId", booking.ReservationID,
		"confirmationCode", booking.Code,
		"created", created)

	// 202 también cuando ya existía: para el cliente el efecto es el mismo.
	writeJSON(w, http.StatusAccepted, createBookingResponse{
		ReservationID: booking.ReservationID,
		Status:        "accepted",
	})
}

// Get maneja GET /{code}.
func (h *BookingHandler) Get(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	booking, err := h.bookings.GetByCode(r.Context(), code)
	switch {
	case errors.Is(err, model.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, model.ErrNotFound):
		writeError(w, http.StatusNotFound, "confirmation code not found")
		return
	case err != nil:
		slog.Error("getting booking", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read booking")
		return
	}

	writeJSON(w, http.StatusOK, booking)
}
