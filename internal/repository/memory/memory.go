// Package memory provee un repositorio puramente en memoria, para tests.
package memory

import (
	"context"

	"pentice-challenge/internal/repository"
)

// New devuelve un Store sin persistencia: el estado se pierde al terminar el
// proceso. Útil en los tests unitarios, donde no queremos tocar el disco.
func New() *repository.Store {
	// NewStore solo falla si el Snapshotter falla, y este nunca lo hace.
	s, err := repository.NewStore(context.Background(), nopSnapshotter{})
	if err != nil {
		panic(err) // inalcanzable
	}
	return s
}

type nopSnapshotter struct{}

func (nopSnapshotter) Load(context.Context) (repository.Snapshot, error) {
	return repository.Snapshot{}, nil
}

func (nopSnapshotter) Save(context.Context, repository.Snapshot) error {
	return nil
}
