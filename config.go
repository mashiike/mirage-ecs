package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	metadata "github.com/brunoscheufler/aws-ecs-metadata-go"
	config "github.com/kayac/go-config"
)

type Config struct {
	Host      Host      `yaml:"host"`
	Listen    Listen    `yaml:"listen"`
	HtmlDir   string    `yaml:"htmldir"`
	Parameter Paramters `yaml:"parameters"`
	ECS       ECSCfg    `yaml:"ecs"`
	Link      Link      `yaml:"link"`

	localMode bool
	session   *session.Session
}

type ECSCfg struct {
	Region                   string                   `yaml:"region"`
	Cluster                  string                   `yaml:"cluster"`
	CapacityProviderStrategy CapacityProviderStrategy `yaml:"capacity_provider_strategy"`
	LaunchType               *string                  `yaml:"launch_type"`
	NetworkConfiguration     *NetworkConfiguration    `yaml:"network_configuration"`
	DefaultTaskDefinition    string                   `yaml:"default_task_definition"`
	EnableExecuteCommand     *bool                    `yaml:"enable_execute_command"`

	capacityProviderStrategy []*ecs.CapacityProviderStrategyItem `yaml:"-"`
	networkConfiguration     *ecs.NetworkConfiguration           `yaml:"-"`
}

func (c ECSCfg) String() string {
	m := map[string]interface{}{
		"region":                     c.Region,
		"cluster":                    c.Cluster,
		"capacity_provider_strategy": c.capacityProviderStrategy,
		"launch_type":                c.LaunchType,
		"network_configuration":      c.networkConfiguration,
		"default_task_definition":    c.DefaultTaskDefinition,
		"enable_execute_command":     c.EnableExecuteCommand,
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (c ECSCfg) valid() bool {
	if c.Region == "" {
		return false
	}
	if c.Cluster == "" {
		return false
	}
	if c.LaunchType == nil && c.capacityProviderStrategy == nil {
		return false
	}
	if c.networkConfiguration == nil {
		return false
	}
	return true
}

type CapacityProviderStrategy []*CapacityProviderStrategyItem

func (s CapacityProviderStrategy) toSDK() []*ecs.CapacityProviderStrategyItem {
	if len(s) == 0 {
		return nil
	}
	var items []*ecs.CapacityProviderStrategyItem
	for _, item := range s {
		items = append(items, item.toSDK())
	}
	return items
}

type CapacityProviderStrategyItem struct {
	CapacityProvider *string `yaml:"capacity_provider"`
	Weight           *int64  `yaml:"weight"`
	Base             *int64  `yaml:"base"`
}

func (i CapacityProviderStrategyItem) toSDK() *ecs.CapacityProviderStrategyItem {
	return &ecs.CapacityProviderStrategyItem{
		CapacityProvider: i.CapacityProvider,
		Weight:           i.Weight,
		Base:             i.Base,
	}
}

type NetworkConfiguration struct {
	AwsVpcConfiguration *AwsVpcConfiguration `yaml:"awsvpc_configuration"`
}

func (c *NetworkConfiguration) toSDK() *ecs.NetworkConfiguration {
	if c == nil {
		return nil
	}
	return &ecs.NetworkConfiguration{
		AwsvpcConfiguration: c.AwsVpcConfiguration.toSDK(),
	}
}

type AwsVpcConfiguration struct {
	AssignPublicIp *string   `yaml:"assign_public_ip"`
	SecurityGroups []*string `yaml:"security_groups"`
	Subnets        []*string `yaml:"subnets"`
}

func (c *AwsVpcConfiguration) toSDK() *ecs.AwsVpcConfiguration {
	return &ecs.AwsVpcConfiguration{
		AssignPublicIp: c.AssignPublicIp,
		Subnets:        c.Subnets,
		SecurityGroups: c.SecurityGroups,
	}
}

type Host struct {
	WebApi             string `yaml:"webapi"`
	ReverseProxySuffix string `yaml:"reverse_proxy_suffix"`
}

type Link struct {
	HostedZoneID           string   `yaml:"hosted_zone_id"`
	DefaultTaskDefinitions []string `yaml:"default_task_definitions"`
}

type Listen struct {
	ForeignAddress string    `yaml:"foreign_address"`
	HTTP           []PortMap `yaml:"http"`
	HTTPS          []PortMap `yaml:"https"`
}

type PortMap struct {
	ListenPort int `yaml:"listen"`
	TargetPort int `yaml:"target"`
}

type Parameter struct {
	Name     string `yaml:"name"`
	Env      string `yaml:"env"`
	Rule     string `yaml:"rule"`
	Required bool   `yaml:"required"`
	Regexp   regexp.Regexp
}

type Paramters []*Parameter

func NewConfig(path string) *Config {
	log.Printf("[info] loading config file: %s", path)
	// default config
	cfg := &Config{
		Host: Host{
			WebApi:             "localhost",
			ReverseProxySuffix: ".dev.example.net",
		},
		Listen: Listen{
			ForeignAddress: "127.0.0.1",
			HTTP:           []PortMap{},
			HTTPS:          []PortMap{},
		},
		HtmlDir: "./html",
	}

	err := config.LoadWithEnv(&cfg, path)
	if err != nil {
		log.Fatalf("cannot load config: %s: %s", path, err)
	}

	for _, v := range cfg.Parameter {
		if v.Rule != "" {
			paramRegex := regexp.MustCompile(v.Rule)
			v.Regexp = *paramRegex
		}
	}

	cfg.session = session.Must(session.NewSession(
		&aws.Config{Region: aws.String(cfg.ECS.Region)},
	))
	cfg.ECS.capacityProviderStrategy = cfg.ECS.CapacityProviderStrategy.toSDK()
	cfg.ECS.networkConfiguration = cfg.ECS.NetworkConfiguration.toSDK()

	if err := cfg.fillECSDefaults(context.TODO()); err != nil {
		log.Printf("[warn] failed to fill ECS defaults: %s", err)
	}
	return cfg
}

func (c *Config) fillECSDefaults(ctx context.Context) error {
	defer func() {
		if c.ECS.valid() {
			log.Printf("[info] built ECS config: %s", c.ECS)
		} else {
			log.Printf("[error] invalid ECS config: %s", c.ECS)
			log.Println("[error] ECS config is invalid, so you may not be able to launch ECS tasks")
		}
	}()
	if c.ECS.Region == "" {
		c.ECS.Region = os.Getenv("AWS_REGION")
		log.Printf("[info] AWS_REGION is not set, using region=%s", c.ECS.Region)
	}
	if c.ECS.LaunchType == nil && c.ECS.CapacityProviderStrategy == nil {
		launchType := "FARGATE"
		c.ECS.LaunchType = &launchType
		log.Printf("[info] launch_type and capacity_provider_strategy are not set, using launch_type=%s", *c.ECS.LaunchType)
	}
	if c.ECS.EnableExecuteCommand == nil {
		enableExecuteCommand := true
		c.ECS.EnableExecuteCommand = &enableExecuteCommand
		log.Printf("[info] enable_execute_command is not set, using enable_execute_command=%t", *c.ECS.EnableExecuteCommand)
	}

	meta, err := metadata.Get(ctx, &http.Client{})
	if err != nil {
		return err
		/*
			for local debugging
			meta = &metadata.TaskMetadataV4{
				Cluster: "your test cluster",
				TaskARN: "your test task arn running on the cluster",
			}
		*/
	}
	log.Printf("[debug] task metadata: %v", meta)
	var cluster, taskArn, service string
	switch m := meta.(type) {
	case *metadata.TaskMetadataV3:
		cluster = m.Cluster
		taskArn = m.TaskARN
	case *metadata.TaskMetadataV4:
		cluster = m.Cluster
		taskArn = m.TaskARN
	}

	svc := ecs.New(c.session)
	if out, err := svc.DescribeTasksWithContext(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   []*string{&taskArn},
	}); err != nil {
		return err
	} else {
		if len(out.Tasks) == 0 {
			return fmt.Errorf("cannot find task: %s", taskArn)
		}
		group := aws.StringValue(out.Tasks[0].Group)
		if strings.HasPrefix(group, "service:") {
			service = group[8:]
		}
	}
	if out, err := svc.DescribeServicesWithContext(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: []*string{&service},
	}); err != nil {
		return err
	} else {
		if len(out.Services) == 0 {
			return fmt.Errorf("cannot find service: %s", service)
		}
		if c.ECS.networkConfiguration == nil {
			c.ECS.networkConfiguration = out.Services[0].NetworkConfiguration
			log.Printf("[info] network_configuration is not set, using network_configuration=%v", c.ECS.networkConfiguration)
		}
	}
	return nil
}
