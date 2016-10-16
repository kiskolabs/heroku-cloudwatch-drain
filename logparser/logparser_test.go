package logparser

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseValidMessage(t *testing.T) {
	entry, err := Parse([]byte(`89 <45>1 2016-10-15T08:59:08.723822+00:00 host heroku web.1 - State changed from up to down`))
	assert.NoError(t, err)
	assert.Equal(t, "heroku[web.1]: State changed from up to down", entry.Message)
	assert.WithinDuration(t, time.Date(2016, 10, 15, 8, 59, 8, 723822000, time.UTC), entry.Time, time.Microsecond)
}

func TestParseInvalidMessages(t *testing.T) {
	tests := []string{
		``,
		`89`,
		`89 <45>`,
		`89 <45>1`,
		`89 <45>1 2016-10-15T08:59:08.723822+00:00`,
		`89 <45>1 2016-10-15T08:59:08.723822+00:00 host`,
		`89 <45>1 2016-10-15T08:59:08.723822+00:00 host heroku`,
		`89 <45>1 2016-10-15T08:59:08.723822+00:00 host heroku web.1`,
		`89 <45>1 2016-10-15T08:59:08.723822+00:00 host heroku web.1 -`,
		`<45>1 2016-10-15T08:59:08.723822+00:00 host heroku web.1 - - State changed from up to down`,
	}

	for _, test := range tests {
		entry, err := Parse([]byte(test))
		assert.Error(t, err)
		assert.Nil(t, entry)
	}
}
