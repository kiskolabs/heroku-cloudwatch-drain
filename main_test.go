package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/kiskolabs/heroku-cloudwatch-drain/logger"
	"github.com/kiskolabs/heroku-cloudwatch-drain/logparser"

	"github.com/stretchr/testify/assert"
)

var l = new(LastMessageLogger)
var parseFunc = func(b []byte) (*logparser.LogEntry, error) {
	return &logparser.LogEntry{Time: time.Now(), Message: ""}, nil
}

var app = &App{
	loggers: map[string]logger.Logger{"app": l},
	parse:   parseFunc,
}
var server = httptest.NewServer(app)

func TestRequestMustNotBeGet(t *testing.T) {
	r, err := http.Get(server.URL + "/app")
	assert.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, r.StatusCode)
}

func TestRequestPathMustBeAppName(t *testing.T) {
	r, err := http.Post(server.URL+"/", "", nil)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, r.StatusCode)
}

func TestBasicAuth(t *testing.T) {
	app.user = "me"
	app.pass = "SECRET"
	defer func() {
		app.user = ""
		app.pass = ""
	}()

	r, err := http.Post(server.URL+"/app", "", nil)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, r.StatusCode)

	uri, _ := url.Parse(server.URL)
	uri.User = url.UserPassword("me", "SECRET")

	r, err = http.Post(uri.String()+"/app", "", nil)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, r.StatusCode)
}

func TestNoBasicAuth(t *testing.T) {
	r, err := http.Post(server.URL+"/app", "", nil)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, r.StatusCode)
}

func TestSingleLogEntry(t *testing.T) {
	app.parse = logparser.Parse
	defer func() {
		app.parse = parseFunc
	}()

	body := bytes.NewBuffer([]byte(`89 <45>1 2016-10-15T08:59:08.723822+00:00 host heroku web.1 - State changed from up to down`))
	r, err := http.Post(server.URL+"/app", "", body)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, r.StatusCode)
	assert.Equal(t, "heroku[web.1]: State changed from up to down", l.m)
}

func TestLogEntryWithEmptyLineAtTheEnd(t *testing.T) {
	app.parse = logparser.Parse
	defer func() {
		app.parse = parseFunc
	}()

	body := bytes.NewBuffer([]byte("89 <45>1 2016-10-15T08:59:08.723822+00:00 host heroku web.1 - State changed from up to down\n"))
	r, err := http.Post(server.URL+"/app", "", body)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, r.StatusCode)
	assert.Equal(t, "heroku[web.1]: State changed from up to down", l.m)
}

func TestAnsiCodeStripping(t *testing.T) {
	app.parse = logparser.Parse
	app.stripAnsiCodes = true
	defer func() {
		app.parse = parseFunc
		app.stripAnsiCodes = false
	}()

	body := bytes.NewBuffer([]byte(`89 <45>1 2016-10-15T08:59:08.723822+00:00 host heroku web.1 - [1m[36m(0.1ms)[0m [1mBEGIN[0m`))
	r, err := http.Post(server.URL+"/app", "", body)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, r.StatusCode)
	assert.Equal(t, "heroku[web.1]: (0.1ms) BEGIN", l.m)
}

type LastMessageLogger struct {
	m string
}

func (l *LastMessageLogger) Log(t time.Time, s string) {
	l.m = s
}

func (l *LastMessageLogger) Stop() {}
