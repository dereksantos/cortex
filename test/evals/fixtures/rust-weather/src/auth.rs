//! API-key middleware.
//!
//! Requires `X-API-Key` matching `AppState.api_key`. The `/health`
//! endpoint mounts outside this layer and is intentionally unauthed.

use std::sync::Arc;

use axum::extract::{Request, State};
use axum::http::StatusCode;
use axum::middleware::Next;
use axum::response::Response;

use crate::router::AppState;

pub const HEADER: &str = "x-api-key";

pub async fn require_api_key(
    State(state): State<Arc<AppState>>,
    req: Request,
    next: Next,
) -> Result<Response, StatusCode> {
    if state.api_key.is_empty() {
        // Misconfigured server — fail closed.
        return Err(StatusCode::UNAUTHORIZED);
    }
    let presented = req
        .headers()
        .get(HEADER)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    if presented != state.api_key {
        return Err(StatusCode::UNAUTHORIZED);
    }
    Ok(next.run(req).await)
}
