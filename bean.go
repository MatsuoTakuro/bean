// MIT License

// Copyright (c) The RAI Authors

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:

// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
package bean

import (
	"crypto/tls"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dgraph-io/badger/v3"
	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
	validatorV10 "github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/labstack/echo-contrib/prometheus"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
	"github.com/labstack/gommon/log"
	"github.com/retail-ai-inc/bean/binder"
	"github.com/retail-ai-inc/bean/dbdrivers"
	"github.com/retail-ai-inc/bean/echoview"

	berror "github.com/retail-ai-inc/bean/error"
	"github.com/retail-ai-inc/bean/goview"
	"github.com/retail-ai-inc/bean/helpers"
	"github.com/retail-ai-inc/bean/middleware"
	str "github.com/retail-ai-inc/bean/string"
	"github.com/retail-ai-inc/bean/validator"
	"go.mongodb.org/mongo-driver/mongo"
	"gorm.io/gorm"
)

// All database connections are initialized using `DBDeps` structure.
type DBDeps struct {
	MasterMySQLDB      *gorm.DB
	MasterMySQLDBName  string
	TenantMySQLDBs     map[uint64]*gorm.DB
	TenantMySQLDBNames map[uint64]string
	MasterMongoDB      *mongo.Client
	MasterMongoDBName  string
	TenantMongoDBs     map[uint64]*mongo.Client
	TenantMongoDBNames map[uint64]string
	MasterRedisDB      map[uint64]*dbdrivers.RedisDBConn
	TenantRedisDBs     map[uint64]*dbdrivers.RedisDBConn
	BadgerDB           *badger.DB
}

type Bean struct {
	DBConn            *DBDeps
	Echo              *echo.Echo
	BeforeServe       func()
	errorHandlerFuncs []berror.ErrorHandlerFunc
	validate          *validatorV10.Validate
	Config            Config
}

type SentryConfig struct {
	On                  bool
	Debug               bool
	Dsn                 string
	Timeout             time.Duration
	TracesSampleRate    float64
	SkipTracesEndpoints []string
	ClientOptions       *sentry.ClientOptions
	ConfigureScope      func(scope *sentry.Scope)
}

type Config struct {
	ProjectName  string
	Environment  string
	DebugLogPath string
	Secret       string
	AccessLog    struct {
		On                bool
		BodyDump          bool
		Path              string
		BodyDumpMaskParam []string
	}
	Prometheus struct {
		On            bool
		SkipEndpoints []string
	}
	HTTP struct {
		Port            string
		Host            string
		BodyLimit       string
		IsHttpsRedirect bool
		Timeout         time.Duration
		KeepAlive       bool
		AllowedMethod   []string
		SSL             struct {
			On            bool
			CertFile      string
			PrivFile      string
			MinTLSVersion uint16
		}
	}
	HTML struct {
		ViewsTemplateCache bool
	}
	Database struct {
		Tenant struct {
			On bool
		}
		MySQL  dbdrivers.SQLConfig
		Mongo  dbdrivers.MongoConfig
		Redis  dbdrivers.RedisConfig
		Badger dbdrivers.BadgerConfig
	}
	Sentry   SentryConfig
	Security struct {
		HTTP struct {
			Header struct {
				XssProtection         string
				ContentTypeNosniff    string
				XFrameOptions         string
				HstsMaxAge            int
				ContentSecurityPolicy string
			}
		}
	}
}

// This is a global variable to hold the debug logger so that we can log data from service, repository or anywhere.
var BeanLogger echo.Logger

// This is a global variable to hold the sentry On/Off setting to check sentry is initialized or not very quickly
// from handlers/services/repositories.
var SentryOn bool

// This key is inherited from `sentryecho` package as the package doesn't support the key for external use.
const SentryHubContextKey = "sentry"

// If a command or service wants to use a different `host` parameter for tenant database connection
// then it's easy to do just by passing that parameter string name using `bean.TenantAlterDbHostParam`.
// Therfore, `bean` will overwrite all host string in `TenantConnections`.`Connections` JSON.
var TenantAlterDbHostParam string

func New(config Config) (b *Bean) {
	// Parse bean system files and directories.
	helpers.ParseBeanSystemFilesAndDirectorires()

	// Create a new echo instance
	e := NewEcho(config)

	b = &Bean{
		Echo:     e,
		validate: validatorV10.New(),
		Config:   config,
	}

	return b
}

func NewEcho(config Config) *echo.Echo {

	e := echo.New()

	// Hide default `Echo` banner during startup.
	e.HideBanner = true

	// Set custom request binder
	e.Binder = &binder.CustomBinder{}

	// Setup HTML view templating engine.
	viewsTemplateCache := config.HTML.ViewsTemplateCache
	e.Renderer = echoview.New(goview.Config{
		Root:         "views",
		Extension:    ".html",
		Master:       "templates/master",
		Partials:     []string{},
		Funcs:        make(template.FuncMap),
		DisableCache: !viewsTemplateCache,
		Delims:       goview.Delims{Left: "{{", Right: "}}"},
	})

	// IMPORTANT: Configure debug log.
	if config.DebugLogPath != "" {
		if file, err := openFile(config.DebugLogPath); err != nil {
			e.Logger.Fatalf("Unable to open log file: %v Server 🚀  crash landed. Exiting...\n", err)
		} else {
			e.Logger.SetOutput(file)
		}
	}
	e.Logger.SetLevel(log.DEBUG)

	// Initialize `BeanLogger` global variable using `e.Logger`.
	BeanLogger = e.Logger

	// IMPORTANT: Configure access log and body dumper. (can be turn off)
	if config.AccessLog.On {
		accessLogConfig := middleware.LoggerConfig{BodyDump: config.AccessLog.BodyDump}
		if config.AccessLog.Path != "" {
			if file, err := openFile(config.AccessLog.Path); err != nil {
				e.Logger.Fatalf("Unable to open log file: %v Server 🚀  crash landed. Exiting...\n", err)
			} else {
				accessLogConfig.Output = file
			}
			if len(config.AccessLog.BodyDumpMaskParam) > 0 {
				accessLogConfig.MaskedParameters = config.AccessLog.BodyDumpMaskParam
			}
		}
		accessLogger := middleware.AccessLoggerWithConfig(accessLogConfig)
		e.Use(accessLogger)
	}

	// IMPORTANT: Capturing error and send to sentry if needed.
	// Sentry `panic` error handler and APM initialization if activated from `env.json`
	if config.Sentry.On {
		// Check the sentry client options is not nil
		if config.Sentry.ClientOptions == nil {
			e.Logger.Fatal("Sentry initialization failed: client options is empty")
		}

		if err := sentry.Init(*config.Sentry.ClientOptions); err != nil {
			e.Logger.Fatal("Sentry initialization failed: ", err, ". Server 🚀  crash landed. Exiting...")
		}

		// Configure custom scope
		if config.Sentry.ConfigureScope != nil {
			sentry.ConfigureScope(config.Sentry.ConfigureScope)
		}

		e.Use(sentryecho.New(sentryecho.Options{
			Repanic: true,
			Timeout: config.Sentry.Timeout,
		}))

		if helpers.FloatInRange(config.Sentry.TracesSampleRate, 0.0, 1.0) > 0.0 {
			e.Pre(middleware.Tracer())
		}

		// Initialize `SentryOn` global variable so that we can check the sentry is On/Off from handlers/services/repository.
		SentryOn = true
	}

	// Some pre-build middleware initialization.
	e.Pre(echomiddleware.RemoveTrailingSlash())
	if config.HTTP.IsHttpsRedirect {
		e.Pre(echomiddleware.HTTPSRedirect())
	}
	e.Use(echomiddleware.Recover())

	// IMPORTANT: Request related middleware.
	// Set the `X-Request-ID` header field if it doesn't exist.
	e.Use(echomiddleware.RequestIDWithConfig(echomiddleware.RequestIDConfig{
		Generator: uuid.NewString,
	}))

	// Adds a `Server` header to the response.
	e.Use(middleware.ServerHeader(config.ProjectName, helpers.CurrVersion()))

	// Sets the maximum allowed size for a request body, return `413 - Request Entity Too Large` if the size exceeds the limit.
	e.Use(echomiddleware.BodyLimit(config.HTTP.BodyLimit))

	// CORS initialization and support only HTTP methods which are configured under `http.allowedMethod` parameters in `env.json`.
	e.Use(echomiddleware.CORSWithConfig(echomiddleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: config.HTTP.AllowedMethod,
	}))

	// Basic HTTP headers security like XSS protection...
	e.Use(echomiddleware.SecureWithConfig(echomiddleware.SecureConfig{
		XSSProtection:         config.Security.HTTP.Header.XssProtection,         // Adds the X-XSS-Protection header with the value `1; mode=block`.
		ContentTypeNosniff:    config.Security.HTTP.Header.ContentTypeNosniff,    // Adds the X-Content-Type-Options header with the value `nosniff`.
		XFrameOptions:         config.Security.HTTP.Header.XFrameOptions,         // The X-Frame-Options header value to be set with a custom value.
		HSTSMaxAge:            config.Security.HTTP.Header.HstsMaxAge,            // HSTS header is only included when the connection is HTTPS.
		ContentSecurityPolicy: config.Security.HTTP.Header.ContentSecurityPolicy, // Allows the Content-Security-Policy header value to be set with a custom value.
	}))

	// Enable prometheus metrics middleware. Metrics data should be accessed via `/metrics` endpoint.
	// This will help us to integrate `bean's` health into `k8s`.
	if config.Prometheus.On {
		p := prometheus.NewPrometheus("echo", prometheusUrlSkipper(config.Prometheus.SkipEndpoints))
		p.Use(e)
	}

	return e
}

func (b *Bean) ServeAt(host, port string) {
	b.Echo.Logger.Info("Starting " + b.Config.Environment + " " + b.Config.ProjectName + " at " + host + ":" + port + "...🚀")

	b.UseErrorHandlerFuncs(berror.DefaultErrorHanderFunc)
	b.Echo.HTTPErrorHandler = b.DefaultHTTPErrorHandler()

	b.Echo.Validator = &validator.DefaultValidator{Validator: b.validate}

	s := http.Server{
		Addr:    host + ":" + port,
		Handler: b.Echo,
	}

	// IMPORTANT: Keep-alive is default true but I kept this here to let you guys no that there is a settings
	// for it :)
	s.SetKeepAlivesEnabled(b.Config.HTTP.KeepAlive)

	// before bean bootstrap
	if b.BeforeServe != nil {
		b.BeforeServe()
	}

	// Start the server
	if b.Config.HTTP.SSL.On {
		s.TLSConfig = &tls.Config{
			MinVersion: b.Config.HTTP.SSL.MinTLSVersion,
		}

		if err := s.ListenAndServeTLS(b.Config.HTTP.SSL.CertFile, b.Config.HTTP.SSL.PrivFile); err != nil && err != http.ErrServerClosed {
			b.Echo.Logger.Fatal(err)
		}

	} else {
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			b.Echo.Logger.Fatal(err)
		}
	}
}

func (b *Bean) UseMiddlewares(middlewares ...echo.MiddlewareFunc) {
	b.Echo.Use(middlewares...)
}

func (b *Bean) UseErrorHandlerFuncs(errHdlrFuncs ...berror.ErrorHandlerFunc) {
	if b.errorHandlerFuncs == nil {
		b.errorHandlerFuncs = []berror.ErrorHandlerFunc{}
	}
	b.errorHandlerFuncs = append(b.errorHandlerFuncs, errHdlrFuncs...)
}

func (b *Bean) UseValidation(validateFuncs ...validator.ValidatorFunc) {
	for _, validateFunc := range validateFuncs {
		if err := validateFunc(b.validate); err != nil {
			b.Echo.Logger.Error(err)
		}
	}
}

func (b *Bean) DefaultHTTPErrorHandler() echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {

		if c.Response().Committed {
			return
		}

		for _, handle := range b.errorHandlerFuncs {
			handled, err := handle(err, c)
			if err != nil {
				if SentryOn {
					SentryCaptureException(c, err)
				} else {
					c.Logger().Error(err)
				}
			}
			if handled {
				break
			}
		}
	}
}

// InitDB initialize all the database dependencies and store it in global variable `global.DBConn`.
func (b *Bean) InitDB() {
	var masterMySQLDB *gorm.DB
	var masterMySQLDBName string
	var masterMongoDB *mongo.Client
	var masterMongoDBName string
	var masterRedisDB map[uint64]*dbdrivers.RedisDBConn
	var tenantMySQLDBs map[uint64]*gorm.DB
	var tenantMySQLDBNames map[uint64]string
	var tenantMongoDBs map[uint64]*mongo.Client
	var tenantMongoDBNames map[uint64]string
	var tenantRedisDBs map[uint64]*dbdrivers.RedisDBConn

	if b.Config.Database.Tenant.On {
		masterMySQLDB, masterMySQLDBName = dbdrivers.InitMysqlMasterConn(b.Config.Database.MySQL)
		tenantMySQLDBs, tenantMySQLDBNames = dbdrivers.InitMysqlTenantConns(b.Config.Database.MySQL, masterMySQLDB, TenantAlterDbHostParam, b.Config.Secret)
		tenantMongoDBs, tenantMongoDBNames = dbdrivers.InitMongoTenantConns(b.Config.Database.Mongo, masterMySQLDB, TenantAlterDbHostParam, b.Config.Secret)
		masterRedisDB = dbdrivers.InitRedisMasterConn(b.Config.Database.Redis)
		tenantRedisDBs = dbdrivers.InitRedisTenantConns(b.Config.Database.Redis, masterMySQLDB, TenantAlterDbHostParam, b.Config.Secret)
	} else {
		masterMySQLDB, masterMySQLDBName = dbdrivers.InitMysqlMasterConn(b.Config.Database.MySQL)
		masterMongoDB, masterMongoDBName = dbdrivers.InitMongoMasterConn(b.Config.Database.Mongo)
		masterRedisDB = dbdrivers.InitRedisMasterConn(b.Config.Database.Redis)
	}

	masterBadgerDB := dbdrivers.InitBadgerConn(b.Config.Database.Badger)

	b.DBConn = &DBDeps{
		MasterMySQLDB:      masterMySQLDB,
		MasterMySQLDBName:  masterMySQLDBName,
		TenantMySQLDBs:     tenantMySQLDBs,
		TenantMySQLDBNames: tenantMySQLDBNames,
		MasterMongoDB:      masterMongoDB,
		MasterMongoDBName:  masterMongoDBName,
		TenantMongoDBs:     tenantMongoDBs,
		TenantMongoDBNames: tenantMongoDBNames,
		MasterRedisDB:      masterRedisDB,
		TenantRedisDBs:     tenantRedisDBs,
		BadgerDB:           masterBadgerDB,
	}
}

// The bean Logger to have debug log from anywhere.
func Logger() echo.Logger {
	return BeanLogger
}

// This is a global function to send sentry exception if you configure the sentry through env.json. You cann pass a proper context or nil.
func SentryCaptureException(c echo.Context, err error) {
	if !SentryOn {
		return
	}

	if c != nil {
		// If the function get a proper context then push the request headers and URI along with other meaningful info.
		if hub := sentryecho.GetHubFromContext(c); hub != nil {
			hub.CaptureException(err)
		}

		return
	}

	// If someone call the function from service/repository without a proper context.
	sentry.CurrentHub().Clone().CaptureException(err)
}

// This is a global function to send sentry message if you configure the sentry through env.json. You cann pass a proper context or nil.
func SentryCaptureMessage(c echo.Context, msg string) {
	if !SentryOn {
		return
	}

	if c != nil {
		// If the function get a proper context then push the request headers and URI along with other meaningful info.
		if hub := sentryecho.GetHubFromContext(c); hub != nil {
			hub.CaptureMessage(msg)
		}

		return
	}

	// If someone call the function from service/repository without a proper context.
	sentry.CurrentHub().Clone().CaptureMessage(msg)
}

// Modify event through beforeSend function.
func DefaultBeforeSend(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
	// Example: enriching the event by adding aditional data.
	switch err := hint.OriginalException.(type) {
	case *validator.ValidationError:
		return event
	case *berror.APIError:
		if err.Ignorable {
			return nil
		}
		event.Contexts["Error"] = map[string]interface{}{
			"HTTPStatusCode": err.HTTPStatusCode,
			"GlobalErrCode":  err.GlobalErrCode,
			"Message":        err.Error(),
		}
		return event
	case *echo.HTTPError:
		return event
	default:
		return event
	}
}

// Modify breadcrumbs through beforeBreadcrumb function.
func DefaultBeforeBreadcrumb(breadcrumb *sentry.Breadcrumb, hint *sentry.BreadcrumbHint) *sentry.Breadcrumb {
	// Example: discard the breadcrumb by return nil.
	// if breadcrumb.Category == "example" {
	// 	return nil
	// }
	return breadcrumb
}

// `prometheusUrlSkipper` ignores metrics route on some endpoints.
func prometheusUrlSkipper(skip []string) func(c echo.Context) bool {
	return func(c echo.Context) bool {
		_, matches := str.MatchAllSubstringsInAString(c.Path(), skip...)
		return matches > 0
	}
}

// openFile opens and return the file, if doesn't exist, create it, or append to the file with the directory.
func openFile(path string) (*os.File, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(path), 0764); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0664)
}
