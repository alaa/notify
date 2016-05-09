package main

import (
	"fmt"
	"log"
	"os"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/stvp/pager"
)

type Consul struct {
	agent   *consulapi.Agent
	catalog *consulapi.Catalog
	health  *consulapi.Health
}

func New(consulAddr string) *Consul {
	config := &consulapi.Config{
		Address: consulAddr,
		Scheme:  "http",
	}

	client, err := consulapi.NewClient(config)
	if err != nil {
		panic(err)
	}

	return &Consul{
		agent:   client.Agent(),
		catalog: client.Catalog(),
		health:  client.Health(),
	}
}

type Services map[string][]string

func (c *Consul) services() Services {
	services, _, err := c.catalog.Services(nil)
	if err != nil {
		log.Print(err)
	}

	return services
}

type serviceChecks []*consulapi.HealthCheck

func (c *Consul) servicesChecks(services Services) (servicesChecks []serviceChecks) {
	for id, _ := range c.services() {
		checks, _, err := c.health.Checks(id, &consulapi.QueryOptions{})
		if err != nil {
			log.Print(err)
		}
		servicesChecks = append(servicesChecks, checks)
	}

	return servicesChecks
}

func failingChecks(servicesChecks []serviceChecks) (failingChecks serviceChecks) {
	for _, serviceChecks := range servicesChecks {
		for _, check := range serviceChecks {
			if check.Status != "passing" {
				failingChecks = append(failingChecks, check)
			}
		}
	}
	return failingChecks
}

type notified map[*consulapi.HealthCheck]record

type record struct {
	timestamp time.Time
	count     int
}

var notifiedChecks = make(notified)

func isNotified(check *consulapi.HealthCheck) bool {
	now := time.Now()
	record, ok := notifiedChecks[check]

	// Ignore the first failing check, as it might be a deployment cycle.
	if !ok {
		record.timestamp = now
		record.count = 0
		return true
	}

	timediff := now.Sub(record.timestamp).Seconds()

	switch {
	// Notify if the check is failing for more than 30 seconds.
	case timediff >= 30 && record.count == 0:
		return false

	// notify again if the alert was failing for one hour without human acknowledgement.
	case timediff > 3600:
		record.timestamp = now
		record.count += 1
		return false
	}

	return true
}

func notify(failingChecks serviceChecks) {
	for _, check := range failingChecks {
		if !isNotified(check) {
			incidentKey, err := pager.Trigger(fmt.Sprintf("%s => %s", check.ServiceName, check.Output))
			if err != nil {
				log.Print(err)
			}
			log.Println("New incident has been submitted to pagerduty", incidentKey)
		}
	}
}

func main() {
	consulAddr := os.Getenv("CONSUL_ADDR")
	if consulAddr == "" {
		log.Print("CONSUL_ADDR is not set, using localhost:8500")
		consulAddr = "localhost:8500"
	}

	pagerdutyServiceKey := os.Getenv("PAGERDUTY_SERVICE_KEY")
	if pagerdutyServiceKey == "" {
		log.Fatal("PAGERDUTY_SERVICE_KEY is not set")
	}

	pager.ServiceKey = pagerdutyServiceKey
	c := New(consulAddr)

	ticker := time.Tick(time.Second * 15)
	for {
		select {
		case <-ticker:
			notify(failingChecks(c.servicesChecks(c.services())))
		}
	}
}
