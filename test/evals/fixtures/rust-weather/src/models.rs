//! JSON shape: Reading + Forecast.
//!
//! Forecast::synthetic produces 12 one-hour entries derived from the
//! latest reading. This is a fixture, not a real model — temperature
//! decays linearly toward 12C and humidity drifts toward 50.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Reading {
    pub station: String,
    pub temp_c: f32,
    pub humidity: u8,
    pub timestamp_unix: i64,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct Forecast {
    pub station: String,
    pub hours: Vec<Reading>,
}

pub const FORECAST_HOURS: usize = 12;

impl Forecast {
    pub fn synthetic(reading: &Reading) -> Self {
        let mut hours = Vec::with_capacity(FORECAST_HOURS);
        for i in 0..FORECAST_HOURS {
            let ratio = (i as f32) / (FORECAST_HOURS as f32);
            let temp = reading.temp_c + (12.0 - reading.temp_c) * ratio;
            let hum =
                (reading.humidity as i32 + ((50 - reading.humidity as i32) * (i as i32)) / FORECAST_HOURS as i32) as u8;
            hours.push(Reading {
                station: reading.station.clone(),
                temp_c: temp,
                humidity: hum,
                timestamp_unix: reading.timestamp_unix + ((i as i64) + 1) * 3600,
            });
        }
        Forecast {
            station: reading.station.clone(),
            hours,
        }
    }
}
