// Package storetest starts a throwaway Postgres in Docker for tests: the image compose.yaml and CI use, by the same
// digest (change all together). One container serves a whole test binary; every test gets its own database in it.
//
// Roles are cluster-wide in Postgres, so tests that create roles (the migrate/app split) must run one after another
// and remove what they made. Do not call t.Parallel in such a test.
//
// Docker missing skips the test with a message, except in CI (CI=true), where it fails: a skipped database test
// there would be a green build that proved nothing.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Image is the Postgres image of the tests: Postgres 18 (native uuidv7()), pinned like compose.yaml and
// .github/workflows/ci.yml, which use the same digest.
const Image = "pgvector/pgvector:pg18@sha256:2358fcba361ed2233a5ed81b5fe4ca779ccb304120ce531a3bf51c0ed7e2bc11"

// The superuser of the container.
const (
	SuperUser     = "postgres"
	SuperPassword = "postgres"
)

var (
	mu        sync.Mutex
	container *postgres.PostgresContainer
	server    *Server
)

// Server is the running container.
type Server struct{ hostPort string }

// Run is for TestMain: `func TestMain(m *testing.M) { os.Exit(storetest.Run(m)) }`. It runs the tests, then stops
// the container, so nothing is left behind even when the test reaper is disabled.
func Run(m *testing.M) int {
	code := m.Run()
	mu.Lock()
	defer mu.Unlock()
	if container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = container.Terminate(ctx)
		container, server = nil, nil
	}
	return code
}

// Start returns the container's server, starting it on first use.
func Start(t *testing.T) *Server {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if server != nil {
		return server
	}
	if err := dockerAvailable(); err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("Docker is required in CI: %v", err)
		}
		t.Skipf("skipping: Docker is not available (%v). Start Docker to run the database tests", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ctr, err := postgres.Run(ctx, Image, postgres.BasicWaitStrategies())
	if err != nil {
		if ctr != nil {
			_ = ctr.Terminate(context.Background())
		}
		t.Fatalf("start %s: %v", Image, err)
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}
	container = ctr
	server = &Server{hostPort: net.JoinHostPort(host, port.Port())}
	return server
}

func dockerAvailable() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return err
	}
	return provider.Health(context.Background())
}

// DSN returns a connection URL for database db.
func (s *Server) DSN(db, user, password string) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(user, password), Host: s.hostPort, Path: "/" + db, RawQuery: "sslmode=disable"}
	return u.String()
}

// SuperDSN returns a connection URL for database db as the container's superuser.
func (s *Server) SuperDSN(db string) string { return s.DSN(db, SuperUser, SuperPassword) }

// Exec runs statements in database db as the superuser.
func (s *Server) Exec(t *testing.T, db string, statements ...string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, s.SuperDSN(db))
	if err != nil {
		t.Fatalf("connect to %s: %v", db, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, stmt := range statements {
		if _, err := conn.Exec(ctx, stmt, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// NewDatabase creates an empty database owned by owner (the superuser when empty) and drops it when the test ends.
// It returns the database name.
func (s *Server) NewDatabase(t *testing.T, owner string) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name := "t_" + hex.EncodeToString(b[:])
	if owner == "" {
		owner = SuperUser
	}
	s.Exec(t, "postgres", fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, name, pgx.Identifier{owner}.Sanitize()))
	t.Cleanup(func() {
		// WITH (FORCE) drops the database even if a pool of the test is still connected.
		s.Exec(t, "postgres", fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
	})
	return name
}

// DropRoles removes roles if they exist, so a test starts from a cluster without them. Drop the databases that use
// the roles first (a test's cleanup does that).
func (s *Server) DropRoles(t *testing.T, roles ...string) {
	t.Helper()
	for _, r := range roles {
		s.Exec(t, "postgres", `DROP ROLE IF EXISTS `+pgx.Identifier{r}.Sanitize())
	}
}
