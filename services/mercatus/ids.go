package mercatus

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// Prefijos de los identificadores públicos.
//
// El contrato pide identificadores opacos y estables con un prefijo reconocible
// de un vistazo en un log. Internamente son UUID del core; aquí se visten y se
// desvisten. Es una traducción, no un almacenamiento: no hay una segunda tabla de
// identificadores que pueda desincronizarse.
const (
	accountPrefix       = "acc_"
	authorizationPrefix = "auth_"
)

var errBadID = errors.New("identificador con formato inválido")

// encodeID viste un UUID del core como identificador público.
func encodeID(prefix, id string) string {
	if id == "" {
		return ""
	}
	return prefix + strings.ReplaceAll(id, "-", "")
}

// decodeID recupera el UUID del core.
//
// Acepta también el UUID sin vestir: durante la integración es cómodo pegar un
// identificador que se vio en un log del banco, y aceptarlo no relaja ninguna
// barrera —la propiedad de la cuenta se verifica aparte, contra la integración
// que hace la llamada.
func decodeID(prefix, public string) (string, error) {
	raw := strings.TrimPrefix(public, prefix)
	if raw == public && strings.Contains(public, "_") {
		// Traía un prefijo, pero no el que corresponde: pasar un acc_ donde va un
		// auth_ es un error del cliente que conviene señalar, no interpretar.
		return "", errBadID
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return "", errBadID
	}
	return parsed.String(), nil
}
