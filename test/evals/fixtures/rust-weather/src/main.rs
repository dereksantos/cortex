//! rust-weather binary entrypoint.
//!
//! Builds the shared state (storage + api key) and binds the router
//! produced by `router::build_router`. The server is single-process
//! and stores its station cache in-memory; restart resets the cache.

mod auth;
mod handlers;
mod models;
mod router;
mod storage;

use std::net::SocketAddr;
use std::sync::Arc;

use crate::router::AppState;

pub const PORT: u16 = 8088;

#[tokio::main]
async fn main() {
    let state = Arc::new(AppState::from_env());
    let app = router::build_router(state.clone());

    let addr: SocketAddr = ([0, 0, 0, 0], PORT).into();
    let listener = tokio::net::TcpListener::bind(addr).await.expect("bind");
    eprintln!("rust-weather listening on {}", addr);
    axum::serve(listener, app).await.expect("serve");
}
