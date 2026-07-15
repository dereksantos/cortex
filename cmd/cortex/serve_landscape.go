// serve_landscape.go — M4.2c1: GET /api/landscape, the read-only half of
// the landscape/models slice GOAL.md §6 M4.2 bundles together (split here
// into M4.2c1 landscape / M4.2c2 models — STATE.md). Delegates to
// webui_landscape.go's buildLandscapeViewModel (M5.2c), itself a thin
// wrapper over scan.go's resolveScanRoots/buildScanReport, rather than
// inventing a parallel scan path — `cortex serve`'s landscape view and
// `cortex scan`'s CLI report can never drift on what roots or caps mean.
//
// A scan run via `cortex scan --root <path>` (without --register having
// ever answered the greeting) journals a landscape.scan event but, before
// scan.go's resolveAndPersistScanRoots, never persisted scan.roots — so
// this endpoint 412'd forever even right after a real scan ran. It now
// falls back to the latest journaled event (internal/journal's
// LatestLandscapeScan) when no roots are persisted, surfacing what was
// last seen (names/counts + roots, per the journal payload's
// content-non-leak shape) rather than a dead end; buildLandscapeResponse's
// Source field ("live" vs "journal") lets the UI say so.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
)

// LandscapeResponse is GET /api/landscape's (and POST
// /api/landscape/rescan's) wire shape: the same ScanReport `cortex scan
// --json` prints, plus HTTP-only metadata distinguishing a fresh scan just
// run against persisted roots ("live") from a fallback to the most recent
// journaled landscape.scan event when no roots are persisted yet
// ("journal"). ScannedAt is the live-scan's wall-clock time, or the
// journaled event's own timestamp.
type LandscapeResponse struct {
	ScanReport
	ScannedAt time.Time `json:"scanned_at"`
	Source    string    `json:"source"`
}

// buildLandscapeResponse resolves persisted scan roots and runs a live scan
// when present ("live" source, same as buildLandscapeViewModel); when none
// are persisted, falls back to the latest journaled landscape.scan event
// ("journal" source). Only returns ErrNoScanRoots when BOTH are absent —
// the true "onboarding never happened, and nothing has ever been scanned"
// case.
func buildLandscapeResponse(configPath, homeDir string) (LandscapeResponse, error) {
	roots, err := readScanRoots(configPath)
	if err != nil {
		return LandscapeResponse{}, err
	}
	if len(roots) > 0 {
		report, err := buildScanReport(homeDir, roots, defaultScanCaps)
		if err != nil {
			return LandscapeResponse{}, err
		}
		return LandscapeResponse{ScanReport: report, Source: "live", ScannedAt: time.Now().UTC()}, nil
	}

	payload, ts, found, err := journal.LatestLandscapeScan()
	if err != nil {
		return LandscapeResponse{}, err
	}
	if !found {
		return LandscapeResponse{}, ErrNoScanRoots
	}
	return LandscapeResponse{
		ScanReport: ScanReport{Roots: payload.Roots, Truncated: payload.Truncated},
		Source:     "journal",
		ScannedAt:  ts,
	}, nil
}

// handleLandscape serves GET /api/landscape: buildLandscapeResponse's
// live-or-journal report, built from the persisted scan.roots config key
// under configPath (GOAL.md D3 — never a blind $HOME sweep) and homeDir
// (harness/runtime probes), falling back to the last journaled scan when
// no roots are persisted. Neither `cortex scan`'s --root flag nor
// --register apply here — configuring roots is M1.7's greeting flow, or
// now `cortex scan --root` itself (scan.go's resolveAndPersistScanRoots),
// or POST /api/landscape/rescan below. ErrNoScanRoots surfaces as 412
// Precondition Failed: the client needs to finish onboarding, or run a
// scan at least once, before this endpoint has anything to report,
// distinct from a 500 the client can't act on.
func handleLandscape(configPath, homeDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report, err := buildLandscapeResponse(configPath, homeDir)
		if err != nil {
			if errors.Is(err, ErrNoScanRoots) {
				http.Error(w, err.Error(), http.StatusPreconditionFailed)
				return
			}
			http.Error(w, "failed to build landscape report: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, report)
	}
}

// handleLandscapeRescan serves POST /api/landscape/rescan: runs a fresh
// scan against the persisted scan.roots config key (never an explicit
// --root — this endpoint has no request body to carry one) and journals
// the result exactly like `cortex scan` does (recordLandscapeScan), so the
// "Rescan" button in the web UI and the CLI keep the journal in sync the
// same way. 412s under the same ErrNoScanRoots condition as GET
// /api/landscape's live branch — there is nothing to rescan without
// persisted roots. A journal-write failure is logged and swallowed
// (best-effort telemetry, matching runScanCLI's convention) rather than
// failing the request — the scan result itself is still valid.
func handleLandscapeRescan(configPath, homeDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roots, err := resolveScanRoots(configPath, "")
		if err != nil {
			if errors.Is(err, ErrNoScanRoots) {
				http.Error(w, err.Error(), http.StatusPreconditionFailed)
				return
			}
			http.Error(w, "failed to resolve scan roots: "+err.Error(), http.StatusInternalServerError)
			return
		}
		report, err := buildScanReport(homeDir, roots, defaultScanCaps)
		if err != nil {
			http.Error(w, "failed to run scan: "+err.Error(), http.StatusInternalServerError)
			return
		}
		scannedAt := time.Now().UTC()
		if err := recordLandscapeScan(report); err != nil {
			fmt.Fprintln(os.Stderr, "landscape rescan: warning: failed to record landscape.scan event:", err)
		}
		writeJSON(w, http.StatusOK, LandscapeResponse{ScanReport: report, Source: "live", ScannedAt: scannedAt})
	}
}
