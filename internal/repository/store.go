package repository

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"pentice-challenge/internal/model"
)

// Snapshot es el estado completo del store, tal como se persiste.
type Snapshot struct {
	Bookings   []model.Booking  `json:"bookings"`
	Deliveries []model.Delivery `json:"deliveries"`
}

// Snapshotter persiste y recupera el estado completo. La implementación de
// archivo escribe de forma atómica; la de memoria no hace nada.
type Snapshotter interface {
	// Load devuelve el estado guardado, o un Snapshot vacío si no hay nada.
	Load(ctx context.Context) (Snapshot, error)
	Save(ctx context.Context, s Snapshot) error
}

// Store es el estado en memoria protegido por un único mutex, con la
// persistencia delegada al Snapshotter. Implementa Repository.
//
// La persistencia ocurre dentro del lock para que el archivo nunca quede
// adelantado ni atrasado respecto de la memoria. A esta escala alcanza; con
// más volumen habría que pasar a un log append-only (ver README).
type Store struct {
	mu            sync.Mutex
	byCode        map[string]model.Booking
	byReservation map[string]string // reservationID → code
	deliveries    map[string]model.Delivery
	snap          Snapshotter
}

// NewStore arma el store y restaura el estado previo del Snapshotter. Las
// entregas no-acked que quedaron en el snapshot se retoman solas: el worker
// las vuelve a ver en Due().
func NewStore(ctx context.Context, snap Snapshotter) (*Store, error) {
	s := &Store{
		byCode:        make(map[string]model.Booking),
		byReservation: make(map[string]string),
		deliveries:    make(map[string]model.Delivery),
		snap:          snap,
	}

	loaded, err := snap.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading snapshot: %w", err)
	}
	for _, b := range loaded.Bookings {
		s.byCode[b.Code] = b
		s.byReservation[b.ReservationID] = b.Code
	}
	for _, d := range loaded.Deliveries {
		s.deliveries[d.Code] = d
	}
	return s, nil
}

// CreateWithDelivery implementa BookingRepository.
func (s *Store) CreateWithDelivery(ctx context.Context, b model.Booking, d model.Delivery) (model.Booking, bool, error) {
	if err := ctx.Err(); err != nil {
		return model.Booking{}, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Idempotencia: la misma reserva nunca recibe un segundo código.
	if code, ok := s.byReservation[b.ReservationID]; ok {
		return s.byCode[code], false, nil
	}
	if _, taken := s.byCode[b.Code]; taken {
		return model.Booking{}, false, model.ErrCodeCollision
	}

	s.byCode[b.Code] = b
	s.byReservation[b.ReservationID] = b.Code
	s.deliveries[d.Code] = d

	if err := s.persistLocked(ctx); err != nil {
		// Revertimos para que la memoria siga reflejando lo persistido.
		delete(s.byCode, b.Code)
		delete(s.byReservation, b.ReservationID)
		delete(s.deliveries, d.Code)
		return model.Booking{}, false, err
	}
	return b, true, nil
}

// GetByCode implementa BookingRepository.
func (s *Store) GetByCode(ctx context.Context, code string) (model.Booking, error) {
	if err := ctx.Err(); err != nil {
		return model.Booking{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	b, ok := s.byCode[code]
	if !ok {
		return model.Booking{}, model.ErrNotFound
	}
	return b, nil
}

// Due implementa DeliveryRepository.
func (s *Store) Due(ctx context.Context, now time.Time, limit int) ([]model.Delivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var due []model.Delivery
	for _, d := range s.deliveries {
		if d.Status != model.DeliveryAcked && !d.NextAttemptAt.After(now) {
			due = append(due, d)
		}
	}
	// Las más vencidas primero; el código desempata para que sea determinista.
	sort.Slice(due, func(i, j int) bool {
		if due[i].NextAttemptAt.Equal(due[j].NextAttemptAt) {
			return due[i].Code < due[j].Code
		}
		return due[i].NextAttemptAt.Before(due[j].NextAttemptAt)
	})
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	return due, nil
}

// RecordAttempt implementa DeliveryRepository.
func (s *Store) RecordAttempt(ctx context.Context, code string, nextAttemptAt time.Time, lastErr string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.deliveries[code]
	if !ok {
		return model.ErrNotFound
	}
	// El ack puede haber llegado mientras el intento estaba en vuelo: acked es
	// terminal y no se pisa.
	if d.Status == model.DeliveryAcked {
		return nil
	}

	previous := d
	d.Status = model.DeliverySent
	d.Attempts++
	d.NextAttemptAt = nextAttemptAt
	d.LastError = lastErr
	s.deliveries[code] = d

	if err := s.persistLocked(ctx); err != nil {
		s.deliveries[code] = previous
		return err
	}
	return nil
}

// MarkAcked implementa DeliveryRepository.
func (s *Store) MarkAcked(ctx context.Context, code string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.deliveries[code]
	if !ok {
		return model.ErrNotFound
	}
	if d.Status == model.DeliveryAcked {
		return nil // idempotente: el dispositivo re-ackea los duplicados
	}

	previous := d
	d.Status = model.DeliveryAcked
	d.AckedAt = &at
	d.LastError = ""
	s.deliveries[code] = d

	if err := s.persistLocked(ctx); err != nil {
		s.deliveries[code] = previous
		return err
	}
	return nil
}

// Get implementa DeliveryRepository.
func (s *Store) Get(ctx context.Context, code string) (model.Delivery, error) {
	if err := ctx.Err(); err != nil {
		return model.Delivery{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.deliveries[code]
	if !ok {
		return model.Delivery{}, model.ErrNotFound
	}
	return d, nil
}

// persistLocked guarda el estado completo. Requiere s.mu tomado.
func (s *Store) persistLocked(ctx context.Context) error {
	return s.snap.Save(ctx, s.snapshotLocked())
}

// snapshotLocked copia el estado a un Snapshot ordenado (salida estable).
func (s *Store) snapshotLocked() Snapshot {
	snap := Snapshot{
		Bookings:   make([]model.Booking, 0, len(s.byCode)),
		Deliveries: make([]model.Delivery, 0, len(s.deliveries)),
	}
	for _, b := range s.byCode {
		snap.Bookings = append(snap.Bookings, b)
	}
	for _, d := range s.deliveries {
		snap.Deliveries = append(snap.Deliveries, d)
	}
	sort.Slice(snap.Bookings, func(i, j int) bool { return snap.Bookings[i].Code < snap.Bookings[j].Code })
	sort.Slice(snap.Deliveries, func(i, j int) bool { return snap.Deliveries[i].Code < snap.Deliveries[j].Code })
	return snap
}
