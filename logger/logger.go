package logger

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
	"github.com/honeybadger-io/honeybadger-go"
	"github.com/newrelic/go-agent"
	"github.com/satori/go.uuid"
)

const (
	maxBatchByteSize = 1048576 - 1024 // Reserve 1KB for request body overhead.
	maxBatchLength   = 10000
	logEventOverhead = 26
)

// The Logger interface defines the minimum set of functions any logger must
// implement.
type Logger interface {
	Log(t time.Time, s string)
	Stop()
}

type logBatch []*cloudwatchlogs.InputLogEvent

func (b logBatch) Len() int {
	return len(b)
}

func (b logBatch) Less(i, j int) bool {
	return *b[i].Timestamp < *b[j].Timestamp
}

func (b logBatch) Swap(i, j int) {
	b[i], b[j] = b[j], b[i]
}

// CloudWatchLogger is a Logger that stores log entries in CloudWatch Logs. Logs
// are automatically batched for a short period of time before being sent.
type CloudWatchLogger struct {
	logGroupName  string
	logStreamName string
	sequenceToken *string
	retention     int
	logs          chan *cloudwatchlogs.InputLogEvent
	batch         logBatch
	batchByteSize int
	timeout       <-chan time.Time
	client        *cloudwatchlogs.CloudWatchLogs
	stop          chan chan bool
	newrelic      newrelic.Application
}

// NewCloudWatchLogger returns a CloudWatchLogger that is ready to be used.
func NewCloudWatchLogger(logGroupName string, retention int, nrApp newrelic.Application) (*CloudWatchLogger, error) {
	sess, err := session.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %s", err)
	}

	client := cloudwatchlogs.New(sess, aws.NewConfig().WithMaxRetries(0))

	cwl := &CloudWatchLogger{
		logGroupName:  logGroupName,
		logStreamName: uuid.NewV4().String(),
		retention:     retention,
		logs:          make(chan *cloudwatchlogs.InputLogEvent),
		client:        client,
		stop:          make(chan chan bool),
		newrelic:      nrApp,
	}
	go cwl.worker()
	return cwl, nil
}

// Log enqueues a log entry to be stored in CloudWatch Logs.
func (cwl *CloudWatchLogger) Log(t time.Time, s string) {
	cwl.logs <- &cloudwatchlogs.InputLogEvent{
		Message:   aws.String(s),
		Timestamp: aws.Int64(t.UnixNano() / int64(time.Millisecond)),
	}
}

// Stop flushes all pending logs and blocks until they are sent to CloudWatch
// Logs.
func (cwl *CloudWatchLogger) Stop() {
	stopped := make(chan bool)
	cwl.stop <- stopped
	<-stopped
}

func (cwl *CloudWatchLogger) worker() {
	cwl.resetBatch()
	for {
		select {
		case logEvent := <-cwl.logs:
			cwl.addToBatch(logEvent)
		case <-cwl.timeout:
			cwl.flush()
		case stopped := <-cwl.stop:
			if len(cwl.batch) > 0 {
				cwl.flush()
			}
			stopped <- true
		}
	}
}

func (cwl *CloudWatchLogger) addToBatch(logEvent *cloudwatchlogs.InputLogEvent) {
	logEventSize := len(*logEvent.Message) + logEventOverhead

	if logEventSize+cwl.batchByteSize > maxBatchByteSize || len(cwl.batch) == maxBatchLength {
		cwl.flush()
	}

	if cwl.timeout == nil {
		cwl.timeout = time.After(time.Second)
	}

	cwl.batch = append(cwl.batch, logEvent)
	cwl.batchByteSize += logEventSize
}

func (cwl *CloudWatchLogger) flush() {
	batch := cwl.batch
	batchByteSize := cwl.batchByteSize
	cwl.resetBatch()
	sort.Sort(batch)
	if err := cwl.sendToCloudWatchLogs(batch, batchByteSize); err != nil {
		if awsErr, ok := err.(awserr.Error); ok {
			// If it's an AWS error, use the AWS error code on Honeybadger
			honeybadger.Notify(err, honeybadger.ErrorClass{
				Name: awsErr.Code(),
			})
		} else if strings.Index(err.Error(), " failed: ") > 0 {
			// Better Honeybadger reports for custom errors
			splits := strings.SplitN(err.Error(), " failed: ", 2)
			honeybadger.Notify(err, honeybadger.ErrorClass{
				Name: splits[0],
			})
		} else {
			// Fallback to a regular error notification
			honeybadger.Notify(err)
		}

		log.Println(err)
	}
}

func (cwl *CloudWatchLogger) resetBatch() {
	cwl.batch = logBatch{}
	cwl.batchByteSize = 0
	cwl.timeout = nil
}

func (cwl *CloudWatchLogger) sendToCloudWatchLogs(batch logBatch, batchByteSize int) error {
	s := time.Now()
	params := &cloudwatchlogs.PutLogEventsInput{
		LogEvents:     batch,
		LogGroupName:  aws.String(cwl.logGroupName),
		LogStreamName: aws.String(cwl.logStreamName),
		SequenceToken: cwl.sequenceToken,
	}
	txn := cwl.newrelic.StartTransaction("PutLogEvents", nil, nil)
	resp, err := cwl.client.PutLogEvents(params)
	txn.End()

	if err != nil {
		if resp != nil && resp.NextSequenceToken != nil {
			cwl.sequenceToken = resp.NextSequenceToken
		}
		if awsErr, ok := err.(awserr.Error); ok {
			if awsErr.Code() == "ResourceNotFoundException" {
				if err = cwl.createLogStream(); err != nil {
					return err
				}
				return cwl.sendToCloudWatchLogs(batch, batchByteSize)
			}
		}
		cwl.reEnqueueBatch(batch)
		return fmt.Errorf("PutLogEvents failed: %s", err)
	}
	log.Printf("wrote %d log events (%d bytes) in %s\n", len(batch), batchByteSize, time.Since(s))

	cwl.sequenceToken = resp.NextSequenceToken
	return nil
}

func (cwl *CloudWatchLogger) createLogStream() error {
	params := &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName:  aws.String(cwl.logGroupName),
		LogStreamName: aws.String(cwl.logStreamName),
	}
	txn := cwl.newrelic.StartTransaction("CreateLogStream", nil, nil)
	_, err := cwl.client.CreateLogStream(params)
	txn.End()
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok {
			if awsErr.Code() == "ResourceNotFoundException" {
				if err = cwl.createLogGroup(); err != nil {
					return err
				}
				return cwl.createLogStream()
			}
		}
		return fmt.Errorf("CreateLogStream failed: %s", err)
	}
	return nil
}

func (cwl *CloudWatchLogger) createLogGroup() error {
	params := &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(cwl.logGroupName),
	}
	txn := cwl.newrelic.StartTransaction("CreateLogGroup", nil, nil)
	_, err := cwl.client.CreateLogGroup(params)
	txn.End()
	if err != nil {
		return fmt.Errorf("CreateLogGroup failed: %s", err)
	}
	return cwl.putRetentionPolicy()
}

func (cwl *CloudWatchLogger) putRetentionPolicy() error {
	if cwl.retention == 0 {
		return nil
	}
	params := &cloudwatchlogs.PutRetentionPolicyInput{
		LogGroupName:    aws.String(cwl.logGroupName),
		RetentionInDays: aws.Int64(int64(cwl.retention)),
	}
	txn := cwl.newrelic.StartTransaction("PutRetentionPolicy", nil, nil)
	_, err := cwl.client.PutRetentionPolicy(params)
	txn.End()
	if err != nil {
		return fmt.Errorf("PutRetentionPolicy failed: %s", err)
	}
	return nil
}

func (cwl *CloudWatchLogger) reEnqueueBatch(batch logBatch) {
	for _, logEvent := range batch {
		cwl.addToBatch(logEvent)
	}
}
