package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/db"
	"github.com/fangxiusun/ai-adapter/internal/debug"
	"github.com/fangxiusun/ai-adapter/internal/debuglog"
	applog "github.com/fangxiusun/ai-adapter/internal/log"
	"github.com/fangxiusun/ai-adapter/internal/proxy"
	"github.com/fangxiusun/ai-adapter/internal/web"
)

// Set by build ldflags.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	deepDebug := flag.Bool("deep-debug", false, "enable deep debug mode: log each request/response to individual files in ./debug_logs/")
	flag.Parse()

	// Check AI_ADAPTER_DEEP_DEBUG environment variable
	deepDebugEnabled := *deepDebug
	if !deepDebugEnabled {
		if v := os.Getenv("AI_ADAPTER_DEEP_DEBUG"); v == "true" || v == "1" {
			deepDebugEnabled = true
		}
	}
	deepDebugLogger := debuglog.New(deepDebugEnabled)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	logger := applog.New(cfg.Logging.Level, cfg.Logging.File, cfg.Logging.LogRequestBody, cfg.Logging.LogIO)
	defer logger.Close()

	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	channels := channel.NewChannelManager(cfg.Channels, logger)
	proxyHandler := proxy.NewProxyHandler(channels, database, logger, cfg, deepDebugLogger)
	webHandler := web.NewWebHandler(channels, database, cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", proxyHandler.HandleChat)
	mux.HandleFunc("/v1/responses", proxyHandler.HandleResponses)
	mux.HandleFunc("/v1/messages", proxyHandler.HandleMessages)
	mux.HandleFunc("/v1beta/models/", proxyHandler.HandleGenerateContent)
	webHandler.RegisterRoutes(mux)
	debugHandler := debug.NewHandler(channels, cfg)
	debugHandler.RegisterRoutes(mux)

	middleware := chainMiddleware(mux,
		loggingMiddleware(logger),
		corsMiddleware(),
	)

	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      middleware,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		logger.Info("server starting", "addr", server.Addr, "version", version, "commit", commit)
		fmt.Printf("ai-adapter %s (%s)\n", version, commit)
		fmt.Printf("Server listening on http://%s\n", server.Addr)
		fmt.Printf("Admin UI: http://%s/\n", server.Addr)
		if deepDebugLogger.IsEnabled() {
			fmt.Printf("Deep Debug: ENABLED (logs in ./debug_logs/)\n")
		}
		fmt.Printf("API endpoints:\n")
		fmt.Printf("  POST /v1/chat/completions  (OpenAI Chat)\n")
		fmt.Printf("  POST /v1/responses         (OpenAI Responses)\n")
		fmt.Printf("  POST /v1/messages          (Anthropic Claude)\n")
		fmt.Printf("  POST /v1beta/models/{model}:generateContent       (Gemini)\n")
		fmt.Printf("  POST /v1beta/models/{model}:streamGenerateContent  (Gemini Stream)\n")
		fmt.Printf("\nDebug endpoints (dry-run, returns curl commands):\n")
		fmt.Printf("  GET/POST /curl/v1/chat/completions\n")
		fmt.Printf("  GET/POST /curl/v1/responses\n")
		fmt.Printf("  GET/POST /curl/v1/messages\n")
		fmt.Printf("  GET/POST /curl/v1beta/models/{model}:generateContent\n")
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func loggingMiddleware(logger *applog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)
			if r.URL.Path != "/admin/api/health" {
				logger.Debug("http_request",
					"method", r.Method,
					"path", r.URL.Path,
					"latency_ms", time.Since(start).Milliseconds(),
				)
			}
		})
	}
}

func corsMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version")
			if r.Method == "OPTIONS" {
				w.WriteHeader(204)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func chainMiddleware(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}
