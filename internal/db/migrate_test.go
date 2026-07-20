package db

import "testing"

func TestToMigrateURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "postgres scheme",
			in:   "postgres://user:pass@localhost:5432/db?sslmode=disable",
			want: "pgx5://user:pass@localhost:5432/db?sslmode=disable",
		},
		{
			name: "postgresql scheme",
			in:   "postgresql://user:pass@localhost:5432/db?sslmode=disable",
			want: "pgx5://user:pass@localhost:5432/db?sslmode=disable",
		},
		{
			name: "already pgx5",
			in:   "pgx5://user:pass@localhost:5432/db",
			want: "pgx5://user:pass@localhost:5432/db",
		},
		{
			name: "unrecognized scheme left alone",
			in:   "sqlite://local.db",
			want: "sqlite://local.db",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toMigrateURL(tt.in)
			if got != tt.want {
				t.Errorf("toMigrateURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
