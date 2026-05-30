//! Endpoint handlers.

use std::sync::Arc;

use axum::extract::{Query, State};
use axum::http::StatusCode;
use axum::Json;
use serde::Deserialize;

use crate::models::{Forecast, Reading};
use crate::router::AppState;

#[derive(Deserialize)]
pub struct StationQuery {
    pub station: String,
}

pub async fn health() -> Json<serde_json::Value> {
    Json(serde_json::json!({ "ok": true }))
}

pub async fn current(
    State(state): State<Arc<AppState>>,
    Query(q): Query<StationQuery>,
) -> Result<Json<Reading>, StatusCode> {
    let store = state.store.read().map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;
    store
        .latest(&q.station)
        .cloned()
        .map(Json)
        .ok_or(StatusCode::NOT_FOUND)
}

pub async fn forecast(
    State(state): State<Arc<AppState>>,
    Query(q): Query<StationQuery>,
) -> Result<Json<Forecast>, StatusCode> {
    let store = state.store.read().map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;
    let reading = store.latest(&q.station).ok_or(StatusCode::NOT_FOUND)?;
    Ok(Json(Forecast::synthetic(reading)))
}
