package controllers

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/convox/kernel/Godeps/_workspace/src/github.com/ddollar/logger"
	docker "github.com/convox/kernel/Godeps/_workspace/src/github.com/fsouza/go-dockerclient"
	"github.com/convox/kernel/Godeps/_workspace/src/github.com/gorilla/mux"
	"github.com/convox/kernel/Godeps/_workspace/src/golang.org/x/net/websocket"

	"github.com/convox/kernel/helpers"
	"github.com/convox/kernel/models"
)

func BuildList(rw http.ResponseWriter, r *http.Request) {
	log := buildsLogger("list").Start()

	vars := mux.Vars(r)
	app := vars["app"]

	l := map[string]string{
		"id":      r.URL.Query().Get("id"),
		"created": r.URL.Query().Get("created"),
	}

	builds, err := models.ListBuilds(app, l)

	if err != nil {
		helpers.Error(log, err)
		RenderError(rw, err)
		return
	}

	a, err := models.GetApp(app)

	if err != nil {
		helpers.Error(log, err)
		RenderError(rw, err)
		return
	}

	params := map[string]interface{}{
		"App":    a,
		"Builds": builds,
	}

	if len(builds) > 0 {
		params["Last"] = builds[len(builds)-1]
	}

	switch r.Header.Get("Content-Type") {
	case "application/json":
		RenderJson(rw, builds)
	default:
		RenderPartial(rw, "app", "builds", params)
	}
}

func BuildGet(rw http.ResponseWriter, r *http.Request) {
	log := buildsLogger("list").Start()

	vars := mux.Vars(r)
	app := vars["app"]
	build := vars["build"]

	b, err := models.GetBuild(app, build)

	if err != nil {
		helpers.Error(log, err)
		RenderError(rw, err)
		return
	}

	RenderJson(rw, b)
}

func BuildCreate(rw http.ResponseWriter, r *http.Request) {
	log := buildsLogger("create").Start()

	err := r.ParseMultipartForm(100 * 1024 * 1024)

	if err != nil {
		helpers.Error(log, err)
		RenderError(rw, err)
		return
	}

	app := mux.Vars(r)["app"]

	build := models.NewBuild(app)

	err = build.Save()

	if err != nil {
		helpers.Error(log, err)
		RenderError(rw, err)
		return
	}

	log.Success("step=build.save app=%q", build.App)

	source, _, err := r.FormFile("source")

	if err != nil && err != http.ErrMissingFile {
		helpers.Error(log, err)
		RenderError(rw, err)
		return
	}

	ch := make(chan error)

	if source != nil {
		go build.ExecuteLocal(source, ch)

		if err = <-ch; err != nil {
			RenderError(rw, err)
		} else {
			RenderText(rw, build.Id)
		}

		return
	}

	if err == http.ErrMissingFile {
		if repo := r.FormValue("repo"); repo != "" {
			go build.ExecuteRemote(repo, ch)

			if err = <-ch; err != nil {
				RenderError(rw, err)
			} else {
				RenderText(rw, build.Id)
			}

			return
		}
	}

	err = fmt.Errorf("no source or repo")
	helpers.Error(log, err)
	RenderError(rw, err)
}

func BuildLogs(ws *websocket.Conn) {
	defer ws.Close()

	log := buildsLogger("logs").Start()

	vars := mux.Vars(ws.Request())
	id := vars["build"]

	log.Success("step=upgrade build=%q", id)

	defer ws.Close()

	// proxy to docker container logs
	// https://docs.docker.com/reference/api/docker_remote_api_v1.19/#get-container-logs
	client, err := docker.NewClient("unix:///var/run/docker.sock")

	if err != nil {
		helpers.Error(log, err)
		ws.Write([]byte(fmt.Sprintf("error: %s\n", err)))
		return
	}

	r, w := io.Pipe()

	quit := make(chan bool)

	go scanLines(r, ws)
	go keepAlive(ws, quit)

	err = client.Logs(docker.LogsOptions{
		Container:    fmt.Sprintf("build-%s", id),
		Follow:       true,
		Stdout:       true,
		Stderr:       true,
		Tail:         "all",
		RawTerminal:  false,
		OutputStream: w,
		ErrorStream:  w,
	})

	quit <- true

	if err != nil {
		helpers.Error(log, err)
		ws.Write([]byte(fmt.Sprintf("error: %s\n", err)))
		return
	}
}

func BuildStatus(rw http.ResponseWriter, r *http.Request) {
	log := buildsLogger("status").Start()

	vars := mux.Vars(r)
	app := vars["app"]
	id := vars["build"]

	build, err := models.GetBuild(app, id)

	if err != nil {
		helpers.Error(log, err)
		RenderError(rw, err)
		return
	}

	RenderText(rw, build.Status)
}

func buildsLogger(at string) *logger.Logger {
	return logger.New("ns=kernel cn=builds").At(at)
}

func scanLines(r io.Reader, ws *websocket.Conn) {
	scanner := bufio.NewScanner(r)

	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "|", 2)

		if len(parts) < 2 {
			ws.Write([]byte(parts[0] + "\n"))
			continue
		}

		switch parts[0] {
		case "manifest":
		case "error":
			ws.Write([]byte(parts[1] + "\n"))
		default:
			ws.Write([]byte(parts[1] + "\n"))
		}
	}
}

func keepAlive(ws *websocket.Conn, quit chan bool) {
	c := time.Tick(5 * time.Second)
	b := []byte{}

	for {
		select {
		case <-c:
			ws.Write(b)
		case <-quit:
			return
		}
	}
}
