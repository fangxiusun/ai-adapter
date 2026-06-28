package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/db"
	"github.com/fangxiusun/ai-adapter/internal/debug"
	"github.com/fangxiusun/ai-adapter/internal/debuglog"
	"github.com/fangxiusun/ai-adapter/internal/headerpolicy"
	applog "github.com/fangxiusun/ai-adapter/internal/log"
	"github.com/fangxiusun/ai-adapter/internal/metrics"
	"github.com/fangxiusun/ai-adapter/internal/proxy"
	"github.com/fangxiusun/ai-adapter/internal/stats"
	"github.com/fangxiusun/ai-adapter/internal/web"
	"github.com/fangxiusun/ai-adapter/internal/websocket"
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

	// Initialize WebSocket Hub

	wsHub := websocket.NewHub()
	go wsHub.Run()

	// Start heartbeat with active request count
	wsHub.StartHeartbeat(func() int {
		return int(metrics.GetActiveRequests())
	})

	// Initialize Stats

	statsInstance := stats.NewStats()

	// Start periodic metrics broadcaster (every 5 seconds)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			wsHub.Broadcast("metrics", map[string]interface{}{
				"qps":            statsInstance.GetCurrentQPS(),
				"avg_latency_ms": statsInstance.GetAvgLatencyMs(),
				"error_rate":     statsInstance.GetErrorRate(),
			})
		}
	}()

	channels := channel.NewChannelManager(cfg.Channels, cfg.Proxies, logger, database, cfg.Failover.LoadBalance)
	headerEngine := headerpolicy.NewEngine(cfg)
	proxyHandler := proxy.NewProxyHandler(channels, database, logger, cfg, deepDebugLogger, headerEngine, statsInstance, wsHub)
	webHandler := web.NewWebHandler(channels, database, cfg, statsInstance, wsHub)

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
		authMiddleware(cfg.Server.APIToken),
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
	channels.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func loggingMiddleware(logger *applog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			model := extractModelForLog(r)
			next.ServeHTTP(w, r)
			if r.URL.Path != "/admin/api/health" {
				logger.Debug("http_request",
					"method", r.Method,
					"path", r.URL.Path,
					"model", model,
					"latency_ms", time.Since(start).Milliseconds(),
				)
			}
		})
	}
}

func extractModelForLog(r *http.Request) string {
	// 1. Gemini: 从 URL 拿
	if strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
		prefix := "/v1beta/models/"
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		parts := strings.SplitN(rest, ":", 2)
		return parts[0]
	}

	// 2. 自定义 header（如果客户端愿意传）
	if model := r.Header.Get("X-Model-Name"); model != "" {
		return model
	}

	// 3. POST JSON body 里拿
	if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/") {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return ""
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		var partial struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &partial); err != nil {
			return ""
		}
		return partial.Model
	}
	return ""
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

func authMiddleware(apiToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiToken == "" {
				next.ServeHTTP(w, r)
				return
			}
			path := r.URL.Path
			if !strings.HasPrefix(path, "/v1/") && !strings.HasPrefix(path, "/v1beta/") {
				next.ServeHTTP(w, r)
				return
			}
			auth := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if len(auth) > len(prefix) && auth[:len(prefix)] == prefix {
				if auth[len(prefix):] == apiToken {
					next.ServeHTTP(w, r)
					return
				}
			}
			if r.Header.Get("x-api-key") == apiToken {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"type":"authentication_error","code":"unauthorized","message":"invalid or missing api token"}}`)
		})
	}
}
func chainMiddleware(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}
