//! Barrendero de retenciones vencidas.
//!
//! Sin este proceso, una autorización que nadie captura deja el dinero del
//! pagador retenido para siempre: el saldo disponible baja y no vuelve nunca. El
//! vencimiento que devuelve la API sería una promesa que nada cumple.
//!
//! Corre aparte del servidor gRPC a propósito. Es trabajo periódico que mueve
//! dinero, y mezclarlo con el proceso que atiende peticiones significa que un
//! pico de tráfico retrasa la devolución de retenciones —o que una pasada larga
//! del barrendero come recursos del camino de pago.
//!
//! Variables de entorno:
//!   DATABASE_URL      conexión a PostgreSQL
//!   SWEEP_INTERVAL_S  segundos entre pasadas (por defecto 30)
//!   SWEEP_BATCH       máximo de retenciones por pasada (por defecto 200)

use aibank_core::authorizations::AuthorizationService;
use aibank_core::db;
use std::error::Error;
use std::time::Duration;

#[tokio::main]
async fn main() -> Result<(), Box<dyn Error>> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()),
        )
        .init();

    let database_url = std::env::var("DATABASE_URL")
        .unwrap_or_else(|_| "postgres://aibank:aibank_dev@localhost:5434/aibank".to_string());
    let interval = env_number("SWEEP_INTERVAL_S", 30);
    let batch = env_number("SWEEP_BATCH", 200);

    let pool = db::connect(&database_url, 4).await?;
    let authorizations = AuthorizationService::new(pool);

    tracing::info!(interval_s = interval, batch, "barrendero de retenciones en marcha");

    let mut ticker = tokio::time::interval(Duration::from_secs(interval));
    loop {
        tokio::select! {
            _ = ticker.tick() => {
                match authorizations.release_expired(batch as i64).await {
                    Ok(ids) if !ids.is_empty() => {
                        tracing::info!(count = ids.len(), "retenciones vencidas liberadas");
                    }
                    Ok(_) => {}
                    // Un fallo de una pasada NO detiene el barrendero: la siguiente
                    // vuelve a intentarlo. Pararse aquí dejaría el dinero retenido
                    // hasta que alguien lo notara.
                    Err(err) => tracing::error!(error = %err, "fallo al liberar vencidas"),
                }
            }
            _ = tokio::signal::ctrl_c() => {
                tracing::info!("apagando");
                return Ok(());
            }
        }
    }
}

fn env_number(name: &str, default: u64) -> u64 {
    std::env::var(name)
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(default)
}
