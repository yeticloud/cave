package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	rice "github.com/GeertJohan/go.rice"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shirou/gopsutil/host"
	"go.etcd.io/bbolt"
)

// TODO: API stuff here

const (
	// APIPREFIX path
	APIPREFIX = "/api/v1/"

	// KVPREFIX path
	KVPREFIX = "/api/v1/kv/"

	// UIPREFIX path
	UIPREFIX = "/ui/"

	// SYSPREFIX path
	SYSPREFIX = "/system"
)

type jsonError struct {
	Message string `json:"message,omitempty"`
}

// API Type
type API struct {
	app       *Cave
	config    *Config
	log       *Log
	terminate chan bool
	kv        *KV
	http      *echo.Echo
}

//NewAPI function
func NewAPI(app *Cave) (*API, error) {
	a := &API{
		app:    app,
		config: app.Config,
		log:    app.Logger,
		kv:     app.KV,
	}
	a.terminate = make(chan bool)
	a.http = echo.New()
	a.http.HideBanner = true
	a.http.HidePort = true
	a.http.Debug = false
	//a.http.Use(middleware.Recover())
	a.http.Use(a.log.EchoLogger("/api/v1/perf/metrics", "/api/v1/perf/logs"))
	// UI
	fs := rice.MustFindBox("./ui/").HTTPBox()
	a.http.GET("/", echo.WrapHandler(http.FileServer(fs)))
	a.http.GET("/ui/*", echo.WrapHandler(http.StripPrefix("/ui/", http.FileServer(fs))))
	a.http.Any("/api/v1/plugin/*", a.PluginHandler)
	a.http.Any("/api/v1/kv/", a.kvHandler)
	a.http.Any("/api/v1/kv/*", a.kvHandler)
	a.http.POST(APIPREFIX+"login", a.routeLogin)
	a.http.GET(APIPREFIX+"cluster/nodes", a.routeClusterNodes)
	a.http.POST("/api/v1/query", a.multiQueryHandler)
	// PERF GROUP
	perf := a.http.Group(APIPREFIX + "perf")
	perf.GET("/logs", a.routeLogs)
	perf.GET("/metrics", echo.WrapHandler(promhttp.Handler()))
	perf.GET("/dashboard", a.routeDashboard)

	system := a.http.Group("/api/v1/system")
	system.GET("/config", a.routeSystemConfig)
	system.GET("/info", a.routeSystemInfo)
	return a, nil
}

// Start starts a new server
func (a *API) Start() {
	go a.watch()
	scheme := "http://"
	if a.config.SSL.Enable {
		scheme = "https://"
		a.log.InfoF(nil, "API listening on %s0.0.0.0:%v", scheme, a.config.API.Port)
		a.log.Error(nil, a.http.StartTLS(fmt.Sprintf("0.0.0.0:%v", a.config.API.Port), a.config.SSL.Certificate, a.config.SSL.Key))
	} else {
		a.log.InfoF(nil, "API listening on %s0.0.0.0:%v", scheme, a.config.API.Port)
		a.log.Error(nil, a.http.Start(fmt.Sprintf("0.0.0.0:%v", a.config.API.Port)))
	}
}

func (a *API) watch() {
	for {
		select {
		case <-a.terminate:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := a.http.Shutdown(ctx)
			if err != nil {
				a.log.Error(nil, err)
			}

			return
		default:
			time.Sleep(500 * time.Millisecond)
		}
	}
}

func (a *API) routeLogin(c echo.Context) error {
	return c.JSON(200, map[string]string{"message": "ok"})
}

func trimPath(path string, prefix string) string {
	return path[len(prefix):]
}

func (a *API) kvHandler(c echo.Context) error {
	switch c.Request().Method {
	case "GET":
		return a.kvGetHandler(c)
	case "POST":
		return a.kvPutHandler(c)
	case "DELETE":
		return a.kvDeleteHandler(c)
	default:
		return c.JSON(405, jsonError{Message: "Method " + c.Request().Method + " is not allowed"})
	}
}

// PluginHandler handles API plugins
func (a *API) PluginHandler(c echo.Context) error {
	path := trimPath(c.Request().URL.Path, "/api/v1/plugin/")
	pathParts := strings.Split(path, "/")
	if _, ok := a.app.Plugins.mgr.Procs["api"][pathParts[0]]; ok {
		var dst interface{}
		var b []byte
		_, err := c.Request().Body.Read(b)
		if err != nil && err.Error() != "EOF" {
			return c.JSON(500, map[string]interface{}{"error": err.Error})
		}
		req := &APIRequest{
			URL:       c.Request().URL,
			Body:      b,
			Headers:   c.Request().Header,
			Host:      c.Request().RemoteAddr,
			UserAgent: c.Request().UserAgent(),
			Cookies:   c.Request().Cookies(),
		}
		urn := fmt.Sprintf("api:%s:http_%s", pathParts[0], strings.ToLower(c.Request().Method))
		err = a.app.Plugins.mgr.Call(urn, &dst, req)
		if err != nil {
			return c.JSON(500, map[string]interface{}{"error": err.Error})
		}
		return c.JSON(200, dst)
	}
	return c.JSON(404, map[string]interface{}{
		"error": "the given path does not exist",
	})

}

func (a *API) treeHandler(c echo.Context, path string) error {
	tree, err := a.kv.GetTree("kv")
	if err != nil {
		return c.JSON(500, jsonError{Message: err.Error()})
	}
	return c.JSON(200, tree)
}

func (a *API) kvGetHandler(c echo.Context) error {
	path := trimPath(c.Request().URL.Path, KVPREFIX)
	if c.Request().URL.Query().Get("tree") != "" {
		return a.treeHandler(c, path)
	}
	if strings.HasSuffix(path, "/") || path == "" {
		k, err := a.kv.GetKeys(path, "kv")
		if err != nil {
			a.log.Error(nil, err)
			return c.JSON(500, jsonError{Message: err.Error()})
		}
		if len(k) == 0 {
			return c.JSON(404, jsonError{
				Message: "Key " + path + " does not exist",
			})
		}
		return c.JSON(200, k)
	}
	b, err := a.kv.Get(path, "kv")
	if err != nil {
		a.log.Error(nil, err)
		return c.JSON(500, jsonError{Message: err.Error()})
	}
	if len(b) == 0 {
		return c.JSON(404, jsonError{Message: "Key " + path + " does not exist"})
	}
	if c.Request().URL.Query().Get("secret") != "" {
		data, err := decryptJSON(a.kv.sharedkey, b)
		if err != nil {
			return c.Blob(200, "application/json", b)
		}
		return c.Blob(200, "application/json", data)
	}
	return c.Blob(200, "application/json", b)
}

func (a *API) kvPutHandler(c echo.Context) error {
	path := trimPath(c.Request().URL.Path, KVPREFIX)
	buf, err := ioutil.ReadAll(c.Request().Body)
	if err != nil {
		a.log.Error(nil, err)
		return c.JSON(400, jsonError{Message: err.Error()})
	}
	secret := false
	if c.Request().URL.Query().Get("secret") != "" {
		secret = true
		data, err := encrytJSON(a.kv.sharedkey, buf)
		if err != nil {
			return c.JSON(400, jsonError{Message: err.Error()})
		}
		buf = data
	}
	err = a.kv.Put(path, buf, "kv", secret)
	if err != nil {
		a.log.Error(nil, err)
		return c.JSON(500, jsonError{Message: err.Error()})
	}
	return c.JSON(200, jsonError{Message: "ok"})
}

func (a *API) kvDeleteHandler(c echo.Context) error {
	path := trimPath(c.Request().URL.Path, KVPREFIX)
	if strings.HasSuffix(path, "/") {
		err := a.kv.DeleteBucket(path, "kv")
		if err != nil {
			if err == bbolt.ErrBucketNotFound {
				return c.JSON(404, jsonError{Message: err.Error()})
			}
			return c.JSON(500, jsonError{Message: err.Error()})
		}
	}
	err := a.kv.DeleteKey(path, "kv")
	if err != nil {
		return c.JSON(500, jsonError{Message: err.Error()})
	}
	return c.JSON(200, jsonError{Message: "ok"})
}

func (a *API) multiQueryHandler(c echo.Context) error {
	buf, err := ioutil.ReadAll(c.Request().Body)
	if err != nil {
		return c.JSON(400, jsonError{Message: err.Error()})
	}
	mq := MultiQuery{}
	err = json.Unmarshal(buf, &mq)
	if err != nil {
		return c.JSON(400, jsonError{Message: err.Error()})
	}
	result := make(chan QueryObject, len(mq.Query))
	for _, q := range mq.Query {
		switch strings.ToUpper(q.Verb) {
		case "GET":
			go a.doGET(q, result)
		case "PUT":
			go a.doPOST(q, result)
		case "POST":
			go a.doPOST(q, result)
		case "DELETE":
			go a.doDELETE(q, result)
		default:
			q.Error = fmt.Sprintf("Verb %s is not a valid operation", q.Verb)
			result <- q
		}
	}
	for {
		if len(result) >= len(mq.Query) {
			close(result)
			break
		}
		time.Sleep(1 * time.Millisecond)
	}
	rq := MultiQuery{
		ID:          uuid.New().String(),
		Query:       []QueryObject{},
		QueryErrors: false,
	}
	for r := range result {
		if r.Error != "" {
			rq.QueryErrors = true
		}
		rq.Query = append(rq.Query, r)
	}
	blob, err := json.Marshal(rq)
	rq.Error = err
	if rq.QueryErrors || err != nil {
		return c.Blob(400, "application/json", blob)
	}
	return c.Blob(200, "application/json", blob)
}

func (a *API) doGET(q QueryObject, result chan QueryObject) {
	b, err := a.kv.Get(q.Key, "kv")
	if err != nil {
		q.Error = err.Error()
		result <- q
		return
	}
	if len(b) == 0 {
		q.Error = fmt.Sprintf("Key %s does not exist", q.Key)
		result <- q
		return
	}
	if q.Secret {
		data, err := decryptJSON(a.kv.sharedkey, b)
		if err != nil {
			q.Value = string(b[:])
		}
		q.Value = string(data[:])
	}
	q.Value = string(b)
	result <- q
	return
}

func (a *API) doPOST(q QueryObject, result chan QueryObject) {
	buf := q.Value
	if q.Secret {
		data, err := encrytJSON(a.kv.sharedkey, q.Value)
		if err != nil {
			q.Error = err.Error()
			result <- q
			return
		}
		buf = string(data[:])
	}
	err := a.kv.Put(q.Key, []byte(buf), "kv", q.Secret)
	if err != nil {
		q.Error = err.Error()
		result <- q
		return
	}
	result <- q
	return
}

func (a *API) doDELETE(q QueryObject, result chan QueryObject) {
	err := a.kv.DeleteKey(q.Key, "kv")
	if err != nil {
		q.Error = err.Error()
		result <- q
		return
	}
	result <- q
	return
}

func (a *API) routeClusterNodes(c echo.Context) error {
	if a.config.Mode == "dev" {
		m := map[string]interface{}{}
		m["mode"] = "dev"
		m["nodes"] = append([]map[string]string{}, map[string]string{"Address": a.app.Cluster.advertiseHost, "public_key": ""})
		return c.JSON(200, m)
	}
	m := map[string]interface{}{}
	peers := a.app.Cluster.peers
	self := a.app.Cluster.node.ID()
	m["nodes"] = append(peers, self)
	m["mode"] = "cluster"
	return c.JSON(200, m)
}

func (a *API) routeLogs(c echo.Context) error {
	logs := []string{}
	for i := 0; i <= 100; i++ {
		select {
		case m := <-a.log.logQueue:
			logs = append(logs, m)
		default:
			break
		}
	}
	return c.JSON(200, logs)
}

func (a *API) routeDashboard(c echo.Context) error {
	return c.Blob(200, "application/json", getDashboard())
}

func (a *API) routeSystemConfig(c echo.Context) error {
	return c.JSON(200, a.app.Config)
}

func (a *API) routeSystemInfo(c echo.Context) error {
	i := map[string]interface{}{}

	info, err := host.Info()
	if err != nil {
		return c.JSON(500, err.Error)
	}
	i["os"] = info
	i["env"] = os.Environ()
	return c.JSON(200, i)
}
