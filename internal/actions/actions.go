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
	"strings"
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

var urlName = regexp.MustCompile(`([^/]+?)(?:\.git)?/?$`)

func repoName(url string) string {
	url = strings.TrimSpace(url)
	m := urlName.FindStringSubmatch(url)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
