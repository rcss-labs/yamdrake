package awsx

import (
	"context"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/smithy-go/middleware"
)

// throttled counts the throttling answers this process has seen from AWS since
// the last TakeThrottles.
var throttled atomic.Int64

// TakeThrottles returns how many throttling answers AWS gave since the last
// call, and starts counting again from zero.
func TakeThrottles() int64 { return throttled.Swap(0) }

// ObserveThrottles returns cfg with a middleware that counts every throttling
// answer (retries included, because each one is load AWS pushed back on). It
// runs inside the SDK's retry loop, so it sees an attempt's answer before the
// retryer hides it. Clients built from the returned configuration are counted.
func ObserveThrottles(cfg aws.Config) aws.Config {
	cfg.APIOptions = append(cfg.APIOptions[:len(cfg.APIOptions):len(cfg.APIOptions)], func(stack *middleware.Stack) error {
		return stack.Finalize.Insert(countThrottle, "Retry", middleware.After)
	})
	return cfg
}

var countThrottle = middleware.FinalizeMiddlewareFunc("YumdrakeCountThrottles",
	func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
		out, metadata, err := next.HandleFinalize(ctx, in)
		if err != nil {
			for _, check := range retry.DefaultThrottles {
				if check.IsErrorThrottle(err).Bool() {
					throttled.Add(1)
					break
				}
			}
		}
		return out, metadata, err
	})
