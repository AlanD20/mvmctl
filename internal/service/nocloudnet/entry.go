package nocloudnet

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

// Config selects user-owned metadata content and one root-owned managed network.
type Config struct {
	CloudInitDir string
	BaseDir      string
	NetworkID    string
	LogFile      string
	KillAfter    time.Duration
	// NotifyReady uses the sole inherited pipe, FD 3, for the spawning parent's startup handshake.
	NotifyReady bool
}

type startupResult struct {
	Endpoint *network.HostServiceResult `json:"endpoint,omitempty"`
	Error    string                     `json:"error,omitempty"`
}

// Run binds one exclusive gateway listener, grants NoCloud access, and revokes it on shutdown.
// CRITICAL: root authorizes the reserved service port, not a particular listener process.
func Run(ctx context.Context, cfg Config) (returnErr error) {
	var ready *os.File
	readySent := false
	if cfg.NotifyReady {
		ready = os.NewFile(3, "nocloud-ready")
		if ready == nil {
			return errs.New(errs.CodeCloudInitNetModeFailed, "NoCloud readiness pipe is unavailable")
		}
		defer func() {
			if returnErr != nil && !readySent {
				message := returnErr.Error()
				if len(message) > 1024 {
					message = message[:1024]
				}
				returnErr = errors.Join(returnErr, writeStartup(ready, startupResult{Error: message}))
			}
			returnErr = errors.Join(returnErr, ready.Close())
		}()
	}
	if ctx == nil || cfg.NetworkID == "" || cfg.KillAfter < 0 || (cfg.BaseDir == "") == (cfg.CloudInitDir == "") {
		return errs.New(errs.CodeValidationFailed, "NoCloud requires one metadata directory, a managed network and nonnegative lifetime")
	}
	directory := cfg.CloudInitDir
	if cfg.BaseDir != "" {
		directory = cfg.BaseDir
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return errs.WrapMsg(errs.CodeCloudInitNetModeFailed, "NoCloud metadata directory is unavailable", err)
	}
	logFile, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return errs.Wrap(errs.CodeCloudInitNetModeFailed, err)
	}
	defer func() { returnErr = errors.Join(returnErr, logFile.Close()) }()
	logger := slog.New(slog.NewTextHandler(logFile, nil))
	defer func() {
		if returnErr != nil {
			logger.Error("NoCloud server stopped with error", "error", returnErr)
		}
	}()
	runCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if cfg.KillAfter > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, cfg.KillAfter)
		defer cancel()
	}
	client := network.NewHostServiceClient()
	request := network.HostServiceRequest{NetworkID: cfg.NetworkID}
	endpoint, err := client.InspectHostService(runCtx, request)
	if err != nil {
		return err
	}
	address := net.JoinHostPort(endpoint.Gateway, strconv.Itoa(int(endpoint.Port)))
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(runCtx, "tcp4", address)
	if err != nil {
		return errs.WrapMsg(errs.CodeCloudInitNetModeFailed,
			"cannot bind reserved NoCloud gateway port; another batch or service owns it", err)
	}
	// Keep the socket bound after http.Server closes its listener so revocation precedes port reuse.
	reservation, err := listener.(*net.TCPListener).File()
	if err != nil {
		return errors.Join(errs.Wrap(errs.CodeCloudInitNetModeFailed, err), listener.Close())
	}
	defer func() { returnErr = errors.Join(returnErr, reservation.Close()) }()
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	// The durable root generation retains cleanup intent after a crash or unknown apply outcome.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, err := client.RemoveHostService(cleanup, request)
		returnErr = errors.Join(returnErr, err)
	}()
	applied, err := client.ApplyHostService(runCtx, request)
	if err != nil {
		return err
	}
	if applied != endpoint {
		return errs.New(errs.CodeCloudInitNetModeFailed, "root NoCloud gateway changed during listener admission")
	}
	if err := runCtx.Err(); err != nil {
		return errs.Wrap(errs.CodeCloudInitNetModeFailed, err)
	}
	server := &http.Server{Handler: &cloudInitRequestHandler{baseDir: directory, singleDir: cfg.BaseDir == ""},
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	defer func() { returnErr = errors.Join(returnErr, server.Close()) }()
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	if ready != nil {
		if err := writeStartup(ready, startupResult{Endpoint: &endpoint}); err != nil {
			return errors.Join(err, server.Close(), <-finished)
		}
		readySent = true
	}
	logger.Info("NoCloud server ready", "address", address, "network_id", cfg.NetworkID)
	select {
	case err := <-finished:
		if !errors.Is(err, http.ErrServerClosed) {
			return errs.Wrap(errs.CodeCloudInitNetModeFailed, err)
		}
	case <-runCtx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		err := server.Shutdown(shutdown)
		cancel()
		closeErr := server.Close()
		serveErr := <-finished
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(err, closeErr, serveErr)
	}
	return nil
}

func writeStartup(pipe *os.File, result startupResult) error {
	if err := pipe.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	return json.NewEncoder(pipe).Encode(result)
}
