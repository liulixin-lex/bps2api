package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabasePoolDefaultsAndExplicitOverride(t *testing.T) {
	t.Run("single-instance defaults match compose", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, 50, cfg.Database.MaxOpenConns)
		require.Equal(t, 10, cfg.Database.MaxIdleConns)
	})
	t.Run("explicit capacity remains configurable", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		t.Setenv("DATABASE_MAX_OPEN_CONNS", "80")
		t.Setenv("DATABASE_MAX_IDLE_CONNS", "20")
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, 80, cfg.Database.MaxOpenConns)
		require.Equal(t, 20, cfg.Database.MaxIdleConns)
	})
}
