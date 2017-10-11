package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"gopkg.in/tylerb/graceful.v1"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
	"github.com/honeybadger-io/honeybadger-go"
	"github.com/jcxplorer/cwlogger"
	"github.com/kiskolabs/heroku-cloudwatch-drain/logparser"
	"github.com/newrelic/go-agent"
)

// App is a Heroku HTTPS log drain. It receives log batches as POST requests,
// parses them, and sends them to CloudWatch Logs.
type App struct {
	retention      int
	stripAnsiCodes bool
	user, pass     string
	parse          logparser.ParseFunc
	newrelic       newrelic.Application

	loggers map[string]logger
	mu      sync.Mutex // protects loggers
}

type logger interface {
	Log(t time.Time, s string)
	Close()
}

func main() {
	var bind, user, pass string
	var retention int
	var stripAnsiCodes bool

	flag.StringVar(&bind, "bind", ":8080", "address to bind to")
	flag.IntVar(&retention, "retention", 0, "log retention in days for new log groups")
	flag.StringVar(&user, "user", "", "username for HTTP basic auth")
	flag.StringVar(&pass, "pass", "", "password for HTTP basic auth")
	flag.BoolVar(&stripAnsiCodes, "strip-ansi-codes", false, "strip ANSI codes from log messages")
	flag.Parse()

	nrAppName := os.Getenv("NEW_RELIC_APP_NAME")
	if nrAppName == "" {
		nrAppName = "heroku-cloudwatch-drain"
	}

	nrLicense := os.Getenv("NEW_RELIC_LICENSE_KEY")
	nrConfig := newrelic.NewConfig(nrAppName, nrLicense)
	nrConfig.Enabled = (nrLicense != "")

	nrApp, err := newrelic.NewApplication(nrConfig)
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	app := &App{
		retention:      retention,
		user:           user,
		pass:           pass,
		stripAnsiCodes: stripAnsiCodes,
		parse:          logparser.Parse,
		loggers:        make(map[string]logger),
		newrelic:       nrApp,
	}

	if honeybadger.Config.APIKey == "" {
		honeybadger.Configure(honeybadger.Configuration{Backend: honeybadger.NewNullBackend()})
	}

	honeybadger.BeforeNotify(
		func(notice *honeybadger.Notice) error {
			if notice.ErrorClass == "errors.errorString" {
				notice.Fingerprint = notice.ErrorMessage
			}
			return nil
		},
	)

	mux := http.NewServeMux()
	mux.Handle(newrelic.WrapHandle(nrApp, "/", honeybadger.Handler(app)))
	err = graceful.RunWithErr(bind, 5*time.Second, mux)
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	app.Stop()
}

func (app *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	appName := r.URL.Path[1:]

	// honeybadger.SetContext(honeybadger.Context{
	// 	"AppName": appName,
	// })

	if r.Method == http.MethodGet {
		if appName == "" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		} else {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("Not found"))
		}
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("The only accepted request method is POST"))
		return
	}

	if appName == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Request path must specify the log group name"))
		return
	}

	user, pass, _ := r.BasicAuth()
	if user != app.user || pass != app.pass {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	txn, _ := w.(newrelic.Transaction)
	if txn != nil {
		if err := txn.AddAttribute("AppName", appName); nil != err {
			log.Printf("failed to add New Relic attribute for app %s: %s\n", appName, err)
		}
	}

	l, err := app.logger(appName)
	if err != nil {
		log.Printf("failed to create logger for app %s: %s\n", appName, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err = app.processMessages(r.Body, l, txn); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		honeybadger.Notify(err)
		log.Println(err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// Stop all the loggers, flushing any pending requests.
func (app *App) Stop() {
	var wg sync.WaitGroup
	wg.Add(len(app.loggers))
	app.mu.Lock()
	defer app.mu.Unlock()
	for _, l := range app.loggers {
		go func(l logger) {
			l.Close()
			wg.Done()
		}(l)
	}
	wg.Wait()
}

func (app *App) logger(appName string) (l logger, err error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	l, ok := app.loggers[appName]
	if !ok {
		l, err = cwlogger.New(&cwlogger.Config{
			LogGroupName: appName,
			Retention:    app.retention,
			Client:       cloudwatchlogs.New(session.New()),
			ErrorReporter: func(err error) {
				honeybadger.Notify(err)
			},
		})
		app.loggers[appName] = l
	}
	return l, err
}

func (app *App) processMessages(r io.Reader, l logger, txn newrelic.Transaction) error {
	if txn != nil {
		defer newrelic.StartSegment(txn, "processMessages").End()
	}
	buf := bufio.NewReader(r)
	eof := false
	for {
		b, err := buf.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				eof = true
			} else {
				honeybadger.Notify(err)
				return fmt.Errorf("failed to scan request body: %s", err)
			}
		}
		if eof && len(b) == 0 {
			break
		}
		entry, err := app.parse(b)
		if err != nil {
			honeybadger.Notify(err)
			return fmt.Errorf("unable to parse message: %s, error: %s", string(b), err)
		}
		m := entry.Message
		if app.stripAnsiCodes {
			m = stripAnsi(m)
		}
		if !eof {
			m = m[:len(m)-1]
		}
		l.Log(entry.Time, m)
		if eof {
			break
		}
	}
	return nil
}

var ansiRegexp = regexp.MustCompile("\x1b[^m]*m")

func stripAnsi(s string) string {
	return ansiRegexp.ReplaceAllLiteralString(s, "")
}
