# Dependency attribution

The original [GNU Affero General Public License](LICENSE) is retained unchanged.
Original source headers and module identities are preserved in this package.

## Vendored Go dependency

`third_party/norma/` contains the source module `github.com/nobanic/norma`,
referenced as version `v0.4.3` and selected by the local `replace` directive in
`go.mod`. It is part of the supplied Uroboros source snapshot. That snapshot
contains no separate license file inside the vendored module; this package does
not invent a separate license or remove the module's source attribution.

## Dependencies obtained during build

The frontend dependencies and their exact resolutions are recorded in
`web/package.json` and `web/package-lock.json`. The Go dependencies are recorded
in `go.mod` and `go.sum`. Python dependencies and tool artifacts are pinned by
the lock files and Dockerfiles in their respective components.

The browser, discovery, scanner and binary-analysis engines remain external
components. Their original projects and release artifacts are identified in
the Dockerfiles, lock files and runtime manifests. The application does not
claim authorship of those engines. Preserve the license and notice material
provided with their distributions.

The two local binary runtime archives are unchanged approved artifacts with
the identities listed in `clean/runtime-images/MANIFEST.md`. No application
source from the retired reference package replaces the current implementation.
