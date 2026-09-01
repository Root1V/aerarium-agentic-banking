//! Core bancario de AIBank: ledger de doble partida, cuentas y motor de posting.
//!
//! Este crate NO conoce proveedores externos (BaaS, emisores, rieles): todo lo
//! externo entra por adaptadores que llaman al motor de posting.

pub mod accounts;
pub mod authorizations;
pub mod db;
pub mod grpc;
pub mod kafka;
pub mod model;
pub mod money;
pub mod outbox;
pub mod posting;
pub mod reconciliation;
pub mod telemetry;
pub mod products;

pub use accounts::AccountRepository;
pub use model::*;
pub use posting::PostingService;
pub use reconciliation::{Finding, FindingKind, Reconciler};
pub use products::ProductRepository;
