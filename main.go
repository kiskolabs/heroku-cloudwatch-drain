package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sync"

	"github.com/honeybadger-io/honeybadger-go"
	"github.com/kiskolabs/heroku-cloudwatch-drain/logger"
	"github.com/kiskolabs/heroku-cloudwatch-drain/logparser"
)

// App ...
type App struct {
	retention      int
	stripAnsiCodes bool
	user, pass     string
	parse          logparser.ParseFunc

	loggers map[string]logger.Logger
	mu      sync.Mutex // protects loggers
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

	app := &App{
		retention:      retention,
		user:           user,
		pass:           pass,
		stripAnsiCodes: stripAnsiCodes,
		parse:          logparser.Parse,
		loggers:        make(map[string]logger.Logger),
	}

	if honeybadger.Config.APIKey == "" {
		honeybadger.Configure(honeybadger.Configuration{Backend: honeybadger.NewNullBackend()})
	}

	http.Handle("/", honeybadger.Handler(app))

	if err := http.ListenAndServe(bind, nil); err != nil {
		log.Println(err)
	}
}

func (app *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	appName := r.URL.Path[1:]

	if appName == "" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
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

	l, err := app.logger(appName)
	if err != nil {
		log.Printf("failed to create logger for app %s: %s\n", appName, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err = app.processMessages(r.Body, l); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		honeybadger.Notify(err)
		log.Println(err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (app *App) logger(appName string) (l logger.Logger, err error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	l, ok := app.loggers[appName]
	if !ok {
		l, err = logger.NewCloudWatchLogger(appName, app.retention)
		app.loggers[appName] = l
	}
	return l, err
}

func (app *App) processMessages(r io.Reader, l logger.Logger) error {
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
		entry, err := app.parse(b)
		if err != nil {
			honeybadger.Notify(err)
			return fmt.Errorf("unable to parse message: %s, error: %s", string(b), err)
		}
		m := entry.Message
		if app.stripAnsiCodes {
			m = stripAnsi(m)
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
