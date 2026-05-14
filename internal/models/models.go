package models

import "time"

type User struct {
	ID        string    `json:"id"`
	Email     *string   `json:"email"`
	Name      *string   `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type APIToken struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"token_prefix"`
	TokenHash   string     `json:"-"`
	RevokedAt   *time.Time `json:"revoked_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

type Tunnel struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Type       string     `json:"type"`
	PublicHost *string    `json:"public_host"`
	PublicPort *int       `json:"public_port"`
	LocalHost  string     `json:"local_host"`
	LocalPort  int        `json:"local_port"`
	Status     string     `json:"status"`
	ExpiresAt  *time.Time `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type TunnelRuntimeToken struct {
	ID          string     `json:"id"`
	TunnelID    string     `json:"tunnel_id"`
	TokenPrefix string     `json:"token_prefix"`
	TokenHash   string     `json:"-"`
	ExpiresAt   time.Time  `json:"expires_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	UseCount    int64      `json:"use_count"`
	RevokedAt   *time.Time `json:"revoked_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

type TunnelEvent struct {
	ID        int64     `json:"id"`
	TunnelID  string    `json:"tunnel_id"`
	EventType string    `json:"event_type"`
	Payload   []byte    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

type IdempotencyKey struct {
	TokenID        string     `json:"token_id"`
	Key            string     `json:"key"`
	RequestHash    string     `json:"request_hash"`
	ResponseStatus *int       `json:"response_status"`
	ResponseBody   []byte     `json:"response_body"`
	CompletedAt    *time.Time `json:"completed_at"`
	CreatedAt      time.Time  `json:"created_at"`
}
