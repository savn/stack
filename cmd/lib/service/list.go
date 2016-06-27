package service

import (
	"flag"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/segmentio/stack/cmd/lib"
	"github.com/segmentio/stack/cmd/lib/cluster"
)

func CmdList(prog string, target string, cmd string, args ...string) (err error) {
	var flags = flag.NewFlagSet("service", flag.ContinueOnError)
	var clusters stack.StringList
	var services []Service

	flags.Var(&clusters, "cluster", "a comma separated list of the clusters to search for services in")

	if err = flags.Parse(args); err != nil {
		return
	}

	if services, err = List(session.New(), clusters...); err != nil {
		return
	}

	table := stack.NewTable(
		"NAME", "STATUS", "CLUSTER", "TASK DEFINITION", "DESIRED COUNT:", "PENDING COUNT:", "RUNNING COUNT:", "CREATED ON",
	)

	for _, service := range services {
		table.Append(stack.Row{
			service.Name,
			service.Status,
			service.Cluster,
			service.Task,
			strconv.Itoa(service.DesiredCount),
			strconv.Itoa(service.PendingCount),
			strconv.Itoa(service.RunningCount),
			service.CreatedOn.Format(time.RFC1123),
		})
	}

	return table.Write(stack.Stdout)
}

func List(config client.ConfigProvider, clusters ...string) (services []Service, err error) {
	for r := range ListAsync(config, clusters...) {
		if r.Error != nil {
			err = stack.AppendError(err, r.Error)
		} else {
			services = append(services, r.Service)
		}
	}
	Sort(services)
	return
}

type ListResult struct {
	Service Service
	Error   error
}

func ListAsync(config client.ConfigProvider, clusters ...string) (res <-chan ListResult) {
	var cli = ecs.New(config)
	var chn = make(chan ListResult, 10)
	var arg <-chan cluster.ListArnResult

	if len(clusters) == 0 {
		arg = cluster.ListArnAsync(config)
	} else {
		c := make(chan cluster.ListArnResult, len(clusters))
		c <- cluster.ListArnResult{ClusterArns: clusters}
		arg = c
		close(c)
	}

	go listAsync(cli, arg, chn)
	res = chn
	return
}

func listAsync(client *ecs.ECS, arg <-chan cluster.ListArnResult, res chan<- ListResult) {
	defer close(res)

	join := &sync.WaitGroup{}
	defer join.Wait()

	for c := range arg {
		if c.Error != nil {
			res <- ListResult{Error: c.Error}
		} else {
			join.Add(len(c.ClusterArns))
			for _, arn := range c.ClusterArns {
				go listClusterAsync(client, join, arn, res)
			}
		}
	}
}

func listClusterAsync(client *ecs.ECS, join *sync.WaitGroup, cluster string, res chan<- ListResult) {
	var token *string

	defer join.Done()

	for {
		var list *ecs.ListServicesOutput
		var err error

		if list, err = client.ListServices(&ecs.ListServicesInput{
			Cluster:   aws.String(cluster),
			NextToken: token,
		}); err != nil {
			res <- ListResult{Error: err}
			break
		}

		if len(list.ServiceArns) != 0 {
			join.Add(1)
			go describeServicesAsync(client, join, cluster, list.ServiceArns, res)
		}

		if token = list.NextToken; token == nil {
			break
		}
	}
}

func describeServicesAsync(client *ecs.ECS, join *sync.WaitGroup, cluster string, services []*string, res chan<- ListResult) {
	defer join.Done()

	if d, err := client.DescribeServices(&ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: services,
	}); err != nil {
		res <- ListResult{Error: err}
	} else {
		for _, s := range d.Services {
			res <- ListResult{Service: makeService(s)}
		}
	}
}
