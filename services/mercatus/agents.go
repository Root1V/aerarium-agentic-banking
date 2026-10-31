package mercatus

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/agents"
	"github.com/aibank/aibank/services/oauth"
)

// Los agentes de una plataforma (AE-10, AE-11). Cada uno tiene un nombre que el
// titular reconoce al darle permiso, y una o más claves Ed25519 con las que
// firma sus pagos. Una plataforma solo ve y gestiona a sus agentes.

type agentKeyJSON struct {
	KeyID     string     `json:"key_id"`
	PublicKey agents.JWK `json:"public_key"`
	CreatedAt time.Time  `json:"created_at"`
	// ExpiresAt: la clave vale hasta entonces, porque se añadió otra.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RetiredAt *time.Time `json:"retired_at,omitempty"`
}

type agentJSON struct {
	AgentID   string         `json:"agent_id"`
	Name      string         `json:"name"`
	Status    string         `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	RevokedAt *time.Time     `json:"revoked_at,omitempty"`
	Keys      []agentKeyJSON `json:"keys"`
}

func toAgentKeyJSON(k agents.Key) agentKeyJSON {
	return agentKeyJSON{KeyID: encodeID(agentKeyPrefix, k.ID), PublicKey: agents.ToJWK(k.PublicKey),
		CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, RetiredAt: k.RetiredAt}
}

func toAgentJSON(a *agents.Agent) agentJSON {
	out := agentJSON{AgentID: encodeID(agentPrefix, a.ID), Name: a.Name, Status: "active",
		CreatedAt: a.CreatedAt, RevokedAt: a.RevokedAt, Keys: []agentKeyJSON{}}
	if a.RevokedAt != nil {
		out.Status = "revoked"
	}
	for _, k := range a.Keys {
		out.Keys = append(out.Keys, toAgentKeyJSON(k))
	}
	return out
}

type registerAgentRequest struct {
	Name      string     `json:"name"`
	PublicKey agents.JWK `json:"public_key"`
}

func (s *Server) handleRegisterAgent(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	var req registerAgentRequest
	if !decodeBody(w, r, &req) {
		return
	}
	agent, err := s.agents.Register(r.Context(), claims.Subject, req.Name, req.PublicKey)
	if err != nil {
		s.agentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAgentJSON(agent))
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	id, ok := agentIDFromPath(w, r)
	if !ok {
		return
	}
	agent, err := s.agents.Get(r.Context(), claims.Subject, id)
	if err != nil {
		s.agentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgentJSON(agent))
}

// handleRevokeAgent da de baja al agente: sus claves y sus permisos dejan de
// servir al momento. Repetirlo no es un error.
func (s *Server) handleRevokeAgent(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	id, ok := agentIDFromPath(w, r)
	if !ok {
		return
	}
	if err := s.agents.Revoke(r.Context(), claims.Subject, id); err != nil {
		s.agentError(w, r, err)
		return
	}
	s.handleGetAgent(w, r, claims)
}

type addAgentKeyRequest struct {
	PublicKey agents.JWK `json:"public_key"`
}

// handleAddAgentKey añade una clave. Las que ya valían siguen valiendo siete
// días: rotar no corta nada ni toca los permisos del agente.
func (s *Server) handleAddAgentKey(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	id, ok := agentIDFromPath(w, r)
	if !ok {
		return
	}
	var req addAgentKeyRequest
	if !decodeBody(w, r, &req) {
		return
	}
	key, err := s.agents.AddKey(r.Context(), claims.Subject, id, req.PublicKey)
	if err != nil {
		s.agentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAgentKeyJSON(*key))
}

func (s *Server) handleRetireAgentKey(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	id, ok := agentIDFromPath(w, r)
	if !ok {
		return
	}
	keyID, err := decodeID(agentKeyPrefix, r.PathValue("keyID"))
	if err != nil {
		writeError(w, http.StatusNotFound, CodeAgentNotFound, "la clave no existe")
		return
	}
	if err := s.agents.RetireKey(r.Context(), claims.Subject, id, keyID); err != nil {
		s.agentError(w, r, err)
		return
	}
	s.handleGetAgent(w, r, claims)
}

func agentIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := decodeID(agentPrefix, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, CodeAgentNotFound, "el agente no existe")
		return "", false
	}
	return id, true
}

func (s *Server) agentError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, agents.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeAgentNotFound, "el agente o la clave no existe")
	case errors.Is(err, agents.ErrRevoked):
		writeError(w, http.StatusConflict, CodeAgentRevoked, "el agente está dado de baja")
	case errors.Is(err, agents.ErrKeyInUse):
		writeError(w, http.StatusConflict, CodeAgentKeyInUse, "esa clave ya está registrada")
	case errors.Is(err, agents.ErrLastKey):
		writeError(w, http.StatusConflict, CodeAgentLastKey,
			"es la única clave vigente; añade otra antes de retirarla")
	case errors.Is(err, agents.ErrInvalidKey), errors.Is(err, agents.ErrBadName):
		writeError(w, http.StatusBadRequest, CodeMalformedRequest, err.Error())
	default:
		s.log.ErrorContext(r.Context(), "agentes", "error", err)
		writeError(w, http.StatusInternalServerError, CodeServerError, "error interno")
	}
}

// ---------------------------------------------------------------- en cada pago

// verifyAgentSignature comprueba que el pago venga firmado por un agente de esta
// integración y devuelve quién firmó. Sin registro de agentes no se exige nada y
// devuelve nil. Devuelve `false` cuando ya respondió.
func (s *Server) verifyAgentSignature(w http.ResponseWriter, r *http.Request, claims *oauth.Claims, body []byte) (*agents.Signer, bool) {
	if s.agents == nil {
		return nil, true
	}
	lookup := func(ctx context.Context, keyID string) (*agents.Signer, error) {
		id, err := decodeID(agentKeyPrefix, keyID)
		if err != nil {
			return nil, agents.ErrNotFound
		}
		return s.agents.KeyForSignature(ctx, id)
	}
	signer, err := agents.Verify(r.Context(), r, body, lookup, time.Now())
	if err == nil && signer.ClientID != claims.Subject {
		// La clave es de un agente de otra plataforma.
		err = agents.ErrSignatureInvalid
	}
	if err != nil {
		// Para afuera, todos los motivos son el mismo; el concreto, al log.
		s.log.WarnContext(r.Context(), "firma de agente rechazada", "cliente", claims.Subject, "motivo", err)
		writeError(w, http.StatusUnauthorized, CodeAgentSignatureInvalid,
			"el pago tiene que venir firmado por un agente registrado de esta integración")
		return nil, false
	}
	return signer, true
}

// checkAgentScope comprueba que el agente que firmó pueda pagar desde esa
// cuenta: en el modelo B, que el mandato sea suyo; en el A, que la sub-cuenta lo
// sea.
func (s *Server) checkAgentScope(w http.ResponseWriter, r *http.Request, signer *agents.Signer, mandate *coreclient.Mandate, payer string) bool {
	if mandate != nil {
		consent, err := s.store.ConsentForMandate(r.Context(), mandate.ID)
		switch {
		case errors.Is(err, oauth.ErrConsentNotFound):
			// Un mandato que no nació de una solicitud no tiene agente.
		case err != nil:
			s.log.ErrorContext(r.Context(), "agente del mandato", "error", err)
			writeError(w, http.StatusServiceUnavailable, CodeServiceUnavailable,
				"el servicio no está disponible; reintenta con la misma Idempotency-Key")
			return false
		case consent.AgentID != "" && consent.AgentID != signer.AgentID:
			writeError(w, http.StatusForbidden, CodeMandateAgentScope,
				"el permiso es de otro agente")
			return false
		}
		// Un mandato de antes de AE-10 no tiene agente: sirve a cualquiera de la
		// plataforma hasta que venza. No se conceden nuevos así.
		return true
	}
	owner, err := s.agents.AccountAgent(r.Context(), payer)
	if err != nil {
		s.log.ErrorContext(r.Context(), "agente de la cuenta", "error", err)
		writeError(w, http.StatusServiceUnavailable, CodeServiceUnavailable,
			"el servicio no está disponible; reintenta con la misma Idempotency-Key")
		return false
	}
	if owner != signer.AgentID {
		writeError(w, http.StatusForbidden, CodeAccountAgentScope,
			"la cuenta pagadora no es de este agente; átala con agent_id al abrirla")
		return false
	}
	return true
}
