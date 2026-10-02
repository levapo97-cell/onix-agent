// Command onix-agent — demonio que corre EN TU PC (FASE 6).
//
// Conexión SALIENTE a NATS (en prod: TLS + token). Atiende, por request-reply, acciones de
// LISTA BLANCA que la plataforma dispara: listar proyectos, validar si un repo está local y
// clonarlo. Nunca ejecuta comandos arbitrarios. Toda operación queda acotada al directorio base.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nats-io/nats.go"

	"github.com/levapo97-cell/onix-agent/internal/actions"
)

const subjectCmd = "onix.agent.cmd"

type request struct {
	Action string `json:"action"` // project.list | repo.check | repo.clone | session.list | session.close|pause|resume
	Name   string `json:"name"`
	URL    string `json:"url"`
	Pid    int    `json:"pid"`
}

type reply struct {
	OK       bool              `json:"ok"`
	Error    string            `json:"error,omitempty"`
	Projects []actions.Project `json:"projects,omitempty"`
	Sessions []actions.Session `json:"sessions,omitempty"`
	Exists   *bool             `json:"exists,omitempty"`
	Path     string            `json:"path,omitempty"`
	Output   string            `json:"output,omitempty"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	natsURL := env("NATS_URL", nats.DefaultURL)
	baseDir := os.Getenv("ONIX_PROJECTS_DIR")
	run := actions.New(baseDir)
	slog.Info("onix-agent arrancando", "nats_url", natsURL, "base_dir", run.BaseDir)

	opts := []nats.Option{nats.Name("onix-agent"), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1)}
	if tok := os.Getenv("NATS_TOKEN"); tok != "" {
		opts = append(opts, nats.Token(tok))
	}
	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		slog.Error("no se pudo conectar a NATS", "err", err)
		os.Exit(1)
	}
	defer nc.Close()

	sub, err := nc.QueueSubscribe(subjectCmd, "onix-agent", func(m *nats.Msg) {
		var req request
		_ = json.Unmarshal(m.Data, &req)
		resp := handle(run, req)
		data, _ := json.Marshal(resp)
		_ = m.Respond(data)
		slog.Info("acción atendida", "action", req.Action, "ok", resp.OK)
	})
	if err != nil {
		slog.Error("no se pudo suscribir", "err", err)
		os.Exit(1)
	}
	defer sub.Drain()
	slog.Info("onix-agent listo", "subject", subjectCmd)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	slog.Info("apagando…")
}

func handle(run *actions.Runner, req request) reply {
	switch req.Action {
	case "project.list":
		ps, err := run.List()
		if err != nil {
			return reply{Error: err.Error()}
		}
		return reply{OK: true, Projects: ps}
	case "repo.check":
		exists, path, err := run.Check(req.Name)
		if err != nil {
			return reply{Error: err.Error()}
		}
		return reply{OK: true, Exists: &exists, Path: path}
	case "repo.clone":
		path, out, err := run.Clone(req.URL)
		if err != nil {
			return reply{Error: err.Error(), Output: out}
		}
		return reply{OK: true, Path: path, Output: out}
	case "session.list":
		ss, err := run.Sessions()
		if err != nil {
			return reply{Error: err.Error()}
		}
		return reply{OK: true, Sessions: ss}
	case "session.close", "session.pause", "session.resume":
		act := strings.TrimPrefix(req.Action, "session.")
		if err := run.SignalSession(req.Pid, act); err != nil {
			return reply{Error: err.Error()}
		}
		return reply{OK: true}
	default:
		return reply{Error: "acción no permitida: " + req.Action}
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
