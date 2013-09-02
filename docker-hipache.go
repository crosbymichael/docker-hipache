package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/BurntSushi/toml"
	"io"
	"log"
	"net/http"
	"os"
	"path"
)

type Event struct {
	ContainerId string `json:"id"`
	Status      string `json:"status"`
	Image       string `json:"from"`
}

type ContainerConfig struct {
	Hostname string
}

type NetworkSettings struct {
	IpAddress   string
	PortMapping map[string]map[string]string
}

type Container struct {
	Id              string
	Image           string
	Config          *ContainerConfig
	NetworkSettings *NetworkSettings
}

type Config struct {
	RedisProto string               `toml:"redisproto"`
	RedisAddr  string               `toml:"redisaddr"`
	DockerApi  string               `toml:"docker"`
	Routes     map[string]RouteHost `toml:"routes"`
}

type RouteHost struct {
	Hostnames []string `toml:"hostname"`
	Port      string   `toml:"port"`
}

type Handler func(c *Container) error

func inspectContainer(id string, image string, c http.Client, config Config) (*Container, error) {
	// Use the container id to fetch the container json from the Remote API
	// http://docs.docker.io/en/latest/api/docker_remote_api_v1.4/#inspect-a-container
	res, err := c.Get(fmt.Sprintf("%s/containers/%s/json", config.DockerApi))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusOK {
		d := json.NewDecoder(res.Body)

		var container Container
		if err = d.Decode(&container); err != nil {
			return nil, err
		}
		container.Image = image
		return &container, nil
	}
	return nil, fmt.Errorf("Invalid status code from api: %d", res.StatusCode)
}

func initRoutes(config Config, h *Hipache) error {
	for _, route := range config.Routes {
		for _, hostname := range route.Hostnames {
			if err := h.CreateRoute(hostname); err != nil {
				return err
			}
		}
	}
	return nil
}

func setupHandlers(h *Hipache) map[string]Handler {
	out := make(map[string]Handler, 2)

	out["start"] = h.AddRoute

	out["stop"] = func(c *Container) error {
		hostPort, err := h.FetchRoutes(c)
		if err != nil {
			return err
		}

		for host, port := range hostPort {
			if err := h.RemoveRoute(host, port); err != nil {
				return err
			}
		}
		return nil
	}

	return out
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	var config Config
	configPath := flag.String("conf", path.Join(cwd, "docker-hipache.toml"), "Path to the toml config file")
	if _, err := toml.DecodeFile(*configPath, &config); err != nil {
		log.Fatalf("Could not load config: %s", err)
		return
	}

	h := NewHipache(config.RedisProto, config.RedisAddr, config.Routes)

	if err := initRoutes(config, h); err != nil {
		log.Fatalf("Could not init routes: %s", err)
		return
	}

	handlers := setupHandlers(h)

	c := http.Client{}
	res, err := c.Get(fmt.Sprintf("%s/events", config.DockerApi))
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()

	// Read the streaming json from the events endpoint
	// http://docs.docker.io/en/latest/api/docker_remote_api_v1.3/#monitor-docker-s-events
	d := json.NewDecoder(res.Body)
	for {
		log.Println("New event received")

		var event Event
		if err := d.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			log.Fatal(err)
		}
		if handler, exists := handlers[event.Status]; exists {
			container, err := inspectContainer(event.ContainerId, event.Image, c, config)
			if err != nil {
				log.Fatalf("Could not inspect container: %s", err)
				continue
			}

			if err := handler(container); err != nil {
				log.Fatalf("Could not handle event. Id: %s, Image: %s, Error: %s", event.ContainerId, event.Image, err)
				continue
			}
		}
	}
	log.Println("Event stream complete, shutting down")
}
