// Package projectscan provides filesystem-scanning primitives shared
// between Cortex's Dream sources (cognition/sources) and the
// project-study DAG (internal/study).
//
// The package centers on IgnoreSet, which combines three layers of
// path-filtering logic:
//
//  1. Hard exclusions (the set of directories and file basenames that
//     Cortex never reads regardless of .gitignore: node_modules, .git,
//     vendor, well-known secret files like id_rsa, etc.).
//  2. Extension blacklist (binary, archive, minified, lock files).
//  3. .gitignore parsing (a simplified parser that handles *, **, !, /).
//
// On top of those, IgnoreSet provides defense-in-depth for sensitive
// files in three layers:
//
//   - Layer 1 — Name-based: secret/credential/token substrings, known
//     credential prefixes (.aws/credentials, .ssh/, .gnupg/, .kube/config),
//     production dotenv variants, and service-account JSON.
//   - Layer 2 — Extension regex: (?i)\.(pem|key|p12|pfx|kdbx|gpg|asc|
//     pgp|enc|jks|keystore)$.
//   - Layer 3 — Magic-byte sniff: open the file, read first 200 bytes,
//     reject if "-----BEGIN" appears. Catches PGP keys, RSA/EC/DSA
//     private keys, X.509 certs, and encrypted blobs that slip past
//     naming conventions.
//
// Why three layers: the deny list is destined to drift as new exfil
// patterns ship; the magic-byte sniff is the durable layer that survives
// drift. Filter at scan source — once chunk bytes reach the LLM, they
// have crossed the journal-path boundary journal.AssertLocalOnly guards.
package projectscan
