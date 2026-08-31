package bff

import (
	"context"
	"errors"
	"fmt"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
)

// ErrForbidden: la cuenta existe pero no es del cliente autenticado.
var ErrForbidden = errors.New("la cuenta no pertenece al cliente")

// authorizeAccount comprueba que la cuenta pedida sea del cliente autenticado.
//
// Es la defensa contra el fallo más común y más caro de una API bancaria: aceptar
// el identificador de cuenta que manda el cliente y responder sin comprobar de
// quién es. Cambiar un id en la URL no puede mostrar el dinero de otra persona.
//
// Se responde igual —no encontrado— tanto si la cuenta no existe como si es de
// otro cliente: distinguirlas permitiría enumerar cuentas ajenas.
func (s *Server) authorizeAccount(ctx context.Context, p *Principal, accountID string) (*corev1.Account, error) {
	account, err := s.core.GetAccountByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, coreclient.ErrNotFound) {
			return nil, ErrForbidden
		}
		return nil, fmt.Errorf("consultar cuenta: %w", err)
	}

	if account.Owner != corev1.AccountOwner_ACCOUNT_OWNER_CUSTOMER {
		// Las cuentas internas del banco jamás se exponen por esta API.
		return nil, ErrForbidden
	}
	if account.OwnerId != p.CustomerID {
		return nil, ErrForbidden
	}
	return account, nil
}
