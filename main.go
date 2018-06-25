package main

import (
	"log"
	"net/http"

	"github.com/ServiceComb/go-chassis"
	"github.com/ServiceComb/go-chassis/server/restful"
)

type Service struct{}

func (s *Service) HelloWorld(c *restful.Context) {
	c.Write([]byte("hello world"))
}

func (r *Service) URLPatterns() []restful.Route {
	return []restful.Route{
		{http.MethodGet, "/testGo", "HelloWorld"},
	}
}

func main() {
	chassis.RegisterSchema("rest", &Service{})
	if err := chassis.Init(); err != nil {
		log.Print("Init failed", err)
		return
	}
	chassis.Run()
}
