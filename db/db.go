// Package db is the PostgreSQL data source for Uroboros (replacing the old single-file SQLite graph).
// It opens the connection, applies the schema, and seeds the built-in agents and variable catalog.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver ("pgx")
	"github.com/nobanic/UroborOS/config"
)

const schemaMigrationLockKey int64 = 7337741001

// maxOpenConns is the connection-pool cap; see the note in Open. It's well below PostgreSQL's default
// max_connections=100, and well above the app's own nested connection-acquire depth (the startup schema
// advisory lock holds one connection while seedBuiltins takes another), so it won't self-lock.
const maxOpenConns = 32

var schemaDeadlockRetryDelays = [...]time.Duration{
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
}

type schemaExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func isPostgresDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

func applySchemaWithRetry(ctx context.Context, execer schemaExecer, sleep func(time.Duration)) error {
	for attempt := 0; ; attempt++ {
		if _, err := execer.ExecContext(ctx, schemaSQL); err != nil {
			if !isPostgresDeadlock(err) || attempt >= len(schemaDeadlockRetryDelays) {
				return err
			}
			sleep(schemaDeadlockRetryDelays[attempt])
			continue
		}
		return nil
	}
}

// withSchemaMigrationLock pins the session-level lock to one checked-out
// connection. Running pg_advisory_lock through *sql.DB is incorrect because a
// later schema or unlock call may use a different pooled PostgreSQL session.
func withSchemaMigrationLock(ctx context.Context, sqlDB *sql.DB, action func(*sql.Conn) error) (err error) {
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	defer func() {
		if _, unlockErr := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, schemaMigrationLockKey); unlockErr != nil && err == nil {
			err = fmt.Errorf("advisory unlock: %w", unlockErr)
		}
	}()
	return action(conn)
}

// coordinateWithSchemaMigration makes long, multi-table archive transactions
// mutually exclusive with startup DDL while allowing ordinary runtime queries
// to continue normally.
func coordinateWithSchemaMigration(tx *sql.Tx) error {
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("coordinate with schema migration: %w", err)
	}
	return nil
}

// DSN resolves the PostgreSQL connection string and reports where it came from.
// Precedence: env UROBOROS_PG_DSN > config file (config.json). There is no
// built-in default — it errors if neither source is configured.
func DSN() (dsn, source string, err error) {
	return config.PostgresDSN()
}

// DB wraps the shared *sql.DB. PG handles its own connection pool + concurrency
// (MVCC), so unlike the old SQLite store there is no process-wide write mutex.
type DB struct{ *sql.DB }

// ensureDatabase connects to the postgres system database and creates the target
// database if it does not exist. dsn must be a postgres:// URL.
func ensureDatabase(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil // unparseable DSN — let the normal Open fail with a clear error
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" || dbName == "postgres" {
		return nil
	}
	// connect to the postgres maintenance database instead
	adminDSN := *u
	adminDSN.Path = "/postgres"
	admin, err := sql.Open("pgx", adminDSN.String())
	if err != nil {
		return nil // best-effort; let Open surface the real error
	}
	defer admin.Close()
	if err := admin.Ping(); err != nil {
		return nil
	}
	var exists bool
	_ = admin.QueryRow(`SELECT true FROM pg_database WHERE datname=$1`, dbName).Scan(&exists)
	if !exists {
		if _, err := admin.Exec(`CREATE DATABASE "` + dbName + `"`); err != nil {
			return fmt.Errorf("create database %q: %w", dbName, err)
		}
	}
	return nil
}

// Open connects, applies the schema (idempotent), and seeds builtin rows.
func Open(dsn string) (*DB, error) {
	if err := ensureDatabase(dsn); err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	// database/sql doesn't cap connections by default: when the pool has no idle connection it opens a new
	// one unconditionally, all the way up to PostgreSQL's max_connections (default 100) before being refused,
	// so peak-time queries get the **error** `FATAL: sorry, too many clients already`. With a cap, excess
	// queries queue for an idle connection instead — under the same load it gets slower rather than erroring,
	// and callers no longer have to distinguish "couldn't read" from "not there". maxOpenConns must leave
	// headroom for psql / reset-password.sh and any other coexisting instances; if max_connections was lowered, lower this too.
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxOpenConns)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping postgres (%s): %w", config.Redact(dsn), err)
	}
	d := &DB{sqlDB}
	// pgx runs multi-statement Exec via the simple protocol when there are no args.
	// Keep the dedicated lock connection checked out until both DDL and seeding
	// finish so concurrent application instances cannot initialize out of order.
	err = withSchemaMigrationLock(context.Background(), sqlDB, func(conn *sql.Conn) error {
		if err := applySchemaWithRetry(context.Background(), conn, time.Sleep); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		if err := d.seedBuiltins(); err != nil {
			return fmt.Errorf("seed builtins: %w", err)
		}
		return nil
	})
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// builtinAgent describes one of the fixed agents and its prompt-variable catalog.
type builtinAgent struct {
	key, name, role, desc string
	vars                  []promptVar
	webSearch             bool // default audited-research switch at row creation; ON CONFLICT preserves later user changes
	interactiveShell      bool // default interactive-shell switch at row creation; ON CONFLICT doesn't override a later manual user toggle
	runSeconds            *int // per-run wall-clock cap (seconds) at row creation; nil=use the seed default (1200), 0=unlimited
}

type promptVar struct{ name, desc, example, source string }

// intp returns a pointer to v, for explicitly setting optional builtinAgent fields (such as runSeconds).
func intp(v int) *int { return &v }

// builtinAgents mirrors docs §5(a). Built-in tools aren't stored in the DB; here we only seed agents + the variable catalog.
// Note: the interactive shell for planner/worker/mainagent/auto is set to true uniformly by the
// interactive_shell_default_v1 block below (respecting a later toggle); the interactiveShell here is only for a new agent that needs "default-on at row creation".
var builtinAgents = []builtinAgent{
	{"goals", "Goal Decomposition", "goals", "Break a penetration-testing task goal into several independent, verifiable sub-goals.", []promptVar{
		{"EngagementDescription", "task description (target/background)", "test the example.com site", "exploration"},
		// Now is a global runtime variable (see server.globalPromptVars) and is no longer redefined in each
		// agent's catalog, otherwise withGlobalVars would collide with the global entry on append.
	}, false, false, nil},
	{"planner", "Planning", "planner", "Read the situation, judge the goal, and add exploration intents only when there's genuinely a new uncovered direction (one planning loop per task).", []promptVar{
		{"Goal", "overall task goal", "take over admin on example.com", "exploration"},
		{"AssetSummary", "asset count / type-distribution summary (optional)", "domain:3 ip:5 site:2", "distilled"},
	}, true, false, nil},
	{"mainagent", "Main", "main", "Human-machine interface: observe progress and turn the human's intent into a hint or a high-priority intent.", []promptVar{
		{"Goal", "current task goal", "take over admin on example.com", "exploration"},
		{"AssetSummary", "opening situation summary (optional)", "domain:3 ip:5", "distilled"},
		{"FindingsSummary", "confirmed-findings summary (optional)", "high:1 medium:2", "distilled"},
	}, true, false, nil},
	{"worker", "Worker", "worker", "Claim one intent and execute it, write the discovered facts/findings back to the knowledge graph, then stop.", []promptVar{
		{"ProxyAddr", "recording proxy address (drives the if dual-copy)", "127.0.0.1:8080", "runtime"},
		{"WorkerName", "worker self-identifier (optional)", "worker-1", "runtime"},
	}, true, false, nil},
	// Auto: the built-in "platform operations" agent. It doesn't take part in the pentest orchestration loop; it's driven from the chat page and operates the platform with tools.
	{"auto", "Auto", "assistant", "Platform-operations assistant: use tools to manage tasks (create/view/pause/hint) and assets, and can create/modify skills, custom tools, and MCP.", nil, true, false, nil},
	// pentest: the built-in "standalone pentest" agent. Driven from the chat page, it single-handedly runs the whole pentest chain from recon to wrap-up, planning, executing, and verifying on its own. Interactive shell is on by default.
	{"pentest", "Penetration Testing", "assistant", "Standalone pentest agent: single-handedly runs the full chain from recon → attack-surface discovery → deep exploitation → verification → wrap-up, planning, executing, and adversarially verifying on its own.", nil, true, true, intp(0)},
}

// seedBuiltins inserts the fixed built-in agents and their variable catalog (idempotent).
func (d *DB) seedBuiltins() error {
	return (installationCatalog{db: d}).seed()
}

// seedDefaultAssetInterceptRules inserts the built-in asset blocklist (fuzzy
// domain matches for government / education sites) once on first startup. Gated
// by a settings flag so a user's later disable/delete is never resurrected on
// restart — same policy as the intercept-rule seed.
func (d *DB) seedDefaultAssetInterceptRules() error {
	return (installationCatalog{db: d}).assetRules()
}

type assetInterceptSeed struct {
	kind    string
	pattern string
	note    string
}

var defaultAssetInterceptRules = []assetInterceptSeed{
	{"fuzzy_domain", ".gov", "[built-in] government site (.gov)"},
	{"fuzzy_domain", ".edu", "[built-in] education site (.edu)"},
}

// V2 removes the two country-specific rules that shipped in the old default
// set. Only built-in rows are touched: an operator-created rule with the same
// pattern remains intact. The version flag keeps the migration one-shot.
func (d *DB) reconcileDefaultAssetInterceptRulesV2() error {
	return (installationCatalog{db: d}).reconcileAssetRules()
}

// builtinSkillVisibility maps a shipped skill's directory name → the built-in
// agent keys that should see it by default. The skill FILES themselves live on the
// filesystem (SkillDir, loaded by norma at runtime); DB only carries this visibility
// binding. Skills omitted here (for example playwright-cli) ship invisible by
// default — the user turns them on per-agent when needed.
var builtinSkillVisibility = map[string][]string{
	"api-recon": {"auto", "pentest", "worker"},
}

// seedBuiltinSkillVisibility binds the shipped built-in skills to their default
// agents. Insert-if-absent (ON CONFLICT DO NOTHING) so a user's later toggle-off is
// never resurrected on restart — matches the browser-MCP / intercept-rule seed policy.
func (d *DB) seedBuiltinSkillVisibility() error {
	return (installationCatalog{db: d}).skills()
}

// seedDefaultInterceptRules inserts built-in safety intercept rules once on
// first startup. The seed is gated by a settings flag so user edits (disable,
// delete, re-order) are never overwritten on subsequent restarts.
func (d *DB) seedDefaultInterceptRules() error {
	return (installationCatalog{db: d}).commandRulesV1()
}

// seedDefaultInterceptRulesV2 migrates the two safety patterns that used to be
// hard-coded in guard.go (destructive shell + data-exfil pipe) into ordinary
// intercept rules. Gated by its own flag so it also lands on DBs that already ran
// v1. Unlike the old guard.go floor, these are plain [built-in] rules — the user can
// disable or delete them. The exfil rule ships DISABLED by default (its
// curl/wget/nc pipe pattern mis-fires on legitimate CTF/pentest reverse-shell and
// data-transfer pipes); enable it manually when exfil gating is actually wanted.
func (d *DB) seedDefaultInterceptRulesV2() error {
	return (installationCatalog{db: d}).commandRulesV2()
}

// seedDefaultInterceptRulesV3 adds the delete-endpoint path rule. The v1 HTTP rules
// only catch the DELETE *method* (curl -X DELETE, requests.delete(, method:'DELETE'),
// and v1's path rule covers only /clear /wipe /flush /purge /truncate /drop /destroy
// /factory-reset /reset-all — so a plain `curl 'http://t/api/user/delete?id=1'` (a
// delete endpoint reached with GET/POST, which is how most web apps expose deletion)
// slipped through every built-in rule. Own flag so it also lands on DBs that already
// ran v1/v2, where editing the v1 seed would have no effect.
//
// The pattern deliberately requires a separator after the verb so /delivery,
// /details, /delta and /delegate do not match, while /deleteAll, /delete_user and
// /delete-user do. destroy is re-covered here because v1's rule does not allow a
// suffix (/destroyAll was missed).
//
// Exported as a package const only so the seeded regex is unit-testable without a DB.
const deleteEndpointPathPattern = `(?i)/(?:(?:delete|remove|unlink|erase|destroy)[-\w]*|del)(?:[/?#"'\s]|$)`

func (d *DB) seedDefaultInterceptRulesV3() error {
	return (installationCatalog{db: d}).commandRulesV3()
}
