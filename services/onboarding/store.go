package onboarding

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/aibank/aibank/adapters/kyc"
)

//go:embed schema.sql
var schemaSQL string

// ErrNotFound: el expediente no existe.
var ErrNotFound = errors.New("expediente no encontrado")

// Store persiste expedientes y sus pasos.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Migrate crea el esquema si falta.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("aplicar esquema de onboarding: %w", err)
	}
	return nil
}

// Create inserta el expediente, o devuelve el existente si la referencia externa
// ya fue usada: reenviar una solicitud no debe abrir un segundo expediente.
func (s *Store) Create(ctx context.Context, app *Application) (*Application, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO onboarding.applications
			(id, external_ref, customer_id, product_code, status,
			 full_name, document_type, document_number, date_of_birth, country_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (external_ref) DO NOTHING`,
		app.ID, app.ExternalRef, app.CustomerID, app.ProductCode, app.Status,
		app.Applicant.FullName, app.Applicant.DocumentType, app.Applicant.DocumentNumber,
		app.Applicant.DateOfBirth, app.Applicant.CountryCode,
	)
	if err != nil {
		return nil, fmt.Errorf("crear expediente: %w", err)
	}
	return s.ByExternalRef(ctx, app.ExternalRef)
}

func (s *Store) Get(ctx context.Context, id string) (*Application, error) {
	return s.queryOne(ctx, `WHERE id = $1`, id)
}

func (s *Store) ByExternalRef(ctx context.Context, ref string) (*Application, error) {
	return s.queryOne(ctx, `WHERE external_ref = $1`, ref)
}

func (s *Store) queryOne(ctx context.Context, where string, arg any) (*Application, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, external_ref, customer_id, product_code, status,
		       full_name, document_type, document_number, date_of_birth, country_code,
		       COALESCE(account_id::text, ''), COALESCE(decision_reason, ''),
		       created_at, updated_at
		FROM onboarding.applications `+where, arg)

	var app Application
	var dob time.Time
	err := row.Scan(&app.ID, &app.ExternalRef, &app.CustomerID, &app.ProductCode, &app.Status,
		&app.Applicant.FullName, &app.Applicant.DocumentType, &app.Applicant.DocumentNumber,
		&dob, &app.Applicant.CountryCode,
		&app.AccountID, &app.DecisionReason, &app.CreatedAt, &app.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("leer expediente: %w", err)
	}
	app.Applicant.DateOfBirth = dob
	return &app, nil
}

// UpdateStatus cambia el estado y la razón de la decisión.
func (s *Store) UpdateStatus(ctx context.Context, id string, status Status, reason string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE onboarding.applications
		SET status = $2, decision_reason = NULLIF($3, ''), updated_at = now()
		WHERE id = $1`, id, status, reason)
	if err != nil {
		return fmt.Errorf("actualizar estado: %w", err)
	}
	return nil
}

// SetAccount registra la cuenta abierta y aprueba el expediente.
func (s *Store) SetAccount(ctx context.Context, id, accountID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE onboarding.applications
		SET account_id = $2, status = $3, updated_at = now()
		WHERE id = $1`, id, accountID, StatusApproved)
	if err != nil {
		return fmt.Errorf("registrar cuenta: %w", err)
	}
	return nil
}

// RecordStep guarda el resultado de un paso. Es idempotente: si el paso ya está
// registrado, no lo sobrescribe — el rastro de auditoría no se reescribe.
func (s *Store) RecordStep(ctx context.Context, applicationID string, record StepRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO onboarding.application_steps
			(application_id, step, outcome, provider, provider_ref, detail)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (application_id, step) DO NOTHING`,
		applicationID, record.Step, record.Outcome,
		nullable(record.Provider), nullable(record.ProviderRef), nullable(record.Detail))
	if err != nil {
		return fmt.Errorf("registrar paso %s: %w", record.Step, err)
	}
	return nil
}

// Steps devuelve el rastro de auditoría del expediente, en orden.
func (s *Store) Steps(ctx context.Context, applicationID string) ([]StepRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT step, outcome, COALESCE(provider,''), COALESCE(provider_ref,''),
		       COALESCE(detail,''), completed_at
		FROM onboarding.application_steps
		WHERE application_id = $1
		ORDER BY id`, applicationID)
	if err != nil {
		return nil, fmt.Errorf("leer pasos: %w", err)
	}
	defer rows.Close()

	var out []StepRecord
	for rows.Next() {
		var r StepRecord
		if err := rows.Scan(&r.Step, &r.Outcome, &r.Provider, &r.ProviderRef, &r.Detail, &r.CompletedAt); err != nil {
			return nil, fmt.Errorf("leer paso: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// completedSteps indexa los pasos ya ejecutados, para saltarlos al reanudar.
func (s *Store) completedSteps(ctx context.Context, applicationID string) (map[Step]StepRecord, error) {
	records, err := s.Steps(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	done := make(map[Step]StepRecord, len(records))
	for _, r := range records {
		done[r.Step] = r
	}
	return done, nil
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// applicantFrom reconstruye los datos del solicitante desde el expediente.
func applicantFrom(app *Application) kyc.Applicant { return app.Applicant }
