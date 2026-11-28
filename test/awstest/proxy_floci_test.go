//go:build floci

package awstest_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/yumdrake/test/awstest"
)

func TestProxyRecordsFailsRewritesAndDrops(t *testing.T) {
	w := awstest.New(t)

	// setup returns SDK clients that talk to a fresh proxy, with retries off
	// so that an injected failure or drop is visible to the caller.
	setup := func(t *testing.T) (*awstest.Proxy, string, *sts.Client, *ecs.Client) {
		t.Helper()
		accountID := awstest.RandomAccountID()
		proxy := w.Proxy(t)
		cfg := w.Account(accountID).AWSConfigAt("us-east-1", proxy.URL)
		cfg.RetryMaxAttempts = 1
		return proxy, accountID, sts.NewFromConfig(cfg), ecs.NewFromConfig(cfg)
	}
	ctx := context.Background()

	t.Run("records requests", func(t *testing.T) {
		proxy, accountID, stsClient, ecsClient := setup(t)
		if _, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); err != nil {
			t.Fatalf("GetCallerIdentity: %v", err)
		}
		if _, err := ecsClient.ListClusters(ctx, &ecs.ListClustersInput{}); err != nil {
			t.Fatalf("ListClusters: %v", err)
		}
		got := proxy.Requests()
		if len(got) != 2 {
			t.Fatalf("recorded %d requests, want 2: %+v", len(got), got)
		}
		if got[0].Service != "sts" || got[0].Operation != "GetCallerIdentity" || got[0].Account != accountID {
			t.Errorf("first request = %+v, want sts GetCallerIdentity for %s", got[0], accountID)
		}
		if got[1].Service != "ecs" || got[1].Operation != "ListClusters" || got[1].Account != accountID {
			t.Errorf("second request = %+v, want ecs ListClusters for %s", got[1], accountID)
		}
		if !strings.Contains(string(got[1].Body), "{") {
			t.Errorf("ecs request body %q is not recorded", got[1].Body)
		}
	})

	t.Run("fails the next matching call only", func(t *testing.T) {
		proxy, _, stsClient, ecsClient := setup(t)
		proxy.FailNext("ecs", "ListClusters", "ThrottlingException", 400)

		// A call that does not match is not affected.
		if _, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); err != nil {
			t.Fatalf("unmatched GetCallerIdentity: %v", err)
		}
		_, err := ecsClient.ListClusters(ctx, &ecs.ListClustersInput{})
		var apiErr smithy.APIError
		if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "ThrottlingException" {
			t.Fatalf("ListClusters error = %v, want ThrottlingException", err)
		}
		if _, err := ecsClient.ListClusters(ctx, &ecs.ListClustersInput{}); err != nil {
			t.Fatalf("second ListClusters: %v", err)
		}
	})

	t.Run("rewrites matching responses", func(t *testing.T) {
		proxy, accountID, stsClient, _ := setup(t)
		proxy.Rewrite("sts", "GetCallerIdentity", func(body []byte) []byte {
			return bytes.ReplaceAll(body, []byte(accountID), []byte("999999999999"))
		})
		out, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		if err != nil {
			t.Fatalf("GetCallerIdentity: %v", err)
		}
		if got := *out.Account; got != "999999999999" {
			t.Errorf("Account = %s, want the rewritten 999999999999", got)
		}
	})

	t.Run("drops the connection after forwarding", func(t *testing.T) {
		proxy, _, _, ecsClient := setup(t)
		name := "drop-" + awstest.RandomSuffix()
		proxy.DropAfterForward("ecs", "CreateCluster")
		if _, err := ecsClient.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: &name}); err == nil {
			t.Fatal("CreateCluster succeeded, want a connection error")
		}
		// The call reached Floci: the cluster exists.
		out, err := ecsClient.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{name}})
		if err != nil {
			t.Fatalf("DescribeClusters: %v", err)
		}
		if len(out.Clusters) != 1 || *out.Clusters[0].ClusterName != name {
			t.Errorf("clusters = %+v, want %s to exist", out.Clusters, name)
		}
		// Only the next matching call is dropped.
		other := "ok-" + awstest.RandomSuffix()
		if _, err := ecsClient.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: &other}); err != nil {
			t.Fatalf("second CreateCluster: %v", err)
		}
	})
}
