package bff

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/aibank/aibank/services/aliases"
	"github.com/aibank/aibank/services/control"
	"github.com/aibank/aibank/services/passkeys"
)

// El centro de permisos y privacidad (F3-06): todo lo que tiene acceso a la
// cuenta, en un solo sitio, y cómo retirarlo.
//
// Hasta ahora estaba repartido: los permisos de pago en una pantalla, desvincular
// el teléfono en un menú, el número del directorio en otra, y las passkeys en
// ninguna. Lo que no se ve no se puede revocar.
//
// Cada sección aparece solo si el banco la ofrece. Revocar usa los mismos
// caminos de siempre (permisos de pago, teléfonos, directorio) más uno nuevo, el
// de las passkeys; y ni el teléfono ni la passkey se quitan si son la última
// forma de entrar.

type accessPhoneJSON struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	HardwareBacked bool       `json:"hardware_backed"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	Locked         bool       `json:"locked,omitempty"`
	// Current: es el teléfono con el que se está usando la app ahora.
	Current bool `json:"current,omitempty"`
}

type accessPasskeyJSON struct {
	ID         string     `json:"id"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	Synced     bool       `json:"synced"`
	Current    bool       `json:"current,omitempty"`
}

type accessActivityJSON struct {
	At        time.Time `json:"at"`
	ActorKind string    `json:"actor_kind"`
	// ActorName: «iPhone de Ana», «Agente de viajes de Mercatus»…
	ActorName string `json:"actor_name"`
	Action    string `json:"action"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason,omitempty"`
}

type accessJSON struct {
	Platforms      []mandateJSON        `json:"platforms"`
	Phones         []accessPhoneJSON    `json:"phones"`
	Passkeys       []accessPasskeyJSON  `json:"passkeys"`
	PhoneDirectory *string              `json:"phone_directory,omitempty"`
	Activity       []accessActivityJSON `json:"activity"`
}

// activityLimit: lo reciente, no el archivo entero.
const activityLimit = 30

func (s *Server) accessRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/access", s.requireAuth(s.handleAccess))
	if s.passkeys != nil {
		mux.Handle("DELETE /v1/passkeys/{id}", s.requireAuth(s.handleRemovePasskey))
	}
}

func (s *Server) handleAccess(w http.ResponseWriter, r *http.Request, p *Principal) {
	ctx := r.Context()
	out := accessJSON{Platforms: []mandateJSON{}, Phones: []accessPhoneJSON{}, Passkeys: []accessPasskeyJSON{},
		Activity: []accessActivityJSON{}}

	if s.consents != nil {
		mandates, err := s.core.ListMandates(ctx, p.CustomerID)
		if err != nil {
			s.accessUnavailable(w, r, "permisos de pago", err)
			return
		}
		for _, m := range mandates {
			// Solo lo que hoy da acceso: lo vencido o retirado ya no.
			if m.Usable() {
				out.Platforms = append(out.Platforms, s.toMandateJSON(ctx, m))
			}
		}
	}

	phoneNames := map[string]string{}
	if s.devices != nil {
		phones, err := s.devices.svc.List(ctx, p.CustomerID)
		if err != nil {
			s.accessUnavailable(w, r, "teléfonos", err)
			return
		}
		for _, d := range phones {
			phoneNames[d.ID] = d.Name
			out.Phones = append(out.Phones, accessPhoneJSON{ID: d.ID, Name: d.Name, HardwareBacked: d.HardwareBacked,
				CreatedAt: d.CreatedAt, LastUsedAt: d.LastUsedAt, Locked: d.Locked, Current: d.ID == p.DeviceID})
		}
	}

	if s.passkeys != nil {
		keys, err := s.passkeys.svc.List(ctx, p.CustomerID)
		if err != nil {
			s.accessUnavailable(w, r, "passkeys", err)
			return
		}
		for _, k := range keys {
			out.Passkeys = append(out.Passkeys, accessPasskeyJSON{ID: k.ID, CreatedAt: k.CreatedAt,
				LastUsedAt: k.LastUsedAt, Synced: k.Synced, Current: k.ID == p.DeviceID})
		}
	}

	if s.aliases != nil {
		alias, err := s.aliases.svc.Mine(ctx, p.CustomerID)
		switch {
		case errors.Is(err, aliases.ErrNotFound):
		case err != nil:
			s.accessUnavailable(w, r, "directorio", err)
			return
		default:
			out.PhoneDirectory = &alias.Phone
		}
	}

	if s.control != nil {
		history, err := s.control.control.History(ctx, p.CustomerID, activityLimit)
		if err != nil {
			s.accessUnavailable(w, r, "actividad", err)
			return
		}
		out.Activity = s.activityJSON(ctx, history, phoneNames)
	}
	writeJSON(w, http.StatusOK, out)
}

// activityJSON traduce el registro de decisiones a quién hizo qué, con nombres
// que la persona reconoce.
func (s *Server) activityJSON(ctx context.Context, history []control.Entry, phones map[string]string) []accessActivityJSON {
	var agentIDs []string
	for _, e := range history {
		if e.Actor.Kind == control.ActorAgent {
			agentIDs = append(agentIDs, e.Actor.ID)
		}
	}
	agentNames := map[string]string{}
	if s.agents != nil && len(agentIDs) > 0 {
		if names, err := s.agents.Names(ctx, agentIDs); err == nil {
			for id, n := range names {
				agentNames[id] = n.Name + " de " + s.merchantName(ctx, n.ClientID)
			}
		}
	}

	out := make([]accessActivityJSON, 0, len(history))
	for _, e := range history {
		name := e.Actor.ID
		switch e.Actor.Kind {
		case control.ActorHolder:
			name = "Tú"
			if phone, ok := phones[e.Actor.ID]; ok {
				name = "Tú, desde " + phone
			}
		case control.ActorAgent:
			if agent, ok := agentNames[e.Actor.ID]; ok {
				name = agent
			} else {
				// Antes de la 1.1 el actor era la plataforma, no el agente.
				name = s.merchantName(ctx, e.Actor.ID)
			}
		}
		out = append(out, accessActivityJSON{At: e.DecidedAt, ActorKind: string(e.Actor.Kind), ActorName: name,
			Action: string(e.Action), Outcome: e.Outcome, Reason: string(e.Reason)})
	}
	return out
}

func (s *Server) accessUnavailable(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.logger.ErrorContext(r.Context(), "centro de permisos", "parte", what, "error", err)
	writeError(w, http.StatusServiceUnavailable, "unavailable", "no pudimos consultar qué tiene acceso a tu cuenta")
}

// lastWayIn dice si quitar un teléfono o una passkey dejaría a la persona sin
// forma de entrar. Entonces no se quita: recuperar el acceso sería un trámite
// con soporte.
func (s *Server) lastWayIn(ctx context.Context, customerID string) (bool, error) {
	ways := 0
	if s.devices != nil {
		phones, err := s.devices.svc.List(ctx, customerID)
		if err != nil {
			return false, err
		}
		ways += len(phones)
	}
	if s.passkeys != nil {
		keys, err := s.passkeys.svc.List(ctx, customerID)
		if err != nil {
			return false, err
		}
		ways += len(keys)
	}
	return ways <= 1, nil
}

func (s *Server) handleRemovePasskey(w http.ResponseWriter, r *http.Request, p *Principal) {
	last, err := s.lastWayIn(r.Context(), p.CustomerID)
	if err != nil {
		s.accessUnavailable(w, r, "formas de entrar", err)
		return
	}
	if last {
		writeError(w, http.StatusConflict, "last_way_in",
			"es tu única forma de entrar: vincula un teléfono o crea otra passkey antes de quitarla")
		return
	}
	err = s.passkeys.svc.Remove(r.Context(), p.CustomerID, r.PathValue("id"))
	if errors.Is(err, passkeys.ErrUnknownCredential) {
		writeError(w, http.StatusNotFound, "not_found", "esa passkey no existe")
		return
	}
	if err != nil {
		s.accessUnavailable(w, r, "quitar passkey", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
