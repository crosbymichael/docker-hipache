package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/garyburd/redigo/redis"
	"io"
	"log"
	"net/http"
	"strings"
)

var (
	routes RouteConfig
	pool   *redis.Pool
)

type Event struct {
	Id     string `json:"id"`
	Status string `json:"status"`
	Image  string `json:"from"`
}

type Config struct {
	Hostname string
}

type NetworkSettings struct {
	IpAddress   string
	PortMapping map[string]map[string]string
}

type Container struct {
	Id              string
	Image           string
	Config          *Config
	NetworkSettings *NetworkSettings
}

type Response struct {
	Port string
}

type RouteConfig struct {
	RedisServer string               `toml:"redis"`
	DockerApi   string               `toml:"docker"`
	Routes      map[string]RouteHost `toml:"routes"`
}

type RouteHost struct {
	Hostname []string `toml:"hostname"`
	Port     string   `toml:"port"`
}

func inspectContainer(id string, image string, c http.Client, config RouteConfig) *Container {
	// Use the container id to fetch the container json from the Remote API
	// http://docs.docker.io/en/latest/api/docker_remote_api_v1.4/#inspect-a-container
	res, err := c.Get(fmt.Sprintf("%s/containers/%s/json", config.DockerApi))
	if err != nil {
		log.Println(err)
		return nil
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusOK {
		d := json.NewDecoder(res.Body)

		var container Container
		if err = d.Decode(&container); err != nil {
			log.Fatal(err)
		}
		container.Image = image
		return &container
	}
	return nil
}

func notify(container *Container) {
	settings := container.NetworkSettings
	log.Println(container)

	if settings != nil && settings.PortMapping != nil {
		if ports, ok := settings.PortMapping["Tcp"]; ok {
			for privatePort, publicPort := range ports {

				if route, ok := routes.Routes[container.Image]; ok {
					log.Println("Found route", publicPort, privatePort)
					if privatePort == route.Port {
						conn := pool.Get()
						defer conn.Close()
						conn.Send("MULTI")

						for _, hostname := range route.Hostname {
							conn.Send("RPUSH", fmt.Sprintf("frontend:%s", hostname), fmt.Sprintf("http://localhost:%s", publicPort))
							conn.Send("RPUSH", container.Id, fmt.Sprintf(hostname+":%s", publicPort))
						}

						if _, err := conn.Do("EXEC"); err != nil {
							log.Fatal(err)
						}

					}
				}
			}
		}
	}
}

func stop(container *Container) {
	log.Println(container.Id)

	conn := pool.Get()
	defer conn.Close()
	val, err := redis.Strings(conn.Do("LRANGE", container.Id, 0, -1))
	if err != nil {
		log.Fatal(err)
	}

	for _, val := range val {
		stale := strings.Split(val, ":")
		if _, err := conn.Do("LREM", fmt.Sprintf("frontend:%s", stale[0]), -1, fmt.Sprintf("http://localhost:%s", stale[1])); err != nil {
			log.Fatal(err)
		}
	}
}

func main() {
	var toml_path = flag.String("conf", "/root/crosby-router/docker-hipache.toml", "Please specify a path to your desired config file with -conf /path")
	if _, err := toml.DecodeFile(*toml_path, &routes); err != nil {
		log.Fatal(err)
	}

	log.Println(routes)

	pool = redis.NewPool(func() (redis.Conn, error) {
		return redis.Dial("tcp", routes.RedisServer)
	}, 10)

	for _, route := range routes.Routes {
		conn := pool.Get()
		defer conn.Close()
		conn.Send("MULTI")

		for _, hostname := range route.Hostname {
			conn.Send("DEL", fmt.Sprintf("frontend:%s", hostname))
			conn.Send("RPUSH", fmt.Sprintf("frontend:%s", hostname), hostname)
		}

		if _, err := conn.Do("EXEC"); err != nil {
			log.Fatal(err)
		}
	}

	c := http.Client{}
	res, err := c.Get(fmt.Sprintf("%s/events", routes.DockerApi))
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()

	// Read the streaming json from the events endpoint
	// http://docs.docker.io/en/latest/api/docker_remote_api_v1.3/#monitor-docker-s-events
	d := json.NewDecoder(res.Body)
	for {
		var event Event
		if err := d.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			log.Fatal(err)
		}
		switch event.Status {
		case "start":
			if container := inspectContainer(event.Id, event.Image, c, routes); container != nil {
				notify(container)
			}
		case "stop":
			if container := inspectContainer(event.Id, event.Image, c, routes); container != nil {
				stop(container)
			}
		}
	}
}
