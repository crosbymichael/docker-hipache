package main

import (
	"fmt"
	"github.com/garyburd/redigo/redis"
	"strings"
)

type Hipache struct {
	pool   *redis.Pool
	routes map[string]RouteHost
}

func NewHipache(proto, addr string, routes map[string]RouteHost) *Hipache {
	pool := redis.NewPool(func() (redis.Conn, error) {
		return redis.Dial(proto, addr)
	}, 10)

	return &Hipache{
		pool:   pool,
		routes: routes,
	}
}

func (h *Hipache) CreateRoute(hostname string) error {
	conn := h.pool.Get()
	defer conn.Close()

	if err := conn.Send("MULTI"); err != nil {
		return err
	}
	key := fmt.Sprintf("frontend:%s", hostname)
	if err := conn.Send("DEL", key); err != nil {
		return err
	}
	if err := conn.Send("RPUSH", key, hostname); err != nil {
		return err
	}
	if _, err := conn.Do("EXEC"); err != nil {
		return err
	}
	return nil
}

func (h *Hipache) AddRoute(c *Container) error {
	if ports := getTcpPorts(c); ports != nil {
		for privatePort, publicPort := range ports {
			if route := h.containsRoute(c, privatePort); route != nil {
				conn := h.pool.Get()
				defer conn.Close()

				if err := conn.Send("MULTI"); err != nil {
					return err
				}

				for _, hostname := range route.Hostnames {
					if err := conn.Send("RPUSH", fmt.Sprintf("frontend:%s", hostname), fmt.Sprintf("http://localhost:%s", publicPort)); err != nil {
						return err
					}
					if err := conn.Send("RPUSH", c.Id, fmt.Sprintf("%s:%s", hostname, publicPort)); err != nil {
						return err
					}
				}

				if _, err := conn.Do("EXEC"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (h *Hipache) RemoveRoute(hostname, publicPort string) error {
	conn := h.pool.Get()
	defer conn.Close()

	if _, err := conn.Do("LREM", fmt.Sprintf("frontend:%s", hostname), -1, fmt.Sprintf("http://localhost:%s", publicPort)); err != nil {
		return err
	}
	return nil
}

func (h *Hipache) FetchRoutes(c *Container) (map[string]string, error) {
	conn := h.pool.Get()
	defer conn.Close()

	reply, err := redis.Strings(conn.Do("LRANGE", c.Id, 0, -1))
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(reply))
	for _, v := range reply {
		parts := strings.Split(v, ":")
		if len(parts) < 2 {
			return nil, fmt.Errorf("Invalid value to split: %s", v)
		}
		out[parts[0]] = parts[1]
	}
	return out, nil
}

func (h *Hipache) containsRoute(c *Container, privatePort string) *RouteHost {
	route, ok := h.routes[c.Image]
	if ok && route.Port == privatePort {
		return &route
	}
	return nil
}

func getTcpPorts(c *Container) map[string]string {
	settings := c.NetworkSettings

	if settings != nil && settings.PortMapping != nil {
		if ports, ok := settings.PortMapping["Tcp"]; ok {
			return ports
		}
	}
	return nil
}
