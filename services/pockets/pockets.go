// Package pockets maneja los bolsillos: dinero apartado dentro de una cuenta
// (doc 15, F3-02).
//
// El bolsillo es una cuenta hija del core, que hace cumplir lo que importa: el
// tope de saldo es de la cuenta con sus bolsillos, y lo que se mueve entre ellos
// no sale de la familia. Aquí se añade para qué es cada uno, y que solo la persona
// dueña los abre, los llena, los vacía y los cierra.
package pockets

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

//go:embed schema.sql
var schema string

// Para qué es un bolsillo.
const (
	PurposeCard    = "card"
	PurposeGeneral = "general"
)

// MaxPockets por cuenta: suficientes para ordenar el dinero, no tantos que la
// cuenta se vuelva una lista.
const MaxPockets = 10

var (
	ErrNotFound     = errors.New("ese bolsillo no existe")
	ErrBadName      = errors.New("el nombre es obligatorio, de hasta 40 caracteres")
	ErrBadPurpose   = errors.New("propósito desconocido")
	ErrCardPocket   = errors.New("esta cuenta ya tiene un bolsillo para la tarjeta")
	ErrTooMany      = fmt.Errorf("una cuenta tiene hasta %d bolsillos", MaxPockets)
	ErrNotAnAccount = errors.New("los bolsillos se abren en una cuenta, no en otro bolsillo")
)

// Pocket es un bolsillo con su saldo.
type Pocket struct {
	ID            string
	ParentID      string
	Name          string
	Purpose       string
	BalanceMicros int64
	Currency      string
}

type Service struct {
	db   *sql.DB
	core *coreclient.Client
}

func NewService(db *sql.DB, core *coreclient.Client) *Service {
	return &Service{db: db, core: core}
}

func (s *Service) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// List devuelve los bolsillos abiertos de una cuenta del cliente.
func (s *Service) List(ctx context.Context, customerID, accountID string) ([]Pocket, error) {
	accounts, err := s.core.ListCustomerAccounts(ctx, customerID)
	if err != nil {
		return nil, err
	}
	purposes, err := s.purposes(ctx, accountID)
	if err != nil {
		return nil, err
	}
	var out []Pocket
	for _, a := range accounts {
		if a.GetParentAccountId() != accountID || a.GetStatus() == corev1.AccountStatus_ACCOUNT_STATUS_CLOSED {
			continue
		}
		balance, err := s.core.GetBalance(ctx, a.Id)
		if err != nil {
			return nil, err
		}
		purpose := purposes[a.Id]
		if purpose == "" {
			purpose = PurposeGeneral
		}
		out = append(out, Pocket{ID: a.Id, ParentID: accountID, Name: a.Name, Purpose: purpose,
			BalanceMicros: balance.AmountMicros, Currency: balance.Currency})
	}
	return out, nil
}

// CardPocket es el bolsillo para la tarjeta de la cuenta, o nil.
func (s *Service) CardPocket(ctx context.Context, customerID, accountID string) (*Pocket, error) {
	list, err := s.List(ctx, customerID, accountID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Purpose == PurposeCard {
			return &list[i], nil
		}
	}
	return nil, nil
}

// CardReserveMicros es lo apartado para la tarjeta en la cuenta. Lo usa la
// previsión de saldo.
func (s *Service) CardReserveMicros(ctx context.Context, customerID, accountID string) (int64, error) {
	pocket, err := s.CardPocket(ctx, customerID, accountID)
	if err != nil || pocket == nil {
		return 0, err
	}
	return pocket.BalanceMicros, nil
}

// Open abre un bolsillo en una cuenta (no en otro bolsillo) del cliente.
func (s *Service) Open(ctx context.Context, customerID, accountID, name, purpose string) (*Pocket, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 40 {
		return nil, ErrBadName
	}
	if purpose != PurposeCard && purpose != PurposeGeneral {
		return nil, ErrBadPurpose
	}
	parent, err := s.core.GetAccountByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if parent.GetOwnerId() != customerID {
		return nil, ErrNotFound
	}
	if parent.GetParentAccountId() != "" {
		return nil, ErrNotAnAccount
	}
	existing, err := s.List(ctx, customerID, accountID)
	if err != nil {
		return nil, err
	}
	if len(existing) >= MaxPockets {
		return nil, ErrTooMany
	}
	for _, p := range existing {
		if purpose == PurposeCard && p.Purpose == PurposeCard {
			return nil, ErrCardPocket
		}
	}

	account, err := s.core.OpenPocket(ctx, accountID, "PK-"+uuid.NewString(), name)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO pockets.pockets (account_id, parent_account_id, customer_id, purpose)
		VALUES ($1, $2, $3, $4)`, account.Id, accountID, customerID, purpose)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		// Dos aperturas a la vez: el bolsillo quedó abierto sin propósito, y se
		// cierra para no dejar uno huérfano.
		_, _ = s.core.ClosePocket(ctx, account.Id)
		return nil, ErrCardPocket
	}
	if err != nil {
		return nil, err
	}
	return &Pocket{ID: account.Id, ParentID: accountID, Name: account.Name, Purpose: purpose,
		Currency: account.Currency}, nil
}

// Move pasa dinero entre la cuenta y su bolsillo: `in` lo aparta, `out` lo devuelve.
func (s *Service) Move(ctx context.Context, customerID, pocketID, idempotencyKey string, in bool, amountMicros int64) (*coreclient.PostResult, error) {
	pocket, err := s.owned(ctx, customerID, pocketID)
	if err != nil {
		return nil, err
	}
	// La descripción es el nombre del bolsillo: la app arma «Apartado en …» o
	// «Devuelto de …» según el sentido.
	from, to := pocket.GetParentAccountId(), pocket.Id
	if !in {
		from, to = pocket.Id, pocket.GetParentAccountId()
	}
	// La clave se compone con el bolsillo: la misma clave para otro bolsillo es
	// otra operación.
	return s.core.MovePocket(ctx, "pocket:"+pocketID+":"+idempotencyKey, from, to, amountMicros,
		pocket.Currency, pocket.Name)
}

// Close devuelve lo que quede a la cuenta y cierra el bolsillo. Cerrar nunca hace
// desaparecer dinero.
func (s *Service) Close(ctx context.Context, customerID, pocketID string) error {
	pocket, err := s.owned(ctx, customerID, pocketID)
	if err != nil {
		return err
	}
	balance, err := s.core.GetBalance(ctx, pocketID)
	if err != nil {
		return err
	}
	if balance.AmountMicros > 0 {
		if _, err := s.core.MovePocket(ctx, "pocket-close:"+pocketID, pocketID, pocket.GetParentAccountId(),
			balance.AmountMicros, balance.Currency, pocket.Name); err != nil {
			return err
		}
	}
	if _, err := s.core.ClosePocket(ctx, pocketID); err != nil {
		return err
	}
	// Sin la fila, la cuenta puede volver a tener un bolsillo para la tarjeta.
	_, err = s.db.ExecContext(ctx, `DELETE FROM pockets.pockets WHERE account_id = $1`, pocketID)
	return err
}

// owned es el bolsillo, si es del cliente y está abierto.
func (s *Service) owned(ctx context.Context, customerID, pocketID string) (*corev1.Account, error) {
	if _, err := uuid.Parse(pocketID); err != nil {
		return nil, ErrNotFound
	}
	account, err := s.core.GetAccountByID(ctx, pocketID)
	if errors.Is(err, coreclient.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if account.GetOwnerId() != customerID || account.GetParentAccountId() == "" ||
		account.GetStatus() == corev1.AccountStatus_ACCOUNT_STATUS_CLOSED {
		return nil, ErrNotFound
	}
	return account, nil
}

func (s *Service) purposes(ctx context.Context, accountID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT account_id, purpose FROM pockets.pockets WHERE parent_account_id = $1`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, purpose string
		if err := rows.Scan(&id, &purpose); err != nil {
			return nil, err
		}
		out[id] = purpose
	}
	return out, rows.Err()
}
