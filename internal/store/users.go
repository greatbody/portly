package store

import "database/sql"

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    int64
}

func (s *Store) GetUserByName(username string) (*User, error) {
	var u User
	err := s.DB.QueryRow(`SELECT id,username,password_hash,created_at FROM users WHERE username=?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) CreateUser(username, passwordHash string) (*User, error) {
	res, err := s.DB.Exec(`INSERT INTO users(username,password_hash,created_at) VALUES(?,?,?)`,
		username, passwordHash, nowUnix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, PasswordHash: passwordHash, CreatedAt: nowUnix()}, nil
}

func (s *Store) UpdateUserPassword(id int64, passwordHash string) error {
	_, err := s.DB.Exec(`UPDATE users SET password_hash=? WHERE id=?`, passwordHash, id)
	return err
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}
