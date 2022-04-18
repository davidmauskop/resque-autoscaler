package main

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/kelseyhightower/envconfig"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type AutoscalerConfig struct {
	WorkerServiceId    string        `required:"true" split_words:"true"`
	RenderAPIKey       string        `required:"true" split_words:"true"`
	RedisAddress       string        `required:"true" split_words:"true"`
	MinInstances       int           `default:"2" split_words:"true"`
	MaxInstances       int           `default:"50" split_words:"true"`
	WorkersPerInstance int           `default:"1" split_words:"true"`
	Interval           time.Duration `default:"1s"`
	NumSamples         int           `default:"1" split_words:"true"`
	ScaleUpDelay       time.Duration `default:"1m" split_words:"true"`
	ScaleDownDelay     time.Duration `default:"10m" split_words:"true"`
}

type Autoscaler struct {
	config        AutoscalerConfig
	instances     int
	lastScaleTime time.Time
	samples       []int
	redis         *redis.Client
	ctx           context.Context
}

var autoscaler *Autoscaler

func init() {
	var config AutoscalerConfig
	if err := envconfig.Process("", &config); err != nil {
		log.Fatal(err)
	}
	autoscaler = &Autoscaler{config: config}
	autoscaler.instances = getInstanceCount()
	autoscaler.redis = redis.NewClient(&redis.Options{
		Addr: config.RedisAddress,
	})
	autoscaler.ctx = context.Background()
}

func main() {
	instancesChan := make(chan int)
	go scaleWorkersLoop(instancesChan)
	calculateInstancesLoop(instancesChan)
}

func getInstanceCount() int {
	path := "/services/" + autoscaler.config.WorkerServiceId
	status, resp, err := renderAPICall("GET", path, "")
	if err != nil || status != http.StatusOK {
		log.Error("unable to retrieve current instance count")
		return autoscaler.config.MinInstances
	}
	count := gjson.Get(resp, "serviceDetails.numInstances").Num
	if count > 0 {
		return int(count)
	}
	return autoscaler.config.MinInstances
}

func renderAPICall(method, path, body string) (int, string, error) {
	url := "https://api.render.com/v1" + path
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, payload)
	if err != nil {
		return 0, "", err
	}
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", autoscaler.config.RenderAPIKey))

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}

	defer res.Body.Close()
	resBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return 0, "", err
	}

	return res.StatusCode, string(resBody), nil
}

func calculateInstancesLoop(c chan int) {
	for {
		n := calculateDesiredInstances()
		if n != autoscaler.instances {
			c <- n
			autoscaler.instances = n
			autoscaler.lastScaleTime = time.Now()
		}
		time.Sleep(autoscaler.config.Interval)
	}
}

func calculateDesiredInstances() int {
	jobs := countActiveJobs() + countPendingJobs()
	autoscaler.samples = append(autoscaler.samples, jobs)

	// not enough samples collected, return current instance count
	if len(autoscaler.samples) < autoscaler.config.NumSamples {
		return autoscaler.instances
	}

	if len(autoscaler.samples) > autoscaler.config.NumSamples {
		autoscaler.samples = autoscaler.samples[1:]
	}

	avgNumJobs := average(autoscaler.samples)
	desiredInstances := int(math.Ceil(avgNumJobs / float64(autoscaler.config.WorkersPerInstance)))
	if desiredInstances > autoscaler.config.MaxInstances {
		desiredInstances = autoscaler.config.MaxInstances
	}
	if desiredInstances < autoscaler.config.MinInstances {
		desiredInstances = autoscaler.config.MinInstances
	}

	now := time.Now()
	if desiredInstances > autoscaler.instances &&
		now.After(autoscaler.lastScaleTime.Add(autoscaler.config.ScaleUpDelay)) {
		return desiredInstances
	}

	if desiredInstances < autoscaler.instances &&
		now.After(autoscaler.lastScaleTime.Add(autoscaler.config.ScaleDownDelay)) {
		return desiredInstances
	}

	return autoscaler.instances
}

func average(xs []int) float64 {
	sum := 0
	for _, x := range xs {
		sum += x
	}
	return float64(sum) / float64(len(xs))
}

func countActiveJobs() int {
	workers, err := autoscaler.redis.SMembers(autoscaler.ctx, "resque:workers").Result()
	if err != nil {
		log.Error("failed to retrieve resque worker set from redis")
	}
	jobs := 0
	for _, worker := range workers {
		workerKey := fmt.Sprintf("resque:worker:%s", worker)
		_, err := autoscaler.redis.Get(autoscaler.ctx, workerKey).Result()
		if err == nil {
			jobs += 1
		} else if err != redis.Nil {
			log.Error("unexpected error when getting resque worker from redis")
		}
	}
	return jobs
}

func countPendingJobs() int {
	queues, err := autoscaler.redis.SMembers(autoscaler.ctx, "resque:queues").Result()
	if err != nil {
		log.Error("failed to retrieve resque queue set from redis")
	}
	var jobs int64
	for _, queue := range queues {
		queueKey := fmt.Sprintf("resque:queue:%s", queue)
		len, err := autoscaler.redis.LLen(autoscaler.ctx, queueKey).Result()
		if err != nil {
			log.Error("unexpected error when getting resque queue length")
		}
		jobs += len
	}
	return int(jobs)
}

func scaleWorkersLoop(c chan int) {
	for {
		select {
		case desiredInstances := <-c:
			updateNumInstances(desiredInstances)
		}
	}
}

func updateNumInstances(n int) {
	log.Infof("scaling to %d instances", n)

	path := fmt.Sprintf("/services/%s/scale", autoscaler.config.WorkerServiceId)
	body := fmt.Sprintf("{\"numInstances\": %d}", n)
	status, _, err := renderAPICall("POST", path, body)
	if err != nil || status != http.StatusAccepted {
		log.Errorf("failed to scale to %d instances", n)
	}
}
