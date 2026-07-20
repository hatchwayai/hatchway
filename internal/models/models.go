package models

import "time"

type User struct {
	ID        string    `json:"id"`
	Email     *string   `json:"email"`
	Name      *string   `json:"name"`
	IsAdmin   bool      `json:"is_admin"`
	CreatedAt time.Time `json:"created_at"`
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
