package oauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Vigencia de una solicitud de consentimiento.
//
// Una hora: tiempo de sobra para que una persona abra su app y decida, y corto
// para que una solicitud olvidada no quede esperando aprobación días después,
// cuando quien la pidió ya no recuerda haberla pedido.
const ConsentRequestTTL = time.Hour

// Estados de una solicitud.
const (
	ConsentPending  = "PENDING"
	ConsentApproved = "APPROVED"
	ConsentRejected = "REJECTED"
	// ConsentExpired no se almacena: se deriva de expires_at, igual que en
	// autorizaciones y mandatos.
	ConsentExpired = "EXPIRED"
)

var (
	ErrConsentNotFound = errors.New("solicitud de consentimiento inexistente")
	ErrConsentResolved = errors.New("la solicitud ya fue resuelta")
	ErrConsentExpired  = errors.New("la solicitud venció")
	// ErrLimitAboveRequested: el titular no puede conceder más de lo que se pidió.
	ErrLimitAboveRequested = errors.New("el permiso concedido excede lo solicitado")
)

// ConsentRequest es lo que una plataforma le pide a un titular.
//
// Nótese lo que NO tiene: la cuenta. La plataforma dice cuánto necesita y para
// qué; el titular elige sobre qué cuenta lo concede. Así una plataforma no
// aprende identificadores de cuenta antes de tener permiso.
type ConsentRequest struct {
	ID          string
	ClientID    string
	HandoffCode string

	RequestedMaxPerOperation *int64
	RequestedMaxTotal        *int64
	Currency                 string
	Purpose                  string

	Status     string
	MandateID  string
	AccountID  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ResolvedAt *time.Time
}

// ObservedStatus deriva el estado teniendo en cuenta el reloj.
func (c *ConsentRequest) ObservedStatus() string {
	if c.Status == ConsentPending && time.Now().After(c.ExpiresAt) {
		return ConsentExpired
	}
	return c.Status
}

// NewConsentRequest crea una solicitud pendiente.
func (s *Store) NewConsentRequest(ctx context.Context, req ConsentRequest) (*ConsentRequest, error) {
	handoff, err := generateSecret()
	if err != nil {
		return nil, err
	}
	req.ID = uuid.NewString()
	req.HandoffCode = "csr_" + handoff
	req.Status = ConsentPending
	req.CreatedAt = time.Now().UTC()
	req.ExpiresAt = req.CreatedAt.Add(ConsentRequestTTL)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO oauth.consent_requests (
			id, client_id, handoff_code, requested_max_per_operation_micros,
			requested_max_total_micros, currency, purpose, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, req.ID, req.ClientID, req.HandoffCode, req.RequestedMaxPerOperation,
		req.RequestedMaxTotal, req.Currency, req.Purpose, req.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("crear solicitud de consentimiento: %w", err)
	}
	return &req, nil
}

// FindConsentRequest busca por id (lo usa la plataforma) o por handoff_code (lo
// usa la app del titular).
func (s *Store) FindConsentRequest(ctx context.Context, idOrHandoff string) (*ConsentRequest, error) {
	var (
		c         ConsentRequest
		mandateID sql.NullString
		accountID sql.NullString
		resolved  sql.NullTime
	)
	// Se busca por las dos columnas en una sola consulta: el identificador que
	// tiene cada parte es distinto y ninguna debería tener que saber cuál usa la
	// otra.
	err := s.db.QueryRowContext(ctx, `
		SELECT id, client_id, handoff_code, requested_max_per_operation_micros,
		       requested_max_total_micros, currency, purpose, status, mandate_id,
		       account_id, created_at, expires_at, resolved_at
		  FROM oauth.consent_requests
		 WHERE handoff_code = $1 OR id::text = $1
	`, idOrHandoff).Scan(
		&c.ID, &c.ClientID, &c.HandoffCode, &c.RequestedMaxPerOperation,
		&c.RequestedMaxTotal, &c.Currency, &c.Purpose, &c.Status, &mandateID,
		&accountID, &c.CreatedAt, &c.ExpiresAt, &resolved,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConsentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("buscar solicitud: %w", err)
	}
	if mandateID.Valid {
		c.MandateID = mandateID.String
	}
	if accountID.Valid {
		c.AccountID = accountID.String
	}
	if resolved.Valid {
		c.ResolvedAt = &resolved.Time
	}
	return &c, nil
}

// CheckGrantable valida que una solicitud se pueda resolver y que lo concedido
// no exceda lo pedido.
//
// El titular puede conceder MENOS de lo que se le pidió — bajar el tope o
// acortar la vigencia— pero nunca más. Permitir más convertiría la pantalla de
// consentimiento en un lugar donde se puede conceder algo que nadie pidió.
func CheckGrantable(req *ConsentRequest, maxPerOperation, maxTotal *int64) error {
	switch req.ObservedStatus() {
	case ConsentPending:
	case ConsentExpired:
		return ErrConsentExpired
	default:
		return ErrConsentResolved
	}

	if exceeds(maxPerOperation, req.RequestedMaxPerOperation) {
		return fmt.Errorf("%w: por operación", ErrLimitAboveRequested)
	}
	if exceeds(maxTotal, req.RequestedMaxTotal) {
		return fmt.Errorf("%w: total", ErrLimitAboveRequested)
	}
	return nil
}

// exceeds indica si `granted` supera a `requested`.
//
// Un `requested` nulo significa "sin tope pedido", así que nada lo excede. Un
// `granted` nulo sobre un `requested` con tope SÍ lo excede: conceder sin tope
// donde se pidió uno acotado es conceder más.
func exceeds(granted, requested *int64) bool {
	if requested == nil {
		return false
	}
	if granted == nil {
		return true
	}
	return *granted > *requested
}

// ApproveConsentRequest la marca aprobada y la liga al mandato creado.
func (s *Store) ApproveConsentRequest(ctx context.Context, id, mandateID, accountID string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE oauth.consent_requests
		   SET status = 'APPROVED', mandate_id = $2, account_id = $3, resolved_at = now()
		 WHERE id = $1 AND status = 'PENDING'
	`, id, mandateID, accountID)
	if err != nil {
		return fmt.Errorf("aprobar solicitud: %w", err)
	}
	// La condición `status = 'PENDING'` en el UPDATE es la que hace la operación
	// segura frente a dos aprobaciones simultáneas: la segunda no afecta filas.
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrConsentResolved
	}
	return nil
}

// RejectConsentRequest la marca rechazada.
func (s *Store) RejectConsentRequest(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE oauth.consent_requests
		   SET status = 'REJECTED', resolved_at = now()
		 WHERE id = $1 AND status = 'PENDING'
	`, id)
	if err != nil {
		return fmt.Errorf("rechazar solicitud: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrConsentResolved
	}
	return nil
}
