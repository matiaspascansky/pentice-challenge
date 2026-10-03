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

// DeliveryHandler expone el ack del dispositivo y el estado de la entrega.
type DeliveryHandler struct {
	deliveries *service.DeliveryService
}

// NewDeliveryHandler arma el handler.
func NewDeliveryHandler(deliveries *service.DeliveryService) *DeliveryHandler {
	return &DeliveryHandler{deliveries: deliveries}
}

type ackRequest struct {
	Code string `json:"confirmationCode"`
}

// Ack maneja POST /acks: el dispositivo confirma que procesó el código.
//
// Es lo único que corta los reintentos. Es idempotente, porque el dispositivo
// re-ackea las notificaciones duplicadas (por si el ack anterior se perdió).
func (h *DeliveryHandler) Ack(w http.ResponseWriter, r *http.Request) {
	var req ackRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	err := h.deliveries.Ack(r.Context(), req.Code)
	switch {
	case errors.Is(err, model.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, model.ErrNotFound):
		writeError(w, http.StatusNotFound, "confirmation code not found")
		return
	case err != nil:
		slog.Error("processing ack", "error", err)
		writeError(w, http.StatusInternalServerError, "could not process ack")
		return
	}

	slog.Info("delivery acked", "confirmationCode", service.NormalizeCode(req.Code))
	w.WriteHeader(http.StatusNoContent)
}

// Status maneja GET /bookings/{code}/delivery: estado operativo de la entrega.
// No está en la consigna; sirve para observar los reintentos en la demo.
func (h *DeliveryHandler) Status(w http.ResponseWriter, r *http.Request) {
	delivery, err := h.deliveries.Status(r.Context(), chi.URLParam(r, "code"))
	switch {
	case errors.Is(err, model.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, model.ErrNotFound):
		writeError(w, http.StatusNotFound, "confirmation code not found")
		return
	case err != nil:
		slog.Error("reading delivery status", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read delivery")
		return
	}

	writeJSON(w, http.StatusOK, delivery)
}
