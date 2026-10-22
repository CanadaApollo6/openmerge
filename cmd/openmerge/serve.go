package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/github"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/serve"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type serveOptions struct {
	authorityPlan, emissionReceipt                                 string
	emissionReceiptMaxAge                                          time.Duration
	database, key, repos, base, cache, listen, secret              string
	app, installation                                              int64
	logLevel, committerName, committerEmail                        string
	apiListen, apiToken, apiTokenFile                              string
	poll, rulesInterval, discoveryInterval                         time.Duration
	backstop, webhookHealthWindow                                  time.Duration
	healthGrace, healthMaxAge, healthMaxCycle, healthProgressStall time.Duration
	concurrency                                                    int
	deleteHeadBranch                                               bool
}

func parseServe(args []string, output io.Writer) (serveOptions, error) {
	var o serveOptions
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(output)
	env := func(name, fallback string) string {
		if value, ok := os.LookupEnv("OPENMERGE_" + name); ok {
			return value
		}
		return fallback
	}
	flags.StringVar(&o.authorityPlan, "authority-plan", env("AUTHORITY_PLAN", ""), "reviewed verification plan with proposed event IDs")
	flags.StringVar(&o.emissionReceipt, "emission-receipt", env("EMISSION_RECEIPT", ""), "database-bound drain and tested-backup receipt")
	flags.DurationVar(&o.emissionReceiptMaxAge, "emission-receipt-max-age", authority.DefaultEmissionReceiptMaxAge, "maximum drain/backup receipt age")
	flags.StringVar(&o.database, "database-url", env("DATABASE_URL", ""), "Postgres URL (OPENMERGE_DATABASE_URL)")
	flags.StringVar(&o.key, "github-private-key", env("GITHUB_PRIVATE_KEY", ""), "RSA PEM key path (OPENMERGE_GITHUB_PRIVATE_KEY)")
	flags.StringVar(&o.repos, "repos", env("REPOS", ""), "deprecated optional comma-separated owner/repo glob allowlist (OPENMERGE_REPOS)")
	flags.StringVar(&o.base, "base", env("BASE", ""), "deprecated optional comma-separated base branch/glob allowlist (OPENMERGE_BASE)")
	flags.StringVar(&o.cache, "git-cache-dir", env("GIT_CACHE_DIR", ".openmerge-cache"), "process-exclusive git cache (OPENMERGE_GIT_CACHE_DIR)")
	flags.StringVar(&o.listen, "webhook-listen", env("WEBHOOK_LISTEN", ""), "optional webhook address (OPENMERGE_WEBHOOK_LISTEN)")
	flags.StringVar(&o.secret, "webhook-secret", env("WEBHOOK_SECRET", ""), "webhook HMAC secret (OPENMERGE_WEBHOOK_SECRET)")
	flags.StringVar(&o.apiListen, "api-listen", env("API_LISTEN", ""), "optional status API address; :port binds loopback (OPENMERGE_API_LISTEN)")
	flags.StringVar(&o.apiTokenFile, "api-token-file", env("API_TOKEN_FILE", ""), "API bearer token file (or OPENMERGE_API_TOKEN)")
	flags.StringVar(&o.logLevel, "log-level", env("LOG_LEVEL", "info"), "debug, info, warn or error (OPENMERGE_LOG_LEVEL)")
	flags.StringVar(&o.committerName, "git-committer-name", env("GIT_COMMITTER_NAME", ""), "commit identity name; defaults to App login (OPENMERGE_GIT_COMMITTER_NAME)")
	flags.StringVar(&o.committerEmail, "git-committer-email", env("GIT_COMMITTER_EMAIL", ""), "commit identity email; defaults to numeric App bot no-reply email (OPENMERGE_GIT_COMMITTER_EMAIL)")
	// Parse typed environment values through flag.Value so malformed values fail
	// startup, while an explicit flag can override the environment.
	flags.Int64Var(&o.app, "github-app-id", 0, "App ID (OPENMERGE_GITHUB_APP_ID)")
	flags.Int64Var(&o.installation, "github-installation-id", 0, "deprecated optional installation allowlist (OPENMERGE_GITHUB_INSTALLATION_ID)")
	flags.DurationVar(&o.poll, "poll-interval", time.Minute, "reconcile interval (OPENMERGE_POLL_INTERVAL)")
	flags.DurationVar(&o.backstop, "backstop-interval", 10*time.Minute, "idle polling interval while webhooks are healthy (OPENMERGE_BACKSTOP_INTERVAL)")
	flags.DurationVar(&o.webhookHealthWindow, "webhook-health-window", 30*time.Minute, "verified delivery health window (OPENMERGE_WEBHOOK_HEALTH_WINDOW)")
	flags.DurationVar(&o.discoveryInterval, "discovery-interval", 10*time.Minute, "installation/repository discovery interval (OPENMERGE_DISCOVERY_INTERVAL)")
	flags.DurationVar(&o.rulesInterval, "rules-interval", 10*time.Minute, "repository rules validation interval (OPENMERGE_RULES_INTERVAL)")
	flags.BoolVar(&o.deleteHeadBranch, "delete-head-branch", true, "default for deleting eligible landed heads; repository config overrides (OPENMERGE_DELETE_HEAD_BRANCH)")
	flags.DurationVar(&o.healthProgressStall, "health-progress-stall-limit", status.DefaultHealthSettings().ProgressStallLimit, "health maximum time without cycle progress, at least action/cleanup/grace budget, at most 30m (OPENMERGE_HEALTH_PROGRESS_STALL_LIMIT)")
	flags.DurationVar(&o.healthMaxCycle, "health-max-cycle-duration", 3*time.Hour, "health maximum cycle runtime, 2m–6h (OPENMERGE_HEALTH_MAX_CYCLE_DURATION)")
	flags.DurationVar(&o.healthGrace, "health-overdue-grace", 30*time.Second, "health alert grace, 1–60s (OPENMERGE_HEALTH_OVERDUE_GRACE)")
	flags.DurationVar(&o.healthMaxAge, "health-max-success-age", 15*time.Minute, "health maximum reconciliation age, 1–60m (OPENMERGE_HEALTH_MAX_SUCCESS_AGE)")
	flags.IntVar(&o.concurrency, "concurrency", 4, "concurrent queues (OPENMERGE_CONCURRENCY)")
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	overridden := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { overridden[f.Name] = true })
	for _, entry := range []struct{ flag, env string }{{"health-progress-stall-limit", "HEALTH_PROGRESS_STALL_LIMIT"}, {"health-max-cycle-duration", "HEALTH_MAX_CYCLE_DURATION"}, {"health-overdue-grace", "HEALTH_OVERDUE_GRACE"}, {"health-max-success-age", "HEALTH_MAX_SUCCESS_AGE"}, {"delete-head-branch", "DELETE_HEAD_BRANCH"}, {"github-app-id", "GITHUB_APP_ID"}, {"github-installation-id", "GITHUB_INSTALLATION_ID"}, {"poll-interval", "POLL_INTERVAL"}, {"backstop-interval", "BACKSTOP_INTERVAL"}, {"webhook-health-window", "WEBHOOK_HEALTH_WINDOW"}, {"discovery-interval", "DISCOVERY_INTERVAL"}, {"rules-interval", "RULES_INTERVAL"}, {"concurrency", "CONCURRENCY"}} {
		if value, ok := os.LookupEnv("OPENMERGE_" + entry.env); ok && !overridden[entry.flag] {
			if err := flags.Set(entry.flag, value); err != nil {
				return o, fmt.Errorf("OPENMERGE_%s: %w", entry.env, err)
			}
		}
	}
	if o.base != "" {
		for _, base := range strings.Split(o.base, ",") {
			if base == "" {
				return o, errors.New("base allowlist must not contain empty selectors")
			}
			if _, err := condition.CompileGlob(base); err != nil {
				return o, fmt.Errorf("invalid base allowlist: %w", err)
			}
		}
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(o.logLevel)); err != nil {
		return o, fmt.Errorf("invalid log level: %w", err)
	}
	if flags.NArg() != 0 {
		return o, errors.New("serve accepts flags only")
	}
	if o.database == "" || o.key == "" || o.app <= 0 || o.installation < 0 {
		return o, errors.New("database URL, GitHub App ID and private key path are required; installation allowlist must be nonnegative")
	}
	if o.backstop <= 0 || o.webhookHealthWindow <= 0 || o.poll <= 0 || o.rulesInterval <= 0 || o.discoveryInterval <= 0 || o.concurrency <= 0 || o.cache == "" {
		return o, errors.New("poll, backstop, webhook health, rules and discovery intervals, concurrency and git cache must be nonempty and positive")
	}
	if err := serve.ValidateHealthSettings(status.HealthSettings{OverdueGrace: o.healthGrace, MaxSuccessAge: o.healthMaxAge, MaxCycleDuration: o.healthMaxCycle, ProgressStallLimit: o.healthProgressStall}, serve.DefaultActionTimeout); err != nil {
		return o, err
	}
	if o.listen != "" && o.secret == "" {
		return o, errors.New("webhook listen requires a webhook secret")
	}
	var err error
	if o.apiListen != "" {
		o.apiToken, err = status.ReadToken(env("API_TOKEN", ""), o.apiTokenFile)
		if err != nil {
			return o, err
		}
	}
	o.apiListen, err = status.ListenAddress(o.apiListen, o.apiToken)
	if err != nil {
		return o, err
	}
	for _, repo := range splitAllowlist(o.repos) {
		parts := strings.Split(repo, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(repo, " \\#%\r\n") {
			return o, fmt.Errorf("invalid repository %q; expected owner/repo glob", repo)
		}
		if _, err := condition.CompileGlob(strings.ToLower(repo)); err != nil {
			return o, fmt.Errorf("invalid repository allowlist: %w", err)
		}
	}
	return o, nil
}

func runServe(args []string, stdout, stderr io.Writer) int {
	o, err := parseServe(args, stdout)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	var level slog.Level
	_ = level.UnmarshalText([]byte(o.logLevel)) // validated by parseServe
	sanitizer := diagnostic.NewSanitizer(o.apiToken, o.secret, o.database)
	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level, ReplaceAttr: sanitizer.LogAttr}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := startServe(ctx, o, logger, sanitizer); err != nil {
		logger.Error("serve stopped", "error", err)
		return 1
	}
	return 0
}

func startServe(ctx context.Context, o serveOptions, logger *slog.Logger, sanitizer *diagnostic.Sanitizer) error {
	db, err := store.Open(ctx, o.database)
	if err != nil {
		return fmt.Errorf("open/migrate database failed; check OPENMERGE_DATABASE_URL and Postgres availability: %w", err)
	}
	defer db.Close()
	key, err := os.ReadFile(o.key)
	if err != nil {
		return fmt.Errorf("read GitHub private key: %w", err)
	}
	app, err := github.New(github.Config{AppID: o.app, PrivateKey: key, Sanitizer: sanitizer})
	if err != nil {
		return err
	}
	for _, setting := range []struct{ flag, env, replacement, value string }{
		{"--github-installation-id", "OPENMERGE_GITHUB_INSTALLATION_ID", "App installation access", strconv.FormatInt(o.installation, 10)},
		{"--repos", "OPENMERGE_REPOS", "App repository access and a default-branch .openmerge.yml", o.repos},
		{"--base", "OPENMERGE_BASE", "queues[].base in the default-branch .openmerge.yml", o.base},
	} {
		if setting.value != "" && setting.value != "0" {
			logger.Warn(setting.flag+" / "+setting.env+" is deprecated; use "+setting.replacement+"; this setting now only narrows discovery", "allowlist", setting.value)
		}
	}
	authorityRuntime, err := productionAuthorityRuntime(o)
	if err != nil {
		return err
	}
	if authorityRuntime != nil {
		authorityRuntime.DatabaseIdentity, err = db.DatabaseIdentity(ctx)
		if err != nil {
			return errors.New("authority database identity unavailable")
		}
	}
	factory := &installationTargets{app: app, options: o, logger: logger, bundles: make(map[string]*installationBundle)}
	// Audits have their own token refresh locks and Git caches. They spend no
	// installation REST/GraphQL quota; credentials are minted with the App JWT.
	// A history fetch must not hold resources needed by landing settlement.
	auditApp, err := github.New(github.Config{AppID: o.app, PrivateKey: key, Sanitizer: sanitizer})
	if err != nil {
		return err
	}
	auditOptions := serveOptions{cache: filepath.Join(o.cache, "outside-push-audit")}
	auditFactory := &installationTargets{app: auditApp, options: auditOptions, logger: logger, bundles: make(map[string]*installationBundle)}
	discovery := serve.Discovery{
		App: strconv.FormatInt(o.app, 10),
		InstallationAllowlist: func() string {
			if o.installation > 0 {
				return strconv.FormatInt(o.installation, 10)
			}
			return ""
		}(),
		RepositoryAllowlist: splitAllowlist(o.repos),
		Target:              factory.target,
		Installations: func(ctx context.Context) ([]serve.Installation, error) {
			listed, err := app.ListInstallations(ctx)
			if err != nil {
				return nil, err
			}
			result := make([]serve.Installation, 0, len(listed))
			for _, item := range listed {
				id := strconv.FormatInt(item.ID, 10)
				result = append(result, serve.Installation{ID: id, Suspended: item.Suspended,
					Repositories: func(ctx context.Context) ([]serve.Repository, error) {
						bundle, err := factory.bundle(ctx, id)
						if err != nil {
							return nil, err
						}
						names, err := bundle.driver.ListRepositories(ctx)
						if err != nil {
							return nil, err
						}
						repos := make([]serve.Repository, 0, len(names))
						for _, name := range names {
							repos = append(repos, serve.Repository{Installation: id, Name: name,
								Branches: func(ctx context.Context) ([]string, error) {
									return bundle.driver.ListBranches(ctx, forge.RepoRef{Path: name})
								},
								Target: func(base string) serve.Target {
									return bundle.target(store.QueueKey{Installation: id, Repository: name, BaseBranch: base})
								},
							})
						}
						return repos, nil
					},
					Budget: func() status.Budget { return factory.budget(id) },
				})
			}
			return result, nil
		},
	}
	service, err := serve.NewDiscovery(ctx, db, discovery, productionServeOptions(o, authorityRuntime, auditFactory.outsidePushHistory, logger))
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErrors := make(chan error, 2)
	for _, endpoint := range []struct {
		name, address string
		handler       http.Handler
	}{{"webhook", o.listen, webhookHandler(service, []byte(o.secret), logger)}, {"status API", o.apiListen, status.DynamicHandler(service, service.Keys, o.apiToken, sanitizer, service.DiscoveryStatus)}} {
		if endpoint.address == "" {
			continue
		}
		listener, err := net.Listen("tcp", endpoint.address)
		if err != nil {
			return err
		}
		server := &http.Server{Handler: endpoint.handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute, BaseContext: func(net.Listener) context.Context { return runCtx }}
		go func() {
			err := server.Serve(listener)
			if !errors.Is(err, http.ErrServerClosed) {
				serveErrors <- err
				cancel()
			}
		}()
		defer func() {
			shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if err := server.Shutdown(shutdown); err != nil {
				logger.Error("HTTP shutdown", "listener", endpoint.name, "error", err)
			}
		}()
		logger.Info("HTTP listening", "listener", endpoint.name, "address", listener.Addr().String())
	}
	logger.Info("OpenMerge serving", "repositories", o.repos, "base", o.base, "poll_interval", o.poll, "webhooks", o.listen != "", "api", o.apiListen != "")
	if err := service.Run(runCtx); err != nil {
		return err
	}
	select {
	case err := <-serveErrors:
		return err
	default:
		return nil
	}
}

type committerDriver interface {
	BotLogin() string
	Committer(context.Context) (gitops.Identity, error)
}

func serveCommitter(ctx context.Context, o serveOptions, logger *slog.Logger, driver committerDriver) gitops.Identity {
	login := driver.BotLogin()
	committer := gitops.Identity{Name: login, Email: login + "@users.noreply.github.com"}
	if o.committerEmail == "" {
		discovered, err := driver.Committer(ctx)
		if err != nil {
			logger.Warn("GitHub App committer discovery failed; using fallback identity. Replace <bot-user-id> with the ID from GET /users/"+login,
				"error", err, "manual_setting", fmt.Sprintf("OPENMERGE_GIT_COMMITTER_NAME=%s OPENMERGE_GIT_COMMITTER_EMAIL=<bot-user-id>+%s@users.noreply.github.com", login, login))
		} else {
			committer = discovered
		}
	}
	if o.committerName != "" {
		committer.Name = o.committerName
	}
	if o.committerEmail != "" {
		committer.Email = o.committerEmail
	}
	return committer
}

type webhookReceiver interface {
	Notify(context.Context, string, string, string) error
	NotifyIgnored(context.Context, string, string, string) error
}

func webhookHandler(service webhookReceiver, secret []byte, logs ...*slog.Logger) http.Handler {
	logger := slog.Default()
	if len(logs) > 0 {
		logger = logs[0]
	}
	var mu sync.Mutex
	var rejectedSignatures uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		event, err := github.VerifyAndParse(r, secret)
		if event.InstallationID > 0 {
			notify := service.Notify
			switch event.Payload.(type) {
			case github.Ignored, nil:
				notify = service.NotifyIgnored
			case github.InstallationChanged:
				if discovery, ok := service.(interface {
					NotifyDiscovery(context.Context, string, string, string) error
				}); ok {
					notify = discovery.NotifyDiscovery
				}
			case github.BranchChanged:
				if branches, ok := service.(interface {
					NotifyBranches(context.Context, string, string, string) error
				}); ok {
					notify = branches.NotifyBranches
				}
			}
			if err != nil {
				notify = service.NotifyIgnored
			}
			if err := notify(r.Context(), strconv.FormatInt(event.InstallationID, 10), event.Repo.Path, event.DeliveryID); err != nil {
				http.Error(w, "delivery persistence failed", http.StatusServiceUnavailable)
				return
			}
		}
		if err != nil {
			var rejected *github.WebhookError
			code := http.StatusBadRequest
			if errors.As(err, &rejected) {
				code = rejected.StatusCode
			}
			if code == http.StatusUnauthorized {
				mu.Lock()
				if rejectedSignatures < 1<<63 {
					rejectedSignatures++
					if rejectedSignatures&(rejectedSignatures-1) == 0 {
						logger.Warn("webhook signature rejections", "count", rejectedSignatures)
					}
				}
				mu.Unlock()
			}
			http.Error(w, err.Error(), code)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
}

func splitAllowlist(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func productionAuthorityRuntime(o serveOptions) (*serve.AuthorityRuntime, error) {
	if o.authorityPlan == "" && o.emissionReceipt == "" {
		return nil, nil
	}
	if o.authorityPlan == "" || o.emissionReceiptMaxAge <= 0 {
		return nil, errors.New("authority opt-in requires plan and positive maximum age")
	}
	var plan struct {
		Targets []serve.AuthorityTarget `json:"targets"`
	}
	if err := readVerificationJSON(o.authorityPlan, &plan); err != nil {
		return nil, errors.New("authority plan unavailable or invalid")
	}
	for _, target := range plan.Targets {
		if target.Input.AppID != strconv.FormatInt(o.app, 10) {
			return nil, errors.New("authority plan App binding mismatch")
		}
	}

	runtime := &serve.AuthorityRuntime{Targets: plan.Targets, ReceiptMaxAge: o.emissionReceiptMaxAge, Receipt: func() (authority.EmissionReceipt, error) {
		var receipt authority.EmissionReceipt
		if err := readVerificationJSON(o.emissionReceipt, &receipt); err != nil {
			return receipt, errors.New("authority emission receipt unavailable or invalid")
		}
		return receipt, nil
	}}
	return runtime, runtime.Validate()
}

func productionServeOptions(o serveOptions, authorityRuntime *serve.AuthorityRuntime, history func(context.Context, store.QueueKey) (forge.OutsidePushHistory, error), logger *slog.Logger) serve.Options {
	return serve.Options{Authority: authorityRuntime, Health: status.HealthSettings{OverdueGrace: o.healthGrace, MaxSuccessAge: o.healthMaxAge, MaxCycleDuration: o.healthMaxCycle, ProgressStallLimit: o.healthProgressStall}, PollInterval: o.poll, BackstopInterval: o.backstop, WebhookHealthWindow: o.webhookHealthWindow, WebhooksConfigured: o.listen != "", RulesInterval: o.rulesInterval, DiscoveryInterval: o.discoveryInterval, BaseAllowlist: splitAllowlist(o.base), Concurrency: o.concurrency, Logger: logger, DisableDeleteHeadBranch: !o.deleteHeadBranch, OutsidePushHistory: history}
}
