package service

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"pentice-challenge/internal/model"
	"pentice-challenge/internal/repository"
)

// Notifier entrega una notificación al dispositivo del huésped. La define el
// consumidor (este service) para poder mockearla en los tests; la
// implementación real es el cliente de webhook.
type Notifier interface {
	Notify(ctx context.Context, n model.Notification) error
}

// jitterSpread es la dispersión que le aplicamos al backoff (±20%), para que
// muchas entregas vencidas al mismo tiempo no salgan todas juntas.
const jitterSpread = 0.2

// DeliveryService entrega los códigos de confirmación y procesa los acks.
type DeliveryService struct {
	repo          repository.Repository
	notifier      Notifier
	backoffBase   time.Duration
	backoffMax    time.Duration
	notifyTimeout time.Duration
}

// NewDeliveryService arma el service con su política de reintentos.
func NewDeliveryService(repo repository.Repository, notifier Notifier, backoffBase, backoffMax, notifyTimeout time.Duration) *DeliveryService {
	return &DeliveryService{
		repo:          repo,
		notifier:      notifier,
		backoffBase:   backoffBase,
		backoffMax:    backoffMax,
		notifyTimeout: notifyTimeout,
	}
}

// Due devuelve las entregas que toca intentar ahora.
func (s *DeliveryService) Due(ctx context.Context, now time.Time, limit int) ([]model.Delivery, error) {
	return s.repo.Due(ctx, now, limit)
}

// Attempt hace un intento de entrega y agenda el siguiente.
//
// Clave del diseño: un 200 del webhook NO es un ack. Significa solamente que
// el request llegó, no que el dispositivo lo procesó. Por eso agendamos el
// próximo intento incluso cuando el envío salió bien: lo único que corta los
// reintentos es el POST /acks del dispositivo.
//
// Devuelve el error de la notificación (para que el worker lo loguee) o un
// error de persistencia, que es más grave.
func (s *DeliveryService) Attempt(ctx context.Context, d model.Delivery) error {
	booking, err := s.repo.GetByCode(ctx, d.Code)
	if err != nil {
		return fmt.Errorf("loading booking %s: %w", d.Code, err)
	}

	notifyCtx, cancel := context.WithTimeout(ctx, s.notifyTimeout)
	defer cancel()

	notifyErr := s.notifier.Notify(notifyCtx, model.Notification{
		Code:          booking.Code,
		ReservationID: booking.ReservationID,
	})

	var lastErr string
	if notifyErr != nil {
		lastErr = notifyErr.Error()
	}

	// El intento se registra incluso si estamos en pleno shutdown: si no, al
	// reiniciar volveríamos a intentar sin haber avanzado el backoff.
	recordCtx, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer recordCancel()

	next := s.NextAttemptAt(time.Now().UTC(), d.Attempts)
	if err := s.repo.RecordAttempt(recordCtx, d.Code, next, lastErr); err != nil {
		return fmt.Errorf("recording attempt for %s: %w", d.Code, err)
	}
	return notifyErr
}

// Ack procesa la confirmación del dispositivo. Es idempotente: el dispositivo
// re-ackea los duplicados para cortar el loop de reintentos, así que el mismo
// ack puede llegar muchas veces.
func (s *DeliveryService) Ack(ctx context.Context, code string) error {
	code = NormalizeCode(code)
	if !ValidCode(code) {
		return fmt.Errorf("%w: confirmation code must match ^[a-z0-9]{5}$", model.ErrInvalidInput)
	}
	return s.repo.MarkAcked(ctx, code, time.Now().UTC())
}

// Status devuelve el estado de la entrega (útil para la demo y el monitoreo).
func (s *DeliveryService) Status(ctx context.Context, code string) (model.Delivery, error) {
	code = NormalizeCode(code)
	if !ValidCode(code) {
		return model.Delivery{}, fmt.Errorf("%w: confirmation code must match ^[a-z0-9]{5}$", model.ErrInvalidInput)
	}
	return s.repo.Get(ctx, code)
}

// NextAttemptAt calcula cuándo reintentar: backoff exponencial con jitter,
// topeado en backoffMax.
//
// attempts es la cantidad de intentos previos, así que el primer reintento
// espera backoffBase. No hay máximo de intentos: la consigna pide reintentar
// hasta entregar, y el tope evita bombardear al dispositivo.
func (s *DeliveryService) NextAttemptAt(now time.Time, attempts int) time.Time {
	delay := s.backoffMax
	// 62 corre el riesgo de desbordar int64; mucho antes de eso ya topeamos.
	if attempts < 62 {
		if exp := s.backoffBase << attempts; exp > 0 && exp < s.backoffMax {
			delay = exp
		}
	}

	// Jitter ±20%, y re-topeamos para que backoffMax sea un techo real.
	jittered := float64(delay) * (1 + jitterSpread*(2*rand.Float64()-1))
	delay = time.Duration(jittered)
	if delay > s.backoffMax {
		delay = s.backoffMax
	}
	if delay < 0 {
		delay = s.backoffBase
	}

	return now.Add(delay)
}
