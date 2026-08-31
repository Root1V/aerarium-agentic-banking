// Package sim provee un autenticador de desarrollo para el BFF.
//
// NO es un sistema de autenticación: no valida credenciales, no expira sesiones y
// no vincula dispositivos. Existe solo para poder ejercitar el BFF en desarrollo y
// pruebas mientras se integra la autenticación real (passkeys FIDO2 vinculadas al
// dispositivo más verificación biométrica).
//
// Por eso vive en un paquete aparte y explícitamente marcado: que sea imposible
// cablearlo a producción por descuido.
package sim

import (
	"context"
	"sync"

	"github.com/aibank/aibank/services/bff"
	"github.com/google/uuid"
)

// Authenticator resuelve tokens emitidos en memoria.
type Authenticator struct {
	mu     sync.RWMutex
	tokens map[string]bff.Principal
}

func NewAuthenticator() *Authenticator {
	return &Authenticator{tokens: make(map[string]bff.Principal)}
}

// Issue emite un token para un cliente. Solo para desarrollo y pruebas.
func (a *Authenticator) Issue(customerID, deviceID string) string {
	token := "dev-" + uuid.NewString()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tokens[token] = bff.Principal{CustomerID: customerID, DeviceID: deviceID}
	return token
}

func (a *Authenticator) Authenticate(_ context.Context, token string) (*bff.Principal, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	principal, ok := a.tokens[token]
	if !ok {
		return nil, bff.ErrUnauthenticated
	}
	return &principal, nil
}
