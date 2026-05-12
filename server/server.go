package server

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"github.com/glassmonkey/bigquery-emulator/internal/connection"
	"github.com/glassmonkey/bigquery-emulator/internal/contentdata"
	"github.com/glassmonkey/bigquery-emulator/internal/metadata"
	"github.com/gorilla/mux"
)

type Server struct {
	Handler        http.Handler
	storage        Storage
	db             *sql.DB
	loggerConfig   *zap.Config
	logger         *zap.Logger
	connMgr        *connection.Manager
	metaRepo       *metadata.Repository
	contentRepo    *contentdata.Repository
	fileCleanup    func() error
	httpServer     *http.Server
	grpcServer     *grpc.Server
	tracerProvider trace.TracerProvider
	tracer         trace.Tracer
	otelShutdown   func(context.Context) error
}

func New(storage Storage) (*Server, error) {
	server := &Server{storage: storage}
	if storage == TempStorage {
		f, err := os.CreateTemp("", "")
		if err != nil {
			return nil, fmt.Errorf("failed to create temporary file: %w", err)
		}
		storage = Storage(fmt.Sprintf("file:%s?cache=shared", f.Name()))
		server.storage = storage
		server.fileCleanup = func() error {
			return os.Remove(f.Name())
		}
	}
	db, err := sql.Open("zetasqlite", string(storage))
	if err != nil {
		return nil, err
	}
	server.db = db
	server.loggerConfig = &zap.Config{
		Level:             zap.NewAtomicLevelAt(zap.ErrorLevel),
		Development:       false,
		Encoding:          "console",
		DisableStacktrace: true,
		EncoderConfig:     zap.NewDevelopmentEncoderConfig(),
		OutputPaths:       []string{"stderr"},
		ErrorOutputPaths:  []string{"stderr"},
	}
	if _, err := server.loggerConfig.Build(); err != nil {
		return nil, fmt.Errorf("invalid default logger config: %w", err)
	}
	server.logger = zap.NewNop()
	server.tracerProvider = tracenoop.NewTracerProvider()
	server.tracer = server.tracerProvider.Tracer(tracerScope)
	metaRepo, err := metadata.NewRepository(db)
	if err != nil {
		return nil, err
	}
	server.connMgr = connection.NewManager(db)
	server.metaRepo = metaRepo
	server.contentRepo = contentdata.NewRepository(db)

	r := mux.NewRouter()
	for _, handler := range handlers {
		r.Handle(handler.Path, handler.Handler).Methods(handler.HTTPMethod)
		r.Handle(fmt.Sprintf("/bigquery/v2%s", handler.Path), handler.Handler).Methods(handler.HTTPMethod)
	}
	r.Handle(discoveryAPIEndpoint, newDiscoveryHandler(server)).Methods("GET")
	r.Handle(newDiscoveryAPIEndpoint, newDiscoveryHandler(server)).Methods("GET")
	r.Handle(uploadAPIEndpoint, &uploadHandler{}).Methods("POST")
	r.Handle(uploadAPIEndpoint, &uploadContentHandler{}).Methods("PUT")
	r.PathPrefix("/").Handler(&defaultHandler{})
	r.Use(tracingMiddleware(server))
	r.Use(sequentialAccessMiddleware())
	r.Use(recoveryMiddleware(server))
	r.Use(loggerMiddleware(server))
	r.Use(accessLogMiddleware())
	r.Use(decompressMiddleware())
	r.Use(withServerMiddleware(server))
	r.Use(withProjectMiddleware())
	r.Use(withDatasetMiddleware())
	r.Use(withJobMiddleware())
	r.Use(withTableMiddleware())
	r.Use(withModelMiddleware())
	r.Use(withRoutineMiddleware())
	server.Handler = r
	return server, nil
}

func (s *Server) Close() error {
	defer func() {
		if s.fileCleanup != nil {
			if err := s.fileCleanup(); err != nil {
				log.Printf("failed to cleanup file: %s", err.Error())
			}
		}
	}()
	if s.otelShutdown != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.otelShutdown(ctx); err != nil {
			log.Printf("failed to shutdown otel tracer provider: %s", err.Error())
		}
		cancel()
	}
	if err := s.db.Close(); err != nil {
		log.Printf("failed to close database: %s", err.Error())
		return err
	}
	return nil
}

// tracerScope is the instrumentation name used for span sources
// created from the emulator itself. Distinct from any scope a
// user (or a downstream package like zetasqlite) may pick.
const tracerScope = "github.com/glassmonkey/bigquery-emulator/server"

// SetOTel wires up an OTLP gRPC trace exporter pointed at
// `endpoint` (e.g. "otel-collector:4317") and replaces the
// no-op tracer with one backed by the real provider. Existing
// HTTP middleware picks the new tracer up through s.tracer.
//
// Idempotent: a second call shuts down the previous provider
// before installing the new one. Pass an empty endpoint to
// revert to a no-op tracer (useful for tests that opt out
// after setup).
func (s *Server) SetOTel(ctx context.Context, endpoint string) error {
	if s.otelShutdown != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = s.otelShutdown(shutdownCtx)
		cancel()
		s.otelShutdown = nil
	}
	if endpoint == "" {
		s.tracerProvider = tracenoop.NewTracerProvider()
		s.tracer = s.tracerProvider.Tracer(tracerScope)
		return nil
	}
	exporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return fmt.Errorf("otlp trace exporter: %w", err)
	}
	// Pin service.name so spans land under a recognisable name in
	// downstream tools instead of "unknown_service:bigquery-emulator".
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName("bigquery-emulator"),
			attribute.String("service.namespace", "bigquery-emulator"),
		),
	)
	if err != nil {
		return fmt.Errorf("otlp resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(res),
	)
	s.tracerProvider = tp
	s.tracer = tp.Tracer(tracerScope)
	s.otelShutdown = tp.Shutdown
	return nil
}

func (s *Server) SetProject(id string) error {
	ctx := context.Background()
	conn, err := s.connMgr.Connection(ctx, id, "")
	if err != nil {
		return err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	if err := tx.MetadataRepoMode(); err != nil {
		return err
	}
	if err := s.metaRepo.AddProjectIfNotExists(
		ctx,
		tx.Tx(),
		metadata.NewProject(s.metaRepo, id, nil, nil),
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

type LogLevel string

const (
	LogLevelUnknown LogLevel = "unknown"
	LogLevelDebug   LogLevel = "debug"
	LogLevelInfo    LogLevel = "info"
	LogLevelWarn    LogLevel = "warn"
	LogLevelError   LogLevel = "error"
	LogLevelFatal   LogLevel = "fatal"
)

func (s *Server) SetLogLevel(level LogLevel) error {
	var atomicLevel zap.AtomicLevel
	switch level {
	case LogLevelDebug:
		atomicLevel = zap.NewAtomicLevelAt(zap.DebugLevel)
	case LogLevelInfo:
		atomicLevel = zap.NewAtomicLevelAt(zap.InfoLevel)
	case LogLevelWarn:
		atomicLevel = zap.NewAtomicLevelAt(zap.WarnLevel)
	case LogLevelError:
		atomicLevel = zap.NewAtomicLevelAt(zap.ErrorLevel)
	case LogLevelFatal:
		atomicLevel = zap.NewAtomicLevelAt(zap.FatalLevel)
	default:
		return fmt.Errorf("unexpected log level %s", level)
	}
	s.loggerConfig.Level = atomicLevel
	logger, err := s.loggerConfig.Build()
	if err != nil {
		return err
	}
	s.logger = logger
	return nil
}

type LogFormat string

const (
	LogFormatConsole LogFormat = "console"
	LogFormatJSON    LogFormat = "json"
)

func (s *Server) SetLogFormat(format LogFormat) error {
	switch format {
	case LogFormatConsole:
		s.loggerConfig.Encoding = "console"
	case LogFormatJSON:
		s.loggerConfig.Encoding = "json"
	default:
		return fmt.Errorf("unexpected log format %s", format)
	}
	logger, err := s.loggerConfig.Build()
	if err != nil {
		return err
	}
	s.logger = logger
	return nil
}

func (s *Server) Load(sources ...Source) error {
	for _, source := range sources {
		if err := source(s); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) Serve(ctx context.Context, httpAddr, grpcAddr string) error {
	httpServer := &http.Server{
		Handler:      s.Handler,
		Addr:         httpAddr,
		WriteTimeout: 5 * time.Minute,
		ReadTimeout:  15 * time.Second,
	}
	s.httpServer = httpServer

	grpcServer := grpc.NewServer()
	registerStorageServer(grpcServer, s)
	s.grpcServer = grpcServer

	httpListener, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return err
	}
	grpcListener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return err
	}

	var eg errgroup.Group
	eg.Go(func() error { return grpcServer.Serve(grpcListener) })
	eg.Go(func() error { return httpServer.Serve(httpListener) })
	return eg.Wait()
}

func (s *Server) Stop(ctx context.Context) error {
	defer s.Close()

	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}
