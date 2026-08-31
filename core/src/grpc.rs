//! Servidor gRPC del core: traduce el contrato de [`contracts/proto`] a los
//! servicios de dominio.
//!
//! Esta capa NO tiene lógica de negocio; su única responsabilidad es convertir
//! entre el contrato y el dominio, y traducir los errores de negocio a códigos
//! gRPC con el motivo tipado en la metadata `x-aibank-reason`.

use crate::model::*;
use crate::{AccountRepository, PostingService, ProductRepository};
use tonic::{Request, Response, Status};
use uuid::Uuid;

pub mod pb {
    tonic::include_proto!("aibank.core.v1");
}

use pb::{
    account_service_server::AccountService, ledger_service_server::LedgerService,
    product_service_server::ProductService,
};

/// Clave de metadata donde viaja el motivo del rechazo.
pub const REASON_METADATA_KEY: &str = "x-aibank-reason";

// ---------------------------------------------------------------- errores

/// Traduce un error de dominio a un `Status` gRPC.
///
/// El código distingue lo reintentable de lo definitivo, y la metadata lleva el
/// motivo exacto para que el llamador no tenga que interpretar el mensaje.
fn to_status(err: PostingError) -> Status {
    use pb::PostingErrorReason as R;

    let (code, reason) = match &err {
        PostingError::Invalid(_) => (tonic::Code::InvalidArgument, R::Invalid),
        PostingError::IdempotencyConflict(_) => (tonic::Code::Aborted, R::IdempotencyConflict),
        PostingError::InsufficientFunds(_) => (tonic::Code::FailedPrecondition, R::InsufficientFunds),
        PostingError::BalanceCapExceeded(_) => (tonic::Code::FailedPrecondition, R::BalanceCapExceeded),
        PostingError::TransactionCapExceeded(_) => {
            (tonic::Code::FailedPrecondition, R::TransactionCapExceeded)
        }
        // Fallo de infraestructura: el llamador SÍ puede reintentar.
        PostingError::Database(_) => (tonic::Code::Unavailable, R::Unspecified),
    };

    let mut status = Status::new(code, err.to_string());
    if let Ok(value) = reason.as_str_name().parse() {
        status.metadata_mut().insert(REASON_METADATA_KEY, value);
    }
    status
}

fn parse_uuid(value: &str, field: &str) -> Result<Uuid, Status> {
    Uuid::parse_str(value).map_err(|_| Status::invalid_argument(format!("{field} is not a valid UUID")))
}

fn to_timestamp(value: chrono::DateTime<chrono::Utc>) -> prost_types::Timestamp {
    prost_types::Timestamp { seconds: value.timestamp(), nanos: value.timestamp_subsec_nanos() as i32 }
}

// ---------------------------------------------------------------- conversiones

impl From<AccountType> for pb::AccountType {
    fn from(value: AccountType) -> Self {
        match value {
            AccountType::Asset => pb::AccountType::Asset,
            AccountType::Liability => pb::AccountType::Liability,
            AccountType::Equity => pb::AccountType::Equity,
            AccountType::Income => pb::AccountType::Income,
            AccountType::Expense => pb::AccountType::Expense,
        }
    }
}

impl TryFrom<pb::AccountType> for AccountType {
    type Error = Status;

    fn try_from(value: pb::AccountType) -> Result<Self, Self::Error> {
        match value {
            pb::AccountType::Asset => Ok(AccountType::Asset),
            pb::AccountType::Liability => Ok(AccountType::Liability),
            pb::AccountType::Equity => Ok(AccountType::Equity),
            pb::AccountType::Income => Ok(AccountType::Income),
            pb::AccountType::Expense => Ok(AccountType::Expense),
            pb::AccountType::Unspecified => Err(Status::invalid_argument("account type is required")),
        }
    }
}

fn account_to_pb(account: Account) -> pb::Account {
    pb::Account {
        id: account.id.to_string(),
        code: account.code,
        name: account.name,
        r#type: pb::AccountType::from(account.account_type) as i32,
        owner: match account.owner {
            AccountOwner::Customer => pb::AccountOwner::Customer,
            AccountOwner::Internal => pb::AccountOwner::Internal,
        } as i32,
        owner_id: account.owner_id.map(|id| id.to_string()).unwrap_or_default(),
        product_id: account.product_id.map(|id| id.to_string()).unwrap_or_default(),
        currency: account.currency,
        status: match account.status {
            AccountStatus::Active => pb::AccountStatus::Active,
            AccountStatus::Frozen => pb::AccountStatus::Frozen,
            AccountStatus::Closed => pb::AccountStatus::Closed,
        } as i32,
        allows_overdraft: account.allows_overdraft,
        created_at: Some(to_timestamp(account.created_at)),
    }
}

fn product_to_pb(product: Product) -> pb::Product {
    pb::Product {
        id: product.id.to_string(),
        code: product.code,
        name: product.name,
        currency: product.currency,
        allows_overdraft: product.allows_overdraft,
        max_balance_minor: product.max_balance_minor,
        max_transaction_minor: product.max_transaction_minor,
        active: product.active,
    }
}

// ---------------------------------------------------------------- servicios

pub struct LedgerApi {
    posting: PostingService,
    accounts: AccountRepository,
}

impl LedgerApi {
    pub fn new(posting: PostingService, accounts: AccountRepository) -> Self {
        Self { posting, accounts }
    }
}

#[tonic::async_trait]
impl LedgerService for LedgerApi {
    async fn post(&self, request: Request<pb::PostRequest>) -> Result<Response<pb::PostResponse>, Status> {
        let req = request.into_inner();

        let mut entries = Vec::with_capacity(req.entries.len());
        for (i, entry) in req.entries.iter().enumerate() {
            let amount = entry
                .amount
                .as_ref()
                .ok_or_else(|| Status::invalid_argument(format!("entry {i}: amount is required")))?;
            let direction = match entry.direction() {
                pb::Direction::Debit => Direction::Debit,
                pb::Direction::Credit => Direction::Credit,
                pb::Direction::Unspecified => {
                    return Err(Status::invalid_argument(format!("entry {i}: direction is required")))
                }
            };
            entries.push(EntryCommand {
                account_id: parse_uuid(&entry.account_id, &format!("entry {i}: account_id"))?,
                direction,
                amount_minor: amount.amount_minor,
                currency: amount.currency.clone(),
            });
        }

        let posting_request = PostingRequest {
            idempotency_key: req.idempotency_key,
            kind: req.kind,
            entries,
            description: (!req.description.is_empty()).then_some(req.description),
        };

        let result = self.posting.post(&posting_request).await.map_err(to_status)?;

        Ok(Response::new(pb::PostResponse {
            transaction_id: result.transaction.id.to_string(),
            posted_at: Some(to_timestamp(result.transaction.posted_at)),
            replayed: result.replayed,
        }))
    }

    async fn get_balance(
        &self,
        request: Request<pb::GetBalanceRequest>,
    ) -> Result<Response<pb::GetBalanceResponse>, Status> {
        let account_id = parse_uuid(&request.into_inner().account_id, "account_id")?;

        let account = self
            .accounts
            .find_by_id(account_id)
            .await
            .map_err(|e| to_status(PostingError::Database(e)))?
            .ok_or_else(|| Status::not_found("account not found"))?;

        let balance = self
            .accounts
            .balance(account_id)
            .await
            .map_err(|e| to_status(PostingError::Database(e)))?;
        let projected = self
            .accounts
            .projected_balance(account_id)
            .await
            .map_err(|e| to_status(PostingError::Database(e)))?;

        Ok(Response::new(pb::GetBalanceResponse {
            balance: Some(pb::Money {
                amount_minor: balance.balance_minor,
                currency: account.currency.clone(),
            }),
            entry_count: balance.entry_count,
            projected_balance: Some(pb::Money {
                amount_minor: projected.balance_minor,
                currency: account.currency,
            }),
        }))
    }
}

pub struct AccountApi {
    accounts: AccountRepository,
    products: ProductRepository,
}

impl AccountApi {
    pub fn new(accounts: AccountRepository, products: ProductRepository) -> Self {
        Self { accounts, products }
    }
}

#[tonic::async_trait]
impl AccountService for AccountApi {
    async fn open_customer_account(
        &self,
        request: Request<pb::OpenCustomerAccountRequest>,
    ) -> Result<Response<pb::Account>, Status> {
        let req = request.into_inner();
        let customer_id = parse_uuid(&req.customer_id, "customer_id")?;

        let product = self
            .products
            .find_by_code(&req.product_code)
            .await
            .map_err(|e| to_status(PostingError::Database(e)))?
            .ok_or_else(|| Status::not_found(format!("product '{}' not found", req.product_code)))?;

        let account = self
            .accounts
            .open_customer_account(&req.code, &req.name, customer_id, &product)
            .await
            .map_err(to_status)?;

        Ok(Response::new(account_to_pb(account)))
    }

    async fn create_internal_account(
        &self,
        request: Request<pb::CreateInternalAccountRequest>,
    ) -> Result<Response<pb::Account>, Status> {
        let req = request.into_inner();
        let account_type = AccountType::try_from(req.r#type())?;

        let account = self
            .accounts
            .create_internal(&req.code, &req.name, account_type, &req.currency)
            .await
            .map_err(to_status)?;

        Ok(Response::new(account_to_pb(account)))
    }

    async fn get_account(
        &self,
        request: Request<pb::GetAccountRequest>,
    ) -> Result<Response<pb::Account>, Status> {
        let code = request.into_inner().code;
        let account = self
            .accounts
            .find_by_code(&code)
            .await
            .map_err(|e| to_status(PostingError::Database(e)))?
            .ok_or_else(|| Status::not_found(format!("account '{code}' not found")))?;

        Ok(Response::new(account_to_pb(account)))
    }
}

pub struct ProductApi {
    products: ProductRepository,
}

impl ProductApi {
    pub fn new(products: ProductRepository) -> Self {
        Self { products }
    }
}

#[tonic::async_trait]
impl ProductService for ProductApi {
    async fn create_product(
        &self,
        request: Request<pb::CreateProductRequest>,
    ) -> Result<Response<pb::Product>, Status> {
        let req = request.into_inner();

        let product = self
            .products
            .create(&NewProduct {
                code: req.code,
                name: req.name,
                kind: ProductKind::DepositAccount,
                currency: req.currency,
                allows_overdraft: req.allows_overdraft,
                max_balance_minor: req.max_balance_minor,
                max_transaction_minor: req.max_transaction_minor,
            })
            .await
            .map_err(to_status)?;

        Ok(Response::new(product_to_pb(product)))
    }

    async fn get_product(
        &self,
        request: Request<pb::GetProductRequest>,
    ) -> Result<Response<pb::Product>, Status> {
        let code = request.into_inner().code;
        let product = self
            .products
            .find_by_code(&code)
            .await
            .map_err(|e| to_status(PostingError::Database(e)))?
            .ok_or_else(|| Status::not_found(format!("product '{code}' not found")))?;

        Ok(Response::new(product_to_pb(product)))
    }
}
