package auth

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

// deps are Mount's dependencies for a test: a store, a webhook secret and a Clerk secret key that is never used for a
// network call (the Directory is a fake or absent).
func deps(st *store.Store, webhookSecret string) Deps {
	return Deps{
		Config: config.Config{AppOrigin: testOrigin, ClerkSecretKey: "sk_test_unused", ClerkWebhookSecret: webhookSecret},
		Store:  st,
	}
}

func TestMain(m *testing.M) { os.Exit(storetest.Run(m)) }

// newStore returns a Store over a fresh, migrated database of the shared test container.
func newStore(t *testing.T) *store.Store {
	t.Helper()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	mig, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mig.Close() }()
	if _, err := mig.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return store.New(pool)
}

// fakeDir is a Directory over maps, with a record of what was asked.
type fakeDir struct {
	mu    sync.Mutex
	users map[string]ClerkUser
	orgs  map[string]ClerkOrg
	err   error // returned for every call when set
	calls []string
}

func newFakeDir() *fakeDir {
	return &fakeDir{users: map[string]ClerkUser{}, orgs: map[string]ClerkOrg{}}
}

func (d *fakeDir) User(_ context.Context, id string) (ClerkUser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "user:"+id)
	if d.err != nil {
		return ClerkUser{}, d.err
	}
	u, ok := d.users[id]
	if !ok {
		return ClerkUser{}, ErrDirectoryNotFound
	}
	return u, nil
}

func (d *fakeDir) Organization(_ context.Context, id string) (ClerkOrg, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "org:"+id)
	if d.err != nil {
		return ClerkOrg{}, d.err
	}
	o, ok := d.orgs[id]
	if !ok {
		return ClerkOrg{}, ErrDirectoryNotFound
	}
	return o, nil
}

func (d *fakeDir) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = nil
}

func (d *fakeDir) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}
