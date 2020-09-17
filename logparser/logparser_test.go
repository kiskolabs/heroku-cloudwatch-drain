package logparser

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseValidMessage(t *testing.T) {
	// a non-json heroku log
	entry, err := Parse([]byte(`89 <45>1 2016-10-15T08:59:08.723822+00:00 host heroku web.1 - State changed from up to down`))
	assert.NoError(t, err)
	assert.Equal(t, "{\"heroku_app\":\"heroku\",\"heroku_process\":\"web.1\",\"message\":\"State changed from up to down\"}", entry.Message)
	assert.WithinDuration(t, time.Date(2016, 10, 15, 8, 59, 8, 723822000, time.UTC), entry.Time, time.Microsecond)

	// a json yonomi log
	entry2, err2 := Parse([]byte(`89 <45>1 2016-10-15T08:59:08.723822+00:00 host app web.2 - {"name":"yonomi-api-prod_web","hostname":"1c8e812c-2b16-42ad-b08c-c6fb30080412","pid":86,"namespace":"middleware.user_agent","level":20,"req.clientdata":{},"msg":"parseUserAgent","time":"2020-09-17T15:41:38.426Z","v":0}`))
	assert.NoError(t, err2)
	assert.Equal(t, "{\"heroku_app\":\"app\",\"heroku_process\":\"web.2\",\"name\":\"yonomi-api-prod_web\",\"hostname\":\"1c8e812c-2b16-42ad-b08c-c6fb30080412\",\"pid\":86,\"namespace\":\"middleware.user_agent\",\"level\":20,\"req.clientdata\":{},\"msg\":\"parseUserAgent\",\"time\":\"2020-09-17T15:41:38.426Z\",\"v\":0}", entry2.Message)
	assert.WithinDuration(t, time.Date(2016, 10, 15, 8, 59, 8, 723822000, time.UTC), entry2.Time, time.Microsecond)
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
