package testutil

import "testing"

func TestValidateTestDatabaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		connStr string
		wantErr bool
	}{
		{
			name:    "URL",
			connStr: "postgres://hatchway:secret@localhost:5432/hatchway_test?sslmode=disable",
		},
		{
			name:    "keyword DSN",
			connStr: "host=localhost port=5432 user=hatchway dbname=integration_test sslmode=disable",
		},
		{
			name:    "production database",
			connStr: "postgres://hatchway:secret@localhost:5432/hatchway",
			wantErr: true,
		},
		{
			name:    "test prefix is insufficient",
			connStr: "postgres://hatchway:secret@localhost:5432/test_hatchway",
			wantErr: true,
		},
		{
			name:    "empty",
			connStr: "",
			wantErr: true,
		},
		{
			name:    "malformed",
			connStr: "://not-a-dsn",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateTestDatabaseURL(tt.connStr)
			if tt.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}
