//! Servidor gRPC del core bancario.
//!
//! Variables de entorno:
//!   DATABASE_URL  conexión a PostgreSQL
//!   BIND_ADDR     dirección de escucha (por defecto 127.0.0.1:50051)

use aibank_core::authorizations::AuthorizationService;
use aibank_core::mandates::MandateService;
use aibank_core::grpc::pb::{
    account_service_server::AccountServiceServer,
    authorization_service_server::AuthorizationServiceServer,
    ledger_service_server::LedgerServiceServer,
    mandate_service_server::MandateServiceServer,
    product_service_server::ProductServiceServer,
    reconciliation_service_server::ReconciliationServiceServer,
};
use aibank_core::grpc::{
    AccountApi, AuthorizationApi, LedgerApi, MandateApi, ProductApi, ReconciliationApi,
};
use aibank_core::reconciliation::Reconciler;
use aibank_core::{db, AccountRepository, PostingService, ProductRepository};
use std::error::Error;
use tonic::transport::Server;

#[tokio::main]
async fn main() -> Result<(), Box<dyn Error>> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "info".into()),
        )
        .init();

    let database_url = std::env::var("DATABASE_URL")
        .unwrap_or_else(|_| "postgres://aibank:aibank_dev@localhost:5434/aibank".to_string());
    let bind_addr = std::env::var("BIND_ADDR").unwrap_or_else(|_| "127.0.0.1:50051".to_string());

    let pool = db::connect(&database_url, 32).await?;
    db::migrate(&pool).await?;
    tracing::info!("migraciones aplicadas");

    let ledger = LedgerApi::new(PostingService::new(pool.clone()), AccountRepository::new(pool.clone()));
    let accounts = AccountApi::new(AccountRepository::new(pool.clone()), ProductRepository::new(pool.clone()));
    let products = ProductApi::new(ProductRepository::new(pool.clone()));
    let reconciliation = ReconciliationApi::new(Reconciler::new(pool.clone()));
    let authorizations = AuthorizationApi::new(AuthorizationService::new(pool.clone()));
    let mandates = MandateApi::new(MandateService::new(pool.clone()));

    tracing::info!(%bind_addr, "core escuchando");

    Server::builder()
        .add_service(LedgerServiceServer::new(ledger))
        .add_service(AccountServiceServer::new(accounts))
        .add_service(ProductServiceServer::new(products))
        .add_service(ReconciliationServiceServer::new(reconciliation))
        .add_service(AuthorizationServiceServer::new(authorizations))
        .add_service(MandateServiceServer::new(mandates))
        .serve_with_shutdown(bind_addr.parse()?, async {
            let _ = tokio::signal::ctrl_c().await;
            tracing::info!("apagando");
        })
        .await?;

    Ok(())
}
