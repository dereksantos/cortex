//! In-memory station cache.
//!
//! Keyed by station id; only the most-recent reading per station is
//! retained. Capacity is bounded by MAX_STATIONS — older stations are
//! evicted oldest-first when the cap is reached.

use std::collections::VecDeque;
use std::collections::HashMap;

use crate::models::Reading;

pub const MAX_STATIONS: usize = 64;

pub struct StationStore {
    latest: HashMap<String, Reading>,
    order: VecDeque<String>,
}

impl StationStore {
    pub fn new() -> Self {
        Self {
            latest: HashMap::new(),
            order: VecDeque::with_capacity(MAX_STATIONS),
        }
    }

    pub fn record(&mut self, reading: Reading) {
        if !self.latest.contains_key(&reading.station) {
            if self.order.len() >= MAX_STATIONS {
                if let Some(evict) = self.order.pop_front() {
                    self.latest.remove(&evict);
                }
            }
            self.order.push_back(reading.station.clone());
        }
        self.latest.insert(reading.station.clone(), reading);
    }

    pub fn latest(&self, station: &str) -> Option<&Reading> {
        self.latest.get(station)
    }

    pub fn len(&self) -> usize {
        self.latest.len()
    }
}
