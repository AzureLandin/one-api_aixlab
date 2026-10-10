package main

import (
	"context"
	"embed"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/i18n"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/controller"
	"github.com/songquanpeng/one-api/middleware"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/router"
)

//go:embed web/build/*
var buildFS embed.FS

func main() {
	common.Init()
	logger.SetupLogger()
	logger.SysLogf("One API %s started", common.Version)

	if os.Getenv("GIN_MODE") != gin.DebugMode {
		gin.SetMode(gin.ReleaseMode)
	}
	if config.DebugEnabled {
		logger.SysLog("running in debug mode")
	}

	// Initialize SQL Database
	model.InitDB()
	model.InitLogDB()

	var err error
	err = model.CreateRootAccountIfNeed()
	if err != nil {
		logger.FatalLog("database init error: " + err.Error())
	}
	defer func() {
		err := model.CloseDB()
		if err != nil {
			logger.FatalLog("failed to close database: " + err.Error())
		}
	}()

	// Initialize Redis
	err = common.InitRedisClient()
	if err != nil {
		logger.FatalLog("failed to initialize Redis: " + err.Error())
	}

	// Initialize options
	model.InitOptionMap()
	logger.SysLog(fmt.Sprintf("using theme %s", config.Theme))
	if common.RedisEnabled {
		// for compatibility with old versions
		config.MemoryCacheEnabled = true
	}
	if config.MemoryCacheEnabled {
		logger.SysLog("memory cache enabled")
		logger.SysLog(fmt.Sprintf("sync frequency: %d seconds", config.SyncFrequency))
		model.InitChannelCache()
	}
	if config.MemoryCacheEnabled {
		go model.SyncOptions(config.SyncFrequency)
		go model.SyncChannelCache(config.SyncFrequency)
	}
	if os.Getenv("CHANNEL_TEST_FREQUENCY") != "" {
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY"))
		if err != nil {
			logger.FatalLog("failed to parse CHANNEL_TEST_FREQUENCY: " + err.Error())
		}
		go controller.AutomaticallyTestChannels(frequency)
	}
	if os.Getenv("BATCH_UPDATE_ENABLED") == "true" {
		config.BatchUpdateEnabled = true
		logger.SysLog("batch update enabled with interval " + strconv.Itoa(config.BatchUpdateInterval) + "s")
		model.InitBatchUpdater()
	}
	if config.EnableMetric {
		logger.SysLog("metric enabled, will disable channel if too much request failed")
	}
	openai.InitTokenEncoders()
	client.Init()

	// Initialize i18n
	if err := i18n.Init(); err != nil {
		logger.FatalLog("failed to initialize i18n: " + err.Error())
	}

	// Initialize HTTP server
	server := gin.New()
	server.Use(gin.Recovery())
	// This will cause SSE not to work!!!
	//server.Use(gzip.Gzip(gzip.DefaultCompression))
	server.Use(middleware.RequestId())
	server.Use(middleware.Language())
	middleware.SetUpLogger(server)
	// Initialize session store
	store := cookie.NewStore([]byte(config.SessionSecret))
	server.Use(sessions.Sessions("session", store))

	router.SetRouter(server, buildFS)

	// Serve through an explicit http.Server rather than gin's Run so a restart can
	// drain: gin.Run calls http.ListenAndServe, which has no way to stop accepting
	// and wait for what is already in flight.
	httpServer := &http.Server{Handler: server}
	listener, err := listen()
	if err != nil {
		logger.FatalLog("failed to listen: " + err.Error())
	}
	logger.SysLogf("server started on %s", listener.Addr().String())

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			logger.FatalLog("failed to start HTTP server: " + err.Error())
		}
	case sig := <-quit:
		// Stop accepting and let the requests already running finish. This is what
		// keeps a deploy invisible to whoever is mid-answer: the answers here can
		// run for minutes, and killing them mid-stream is exactly what a user
		// notices.
		logger.SysLogf("received %s, draining in-flight requests", sig)
		ctx, cancel := context.WithTimeout(context.Background(), drainTimeout())
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			logger.SysLog("drain did not finish cleanly: " + err.Error())
		}
	}
}

// listen takes the listening socket from systemd when the service was
// socket-activated, and binds the port itself otherwise.
//
// Socket activation is what makes a restart seamless rather than merely quick:
// systemd owns the socket, so it stays open while the old process drains and the
// new one has not started yet. Connections arriving in that window are queued by
// the kernel and served by whoever comes up, instead of being refused.
func listen() (net.Listener, error) {
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) && os.Getenv("LISTEN_FDS") != "" {
		return net.FileListener(os.NewFile(3, "systemd-listener"))
	}
	return net.Listen("tcp", ":"+port())
}

// port is the address to bind when systemd did not hand one over.
func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return strconv.Itoa(*common.Port)
}

// drainTimeout bounds how long a shutdown waits for in-flight requests. The
// gateway serves answers that legitimately run for minutes, so it is generous —
// and systemd's TimeoutStopSec has to be larger still, or a SIGKILL cuts the
// drain short and undoes the point of it.
func drainTimeout() time.Duration {
	if v := os.Getenv("SHUTDOWN_TIMEOUT_SECONDS"); v != "" {
		if seconds, err := strconv.Atoi(v); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return 5 * time.Minute
}
