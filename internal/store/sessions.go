package store

import (
	"database/sql"
	"time"
)

type Session struct {
	Token     string
	UserID    int64
	CreatedAt int64
	ExpiresAt int64
}

func (s *Store) CreateSession(userID int64, token string, ttl time.Duration) (*Session, error) {
	now := time.Now()
	exp := now.Add(ttl)
	_, err := s.DB.Exec(`INSERT INTO sessions(token,user_id,created_at,expires_at) VALUES(?,?,?,?)`,
		token, userID, now.Unix(), exp.Unix())
	if err != nil {
		return nil, err
	}
	return &Session{Token: token, UserID: userID, CreatedAt: now.Unix(), ExpiresAt: exp.Unix()}, nil
}

func (s *Store) GetSession(token string) (*Session, error) {
	var ses Session
	err := s.DB.QueryRow(`SELECT token,user_id,created_at,expires_at FROM sessions WHERE token=?`, token).
		Scan(&ses.Token, &ses.UserID, &ses.CreatedAt, &ses.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ses.ExpiresAt < time.Now().Unix() {
		_, _ = s.DB.Exec(`DELETE FROM sessions WHERE token=?`, token)
		return nil, nil
	}
	return &ses, nil
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE token=?`, token)
	return err
}

func (s *Store) CleanupSessions() error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	return err
}
