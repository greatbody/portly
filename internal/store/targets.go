package store

import (
	"database/sql"
)

type Target struct {
	ID          int64
	Slug        string
	Name        string
	Scheme      string
	Host        string
	Port        int
	Description string
	Enabled     bool
	CreatedAt   int64
	UpdatedAt   int64
}

func (s *Store) ListTargets() ([]*Target, error) {
	rows, err := s.DB.Query(`SELECT id,slug,name,scheme,host,port,description,enabled,created_at,updated_at FROM targets ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Target
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTargetBySlug(slug string) (*Target, error) {
	row := s.DB.QueryRow(`SELECT id,slug,name,scheme,host,port,description,enabled,created_at,updated_at FROM targets WHERE slug=?`, slug)
	t, err := scanTarget(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (s *Store) GetTarget(id int64) (*Target, error) {
	row := s.DB.QueryRow(`SELECT id,slug,name,scheme,host,port,description,enabled,created_at,updated_at FROM targets WHERE id=?`, id)
	t, err := scanTarget(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (s *Store) CreateTarget(t *Target) (*Target, error) {
	now := nowUnix()
	res, err := s.DB.Exec(`INSERT INTO targets(slug,name,scheme,host,port,description,enabled,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		t.Slug, t.Name, t.Scheme, t.Host, t.Port, t.Description, boolToInt(t.Enabled), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	t.ID = id
	t.CreatedAt = now
	t.UpdatedAt = now
	return t, nil
}

func (s *Store) UpdateTarget(t *Target) error {
	now := nowUnix()
	_, err := s.DB.Exec(`UPDATE targets SET slug=?,name=?,scheme=?,host=?,port=?,description=?,enabled=?,updated_at=? WHERE id=?`,
		t.Slug, t.Name, t.Scheme, t.Host, t.Port, t.Description, boolToInt(t.Enabled), now, t.ID)
	t.UpdatedAt = now
	return err
}

func (s *Store) DeleteTarget(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM targets WHERE id=?`, id)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTarget(r rowScanner) (*Target, error) {
	var t Target
	var enabled int
	if err := r.Scan(&t.ID, &t.Slug, &t.Name, &t.Scheme, &t.Host, &t.Port, &t.Description, &enabled, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.Enabled = enabled != 0
	return &t, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
