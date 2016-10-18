package logger

import (
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
	"github.com/honeybadger-io/honeybadger-go"
	"github.com/satori/go.uuid"
)

const (
	maxBatchByteSize = 1048576
	maxBatchLength   = 10000
	logEventOverhead = 26
)

type Logger interface {
	Log(t time.Time, s string)
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
}

func NewCloudWatchLogger(logGroupName string, retention int) (*CloudWatchLogger, error) {
	sess, err := session.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %s", err)
	}

	client := cloudwatchlogs.New(sess)

	cwl := &CloudWatchLogger{
		logGroupName:  logGroupName,
		logStreamName: uuid.NewV4().String(),
		retention:     retention,
		logs:          make(chan *cloudwatchlogs.InputLogEvent, 100),
		client:        client,
	}
	go cwl.worker()
	return cwl, nil
}

func (cwl *CloudWatchLogger) Log(t time.Time, s string) {
	cwl.logs <- &cloudwatchlogs.InputLogEvent{
		Message:   aws.String(s),
		Timestamp: aws.Int64(t.UnixNano() / int64(time.Millisecond)),
	}
}

func (cwl *CloudWatchLogger) worker() {
	cwl.resetBatch()
	for {
		select {
		case logEvent := <-cwl.logs:
			cwl.addToBatch(logEvent)
		case <-cwl.timeout:
			cwl.flush()
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
		if honeybadger.Config.APIKey != "" {
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
	resp, err := cwl.client.PutLogEvents(params)

	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok {
			if awsErr.Code() == "ResourceNotFoundException" {
				if err = cwl.createLogStream(); err != nil {
					return err
				}
				return cwl.sendToCloudWatchLogs(batch, batchByteSize)
			}
		}
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
	if _, err := cwl.client.CreateLogStream(params); err != nil {
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
	if _, err := cwl.client.CreateLogGroup(params); err != nil {
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
	_, err := cwl.client.PutRetentionPolicy(params)
	if err != nil {
		return fmt.Errorf("PutRetentionPolicy failed: %s", err)
	}
	return nil
}
