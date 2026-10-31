//! Core bancario de AIBank: ledger de doble partida, cuentas y motor de posting.
//!
//! Este crate NO conoce proveedores externos (BaaS, emisores, rieles): todo lo
//! externo entra por adaptadores que llaman al motor de posting.

pub mod accounts;
pub mod authorizations;
pub mod credit;
pub mod credit_cycle;
pub mod db;
pub mod grpc;
pub mod kafka;
pub mod mandates;
pub mod model;
pub mod money;
pub mod outbox;
pub mod posting;
pub mod products;
pub mod reconciliation;
pub mod telemetry;

pub use accounts::AccountRepository;
pub use credit::{CreditLine, CreditLineStatus, CreditRepository, NewCreditLine};
pub use credit_cycle::Statement;
pub use model::*;
pub use posting::PostingService;
pub use products::ProductRepository;
pub use reconciliation::{Finding, FindingKind, Reconciler};
