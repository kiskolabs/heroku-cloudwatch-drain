package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/kiskolabs/heroku-cloudwatch-drain/logger"
	"github.com/kiskolabs/heroku-cloudwatch-drain/logparser"
)

type App struct {
	retention  int
	user, pass string
	parse      logparser.ParseFunc

	loggers map[string]logger.Logger
	mu      sync.Mutex // protects loggers
}

func main() {
	var bind, user, pass string
	var retention int

	flag.StringVar(&bind, "bind", ":8080", "address to bind to")
	flag.IntVar(&retention, "retention", 0, "log retention in days for new log groups")
	flag.StringVar(&user, "user", "", "username for HTTP basic auth")
	flag.StringVar(&pass, "pass", "", "password for HTTP basic auth")
	flag.Parse()

	app := &App{
		retention: retention,
		user:      user,
		pass:      pass,
		parse:     logparser.Parse,
		loggers:   make(map[string]logger.Logger),
	}

	http.Handle("/", app)
	if err := http.ListenAndServe(bind, nil); err != nil {
		log.Println(err)
	}
}

func (app *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("The only accepted request method is POST"))
		return
	}

	appName := r.URL.Path[1:]
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
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		entry, err := app.parse(scanner.Bytes())
		if err != nil {
			return fmt.Errorf("unable to parse message: %s, error: %s", scanner.Text(), err)
		}
		l.Log(entry.Time, entry.Message)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to scan request body: %s", err)
	}
	return nil
}
