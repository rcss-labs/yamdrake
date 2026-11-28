//go:build integration && floci

package ecsdeploy_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

func TestZZDebugCrash(t *testing.T) {
	s := newFlociService(t)
	digest := s.acct.RepoCrashingImage(flociRegion, s.repo, "crash", "crash", 3)
	out, _ := s.deploy(t, s.acct.AWSConfig(flociRegion), digest)
	fmt.Println("OUTCOME", out.State)
	c := ecs.NewFromConfig(s.acct.AWSConfig(flociRegion))
	for i := 0; i < 8; i++ {
		time.Sleep(5 * time.Second)
		l, _ := c.ListTasks(context.Background(), &ecs.ListTasksInput{Cluster: aws.String(s.cluster), DesiredStatus: ecstypes.DesiredStatusStopped})
		d, err := c.DescribeTasks(context.Background(), &ecs.DescribeTasksInput{Cluster: aws.String(s.cluster), Tasks: l.TaskArns})
		fmt.Println("round", i, "stopped", len(l.TaskArns), err)
		for _, tk := range d.Tasks {
			code := "nil"
			if len(tk.Containers) > 0 && tk.Containers[0].ExitCode != nil {
				code = fmt.Sprint(*tk.Containers[0].ExitCode)
			}
			fmt.Println("  ", aws.ToString(tk.TaskDefinitionArn), aws.ToString(tk.StoppedReason), tk.StopCode, code, tk.StoppedAt)
		}
		sv, _ := c.DescribeServices(context.Background(), &ecs.DescribeServicesInput{Cluster: aws.String(s.cluster), Services: []string{"web"}})
		for _, dp := range sv.Services[0].Deployments {
			fmt.Println("  dep", aws.ToString(dp.Status), dp.RolloutState, dp.RunningCount, aws.ToString(dp.TaskDefinition))
		}
	}
}
