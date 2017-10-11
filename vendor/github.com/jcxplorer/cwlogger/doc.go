// Package cwlogger is a library for reliably writing logs to Amazon CloudWatch
// Logs.
//
// Features
//
// Batches log messages for efficiency by decreasing the number of API calls.
//
// Handles log stream creation based on log throughput. If too many logs are
// being written in a short period of time, the CloudWatch Logs API will return
// a ThrottlingException, which this library handles by creating an additional
// log stream every time that happens. Subsequent log writes will be distributed
// throughout all existing log streams.
//
// Handles DataAlreadyAcceptedException and InvalidSequenceTokenException errors
// by setting the log stream sequence token to the one returned by the error
// response. For InvalidSequenceTokenException, the request will be retried with
// the correct sequence token.
//
// Retries PutLogEvents API calls in case of connection failure, or temporary
// errors on CloudWatch Logs.
//
// Dependencies
//
// The only dependency for this package is the official AWS SDK for Go.
//
// Usage
//
// Use the AWS SDK for Go to configure and create the client.
//
//   logger, err := cwlogger.New(&cwlogger.Config{
//     LogGroupName: "groupName",
//     Client: cloudwatchlogs.New(session.New())
//   })
//   // handle err
//   logger.Log(time.Now(), "log message")
//
// For information on how to configure the AWS client, refer to the AWS
// documentation at http://docs.aws.amazon.com/sdk-for-go/api/aws/session/.
package cwlogger
