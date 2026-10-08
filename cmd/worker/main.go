package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"k8s.io/client-go/dynamic"

	"db-paas/pkg/provisioner/cnpg"
	"db-paas/pkg/provisioner/fake"
	"db-paas/pkg/repository/postgres"
	"db-paas/pkg/worker"
)

func main() {
	var (
		dbURL          string
		healthAddr     string
		concurrency    int
		interval       time.Duration
		reclaimAfter   time.Duration
		provisionerArg string
		provisionDelay time.Duration
		kubeconfig     string
		storageClass   string
		imageRepo      string
		s3Endpoint     string
		s3Bucket       string
	)
	flag.StringVar(&dbURL, "database-url", os.Getenv("DATABASE_URL"), "Postgres connection string (default: $DATABASE_URL)")
	flag.StringVar(&healthAddr, "health-addr", ":8081", "address for the /healthz probe endpoint (empty = disabled)")
	flag.IntVar(&concurrency, "concurrency", 4, "max provisions in flight")
	flag.DurationVar(&interval, "interval", 2*time.Second, "how often to poll for work")
	flag.DurationVar(&reclaimAfter, "reclaim-after", 15*time.Minute, "retake rows stuck in provisioning for this long")
	flag.StringVar(&provisionerArg, "provisioner", "fake", "fake | cnpg")
	flag.DurationVar(&provisionDelay, "provision-delay", 5*time.Second, "fake provisioner: how long it takes")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "cnpg: kubeconfig path (default: in-cluster, then $KUBECONFIG, then ~/.kube/config)")
	flag.StringVar(&storageClass, "storage-class", "", "cnpg: StorageClass for database volumes (default: cluster default)")
	flag.StringVar(&imageRepo, "image-repo", "ghcr.io/cloudnative-pg/postgresql", "cnpg: Postgres image repository; the tag is the requested version")
	flag.StringVar(&s3Endpoint, "s3-endpoint", "", "cnpg: S3 endpoint as seen from inside the cluster, e.g. http://minio.minio.svc:9000")
	flag.StringVar(&s3Bucket, "s3-bucket", "cnpg-backups", "cnpg: backup bucket")
	flag.Parse()

	log, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer log.Sync()

	if dbURL == "" {
		log.Fatal("a database URL is required: set -database-url or $DATABASE_URL")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatal("connect to database", zap.Error(err))
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatal("ping database", zap.Error(err))
	}

	// ---- provisioner ----
	var prov worker.Provisioner
	switch provisionerArg {
	case "fake":
		prov = fake.Provisioner{Delay: provisionDelay}

	case "cnpg":
		restCfg, err := cnpg.RESTConfig(kubeconfig)
		if err != nil {
			log.Fatal("load kubernetes config", zap.Error(err))
		}
		client, err := dynamic.NewForConfig(restCfg)
		if err != nil {
			log.Fatal("create kubernetes client", zap.Error(err))
		}

		cfg := cnpg.Config{StorageClass: storageClass, ImageRepo: imageRepo}
		// credentials come from the environment, never from flags (visible in `ps`)
		accessKey, secretKey := os.Getenv("S3_ACCESS_KEY_ID"), os.Getenv("S3_SECRET_ACCESS_KEY")
		if s3Endpoint != "" && accessKey != "" && secretKey != "" {
			cfg.Backup = &cnpg.BackupStore{
				Bucket: s3Bucket, EndpointURL: s3Endpoint,
				AccessKeyID: accessKey, SecretAccessKey: secretKey,
			}
		} else {
			log.Warn("no backup store configured; databases with backups enabled will fail to provision")
		}
		prov = cnpg.New(client, cfg)

	default:
		log.Fatal("unknown -provisioner (want fake or cnpg)", zap.String("value", provisionerArg))
	}
	log.Info("provisioner selected", zap.String("kind", provisionerArg))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	w, err := worker.New(postgres.New(pool), prov, log, worker.Config{
		Interval:         interval,
		Concurrency:      concurrency,
		ProvisionTimeout: reclaimAfter * 2 / 3, // must stay shorter than reclaim-after
		ReclaimAfter:     reclaimAfter,
	})
	if err != nil {
		log.Fatal("create worker", zap.Error(err))
	}

	if healthAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, r *http.Request) {
			pctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := pool.Ping(pctx); err != nil {
				rw.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			rw.WriteHeader(http.StatusOK)
		})
		hs := &http.Server{Addr: healthAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("health server", zap.Error(err))
			}
		}()
		defer hs.Close()
	}

	w.Run(ctx)
	log.Info("worker exited")
}
