// Package actions: operaciones de LISTA BLANCA que el onix-agent ejecuta en la PC del jefe.
// Solo listar proyectos, validar si un repo está local y clonarlo dentro del directorio base.
// Nunca comandos arbitrarios.
package actions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func execTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// Project describe una carpeta del directorio base.
type Project struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsGit bool   `json:"is_git"`
}

// Runner ejecuta las acciones acotado a un directorio base.
type Runner struct{ BaseDir string }

func New(baseDir string) *Runner {
	if baseDir == "" {
		home, _ := os.UserHomeDir()
		baseDir = filepath.Join(home, "Documents", "Proyectos")
	}
	return &Runner{BaseDir: baseDir}
}

// List devuelve las carpetas del directorio base (marcando cuáles son repos git).
func (r *Runner) List() ([]Project, error) {
	entries, err := os.ReadDir(r.BaseDir)
	if err != nil {
		return nil, err
	}
	var out []Project
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(r.BaseDir, e.Name())
		out = append(out, Project{Name: e.Name(), Path: p, IsGit: isGit(p)})
	}
	return out, nil
}

// Check indica si un repo (por nombre) está presente localmente.
func (r *Runner) Check(name string) (bool, string, error) {
	dest, err := r.safeDest(name)
	if err != nil {
		return false, "", err
	}
	if st, err := os.Stat(dest); err == nil && st.IsDir() {
		return true, dest, nil
	}
	return false, dest, nil
}

// Clone clona una URL dentro del directorio base si no existe ya. Devuelve la ruta local.
func (r *Runner) Clone(url string) (string, string, error) {
	name := repoName(url)
	if name == "" {
		return "", "", fmt.Errorf("no pude derivar el nombre del repo de la URL")
	}
	dest, err := r.safeDest(name)
	if err != nil {
		return "", "", err
	}
	if st, err := os.Stat(dest); err == nil && st.IsDir() {
		return dest, "ya existía (no se clonó de nuevo)", nil
	}
	ctx, cancel := execTimeout(5 * time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", url, dest)
	outB, err := cmd.CombinedOutput()
	if err != nil {
		return "", string(outB), fmt.Errorf("git clone falló: %w", err)
	}
	return dest, string(outB), nil
}

// safeDest garantiza que el destino quede DENTRO del directorio base (anti path traversal).
func (r *Runner) safeDest(name string) (string, error) {
	clean := filepath.Base(filepath.Clean(name)) // descarta separadores y ".."
	if clean == "" || clean == "." || clean == ".." {
		return "", fmt.Errorf("nombre de repo inválido")
	}
	return filepath.Join(r.BaseDir, clean), nil
}

func isGit(path string) bool {
	st, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && st.IsDir()
}

// ─────────────── Sesiones de Claude Code (cerrar/pausar/reanudar) ───────────────
// Lista blanca estricta: solo actúa sobre procesos cuyo comando contiene "claude"
// (el CLI de Claude Code). Nunca mata PIDs arbitrarios.

type Session struct {
	Pid     int    `json:"pid"`
	Command string `json:"command"`
}

// Sessions lista los procesos de Claude Code en ejecución en la PC.
func (r *Runner) Sessions() ([]Session, error) {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sp := strings.SplitN(line, " ", 2)
		if len(sp) < 2 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(sp[0]))
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(sp[1])
		// Solo sesiones reales de Claude Code (ejecutable == claude), no cualquier ruta con "claude".
		if !looksLikeClaude(cmd) {
			continue
		}
		if len(cmd) > 160 {
			cmd = cmd[:160] + "…"
		}
		sessions = append(sessions, Session{Pid: pid, Command: cmd})
	}
	return sessions, nil
}

// looksLikeClaude: el ejecutable del proceso es "claude" (no una ruta cualquiera que lo contenga).
func looksLikeClaude(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	if filepath.Base(fields[0]) == "claude" {
		return true
	}
	// o algún token es una ruta a un ejecutable claude (…/claude)
	for _, f := range fields {
		if strings.Contains(f, "/") && filepath.Base(f) == "claude" {
			return true
		}
	}
	return false
}

// isClaude verifica que el PID corresponde a un proceso de Claude Code (anti-kill arbitrario).
func (r *Runner) isClaude(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	return looksLikeClaude(strings.TrimSpace(string(out)))
}

// SignalSession envía la señal correspondiente SOLO si el PID es un proceso de Claude Code.
// action: "close" (SIGTERM), "pause" (SIGSTOP), "resume" (SIGCONT).
func (r *Runner) SignalSession(pid int, action string) error {
	if pid <= 1 {
		return fmt.Errorf("pid inválido")
	}
	if !r.isClaude(pid) {
		return fmt.Errorf("el PID %d no es una sesión de Claude Code (no se toca)", pid)
	}
	var sig syscall.Signal
	switch action {
	case "close":
		sig = syscall.SIGTERM
	case "pause":
		sig = syscall.SIGSTOP
	case "resume":
		sig = syscall.SIGCONT
	default:
		return fmt.Errorf("acción de sesión no permitida: %s", action)
	}
	return syscall.Kill(pid, sig)
}

var urlName = regexp.MustCompile(`([^/]+?)(?:\.git)?/?$`)

func repoName(url string) string {
	url = strings.TrimSpace(url)
	m := urlName.FindStringSubmatch(url)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
