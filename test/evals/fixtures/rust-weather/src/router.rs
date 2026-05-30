//! Router assembly.
//!
//! `build_router` wires the three endpoints behind the API-key
//! middleware. `/health` is exempt so a load balancer can probe
//! liveness without a key.

use std::sync::{Arc, RwLock};

use axum::middleware;
use axum::routing::get;
use axum::Router;

use crate::auth;
use crate::handlers;
use crate::storage::StationStore;

pub struct AppState {
    pub api_key: String,
    pub store: RwLock<StationStore>,
}

impl AppState {
    pub fn from_env() -> Self {
        let api_key = std::env::var("WEATHER_API_KEY").unwrap_or_default();
        Self {
            api_key,
            store: RwLock::new(StationStore::new()),
        }
    }
}

pub fn build_router(state: Arc<AppState>) -> Router {
    let protected = Router::new()
        .route("/current", get(handlers::current))
        .route("/forecast", get(handlers::forecast))
        .with_state(state.clone())
        .layer(middleware::from_fn_with_state(state.clone(), auth::require_api_key));

    let open = Router::new()
        .route("/health", get(handlers::health));

    open.merge(protected)
}
