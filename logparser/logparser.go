package logparser

import (
	"errors"
	"fmt"
	"time"
)

// A ParseFunc receives a single raw (unparsed) log entry and parses it into a
// LogEntry, which it returns.
type ParseFunc func(b []byte) (*LogEntry, error)

// A LogEntry represents a single log event containing the timestamp of the
// event, and the logged message.
type LogEntry struct {
	Time    time.Time
	Message string
}

// Parse returns a parsed entry for a single Heroku syslog message delivered via
// HTTPS.
func Parse(b []byte) (*LogEntry, error) {
	parser := logParser{
		b:      b,
		cursor: 0,
		len:    len(b),
	}
	return parser.parse()
}

type logParser struct {
	b      []byte
	cursor int
	len    int
}

func (p *logParser) parse() (*LogEntry, error) {
	if err := p.skip(2); err != nil {
		return nil, fmt.Errorf("failed to skip to TIMESTAMP: %s", err)
	}

	t, err := p.parseDate()
	if err != nil {
		return nil, fmt.Errorf("failed to parse TIMESTAMP: %s", err)
	}

	if err = p.skip(1); err != nil {
		return nil, fmt.Errorf("failed to skip to APP-NAME: %s", err)
	}

	app, err := p.nextWord()
	if err != nil {
		return nil, fmt.Errorf("failed to read APP-NAME: %s", err)
	}

	process, err := p.nextWord()
	if err != nil {
		return nil, fmt.Errorf("failed to read PROCID: %s", err)
	}

	if err = p.skip(1); err != nil {
		return nil, fmt.Errorf("failed to skip to MSG: %s", err)
	}

	message := app + "[" + process + "]: " + string(p.b[p.cursor:])

	return &LogEntry{
		Time:    t,
		Message: message,
	}, nil
}

func (p *logParser) skip(num int) error {
	for skipped := 0; p.cursor < p.len; p.cursor++ {
		if p.b[p.cursor] == ' ' {
			skipped++
		}
		if skipped == num {
			p.cursor++
			return nil
		}
	}
	return errors.New("unexpected EOF")
}

func (p *logParser) parseDate() (time.Time, error) {
	word, err := p.nextWord()
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, word)
}

func (p *logParser) nextWord() (string, error) {
	start := p.cursor
	if err := p.skip(1); err != nil {
		return "", err
	}
	end := p.cursor - 1
	return string(p.b[start:end]), nil
}
