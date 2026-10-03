// Package file provee un repositorio que sobrevive reinicios guardando el
// estado completo en un archivo JSON.
package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"pentice-challenge/internal/repository"
)

// New devuelve un Store respaldado por el archivo path, cargando el estado
// previo si existe. Las entregas pendientes se retoman al arrancar.
func New(ctx context.Context, path string) (*repository.Store, error) {
	return repository.NewStore(ctx, snapshotter{path: path})
}

type snapshotter struct {
	path string
}

// Load lee el snapshot. Un archivo inexistente es un arranque en frío, no un
// error; un archivo corrupto sí lo es (preferimos no arrancar perdiendo datos).
func (s snapshotter) Load(ctx context.Context) (repository.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return repository.Snapshot{}, err
	}

	raw, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return repository.Snapshot{}, nil
	}
	if err != nil {
		return repository.Snapshot{}, err
	}
	if len(raw) == 0 {
		return repository.Snapshot{}, nil
	}

	var snap repository.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return repository.Snapshot{}, fmt.Errorf("corrupt snapshot %s: %w", s.path, err)
	}
	return snap, nil
}

// Save escribe el snapshot de forma atómica: archivo temporal en el mismo
// directorio + fsync + rename. Así un crash a mitad de escritura deja el
// snapshot anterior intacto en lugar de un archivo truncado.
func (s snapshotter) Save(ctx context.Context, snap repository.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Si algo falla después de crear el temporal, no lo dejamos tirado.
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	tmpName = "" // el rename ya se lo llevó

	return syncDir(dir)
}

// syncDir fuerza el flush de la entrada de directorio para que el rename
// también sobreviva a un corte de energía.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
