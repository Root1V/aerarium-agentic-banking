package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/aibank/aibank/services/devices"
	"github.com/aibank/aibank/services/onboarding"
	"github.com/aibank/aibank/services/passkeys"
	"github.com/aibank/aibank/services/sessions"
)

// setupPasskeys configura el Relying Party.
//
// El RP ID es el dominio al que quedan atadas las passkeys, y no se cambia:
// cambiarlo invalida todas las que existan. Por eso no tiene valor por defecto.
func setupPasskeys(ctx context.Context, db *sql.DB, onboardingStore *onboarding.Store, sessionStore *sessions.Store, rpID string) (*passkeys.Service, error) {
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
	}, store, onboardingStore, sessionStore)
}

// setupDevices prepara las sesiones y los teléfonos vinculados. No dependen de
// ningún dominio: funcionan en cuanto hay base de datos.
func setupDevices(ctx context.Context, db *sql.DB, onboardingStore *onboarding.Store) (*sessions.Store, *devices.Service, error) {
	sessionStore := sessions.NewStore(db)
	deviceStore := devices.NewStore(db)
	for _, m := range []interface{ Migrate(context.Context) error }{sessionStore, deviceStore} {
		if err := m.Migrate(ctx); err != nil {
			return nil, nil, fmt.Errorf("migrar el esquema de identidad: %w", err)
		}
	}
	return sessionStore, devices.NewService(devices.Config{}, deviceStore, onboardingStore, sessionStore), nil
}
