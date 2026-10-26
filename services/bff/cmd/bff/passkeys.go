package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/aibank/aibank/services/onboarding"
	"github.com/aibank/aibank/services/passkeys"
)

// setupPasskeys configura el Relying Party.
//
// El RP ID es el dominio al que quedan atadas las passkeys, y no se cambia:
// cambiarlo invalida todas las que existan. Por eso no tiene valor por defecto.
func setupPasskeys(ctx context.Context, db *sql.DB, onboardingStore *onboarding.Store, rpID string) (*passkeys.Service, error) {
	var origins []string
	for _, o := range strings.Split(os.Getenv("PASSKEY_RP_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	if len(origins) == 0 {
		origins = []string{"https://" + rpID}
	}

	store := passkeys.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrar el esquema de identidad: %w", err)
	}
	return passkeys.NewService(passkeys.Config{
		RPID:          rpID,
		RPDisplayName: env("PASSKEY_RP_NAME", "AIBank"),
		RPOrigins:     origins,
	}, store, onboardingStore)
}
