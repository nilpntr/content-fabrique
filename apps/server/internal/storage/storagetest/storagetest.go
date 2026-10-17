// Package storagetest starts a throwaway MinIO in Docker for tests: the image compose.yaml and CI use, by the same digest
// (change all together). One container serves a whole test binary; every test gets its own bucket in it.
//
// Docker missing skips the test with a message, except in CI (CI=true), where it fails: a skipped storage test there
// would be a green build that proved nothing.
package storagetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nilpntr/content-fabrique/internal/storage"
)

// Image is the MinIO image of the tests: Chainguard's build, pinned like compose.yaml and .github/workflows/ci.yml, which
// use the same digest.
const Image = "cgr.dev/chainguard/minio@sha256:2136d593beacf4461187035eca949fe49abd589d5079735025c711ca58fee0c8"

// The root credentials of the container (the same as the local .env defaults).
const (
	AccessKey = "minioadmin"
	SecretKey = "minioadmin"
)

var (
	mu        sync.Mutex
	container testcontainers.Container
	endpoint  string
)

// Run is for TestMain: `func TestMain(m *testing.M) { os.Exit(storagetest.Run(m)) }`. It runs the tests, then stops the
// container.
func Run(m *testing.M) int {
	code := m.Run()
	Stop()
	return code
}

// Stop terminates the container if one was started. A TestMain that also runs another container calls it after its tests.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = container.Terminate(ctx)
		container, endpoint = nil, ""
	}
}

// Start returns the endpoint of the container (http://host:port), starting it on first use.
func Start(t testing.TB) string {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if container != nil {
		return endpoint
	}
	if err := dockerAvailable(); err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("Docker is required in CI: %v", err)
		}
		t.Skipf("skipping: Docker is not available (%v). Start Docker to run the object storage tests", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: Image,
			// The image's entrypoint is /usr/bin/minio. /tmp is writable for its unprivileged user.
			Cmd:          []string{"server", "/tmp/data"},
			ExposedPorts: []string{"9000/tcp"},
			Env:          map[string]string{"MINIO_ROOT_USER": AccessKey, "MINIO_ROOT_PASSWORD": SecretKey},
			WaitingFor:   wait.ForHTTP("/minio/health/ready").WithPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		if ctr != nil {
			_ = ctr.Terminate(context.Background())
		}
		t.Fatalf("start %s: %v", Image, err)
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		_ = ctr.Terminate(context.Background())
		t.Fatalf("container host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "9000/tcp")
	if err != nil {
		_ = ctr.Terminate(context.Background())
		t.Fatalf("container port: %v", err)
	}
	container = ctr
	endpoint = "http://" + net.JoinHostPort(host, port.Port())
	return endpoint
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

// NewBucket creates a bucket of its own and returns the storage configuration for it.
func NewBucket(t testing.TB) storage.Config {
	t.Helper()
	ep := Start(t)
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		t.Fatal(err)
	}
	cfg := storage.Config{Endpoint: ep, Bucket: "t-" + hex.EncodeToString(rnd[:]), AccessKeyID: AccessKey, SecretAccessKey: SecretKey}
	if _, err := S3(cfg).CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return cfg
}

// S3 returns a plain SDK client for cfg (path style, as MinIO needs), for tests that look at the bucket directly.
func S3(cfg storage.Config) *s3.Client {
	return s3.New(s3.Options{
		Region:                     "us-east-1",
		BaseEndpoint:               aws.String(cfg.Endpoint),
		Credentials:                credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		UsePathStyle:               true,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}
