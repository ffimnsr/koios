package cli

import (
	"fmt"
	"io"

	"github.com/ffimnsr/koios/internal/artifacts"
	"github.com/ffimnsr/koios/internal/bookmarks"
	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/decisions"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/notes"
	"github.com/ffimnsr/koios/internal/plans"
	"github.com/ffimnsr/koios/internal/preferences"
	"github.com/ffimnsr/koios/internal/projects"
	"github.com/ffimnsr/koios/internal/reminder"
	"github.com/ffimnsr/koios/internal/tasks"
)

type dbBootstrapTarget struct {
	name string
	path string
	open func() (io.Closer, error)
}

func bootstrapWorkspaceDBs(state *repoState) ([]string, error) {
	if state == nil || state.Config == nil || state.WorkspaceRoot == "" {
		return nil, nil
	}
	targets := []dbBootstrapTarget{
		{name: "memory", path: state.Config.MemoryDBPath(), open: func() (io.Closer, error) { return memory.New(state.Config.MemoryDBPath(), nil) }},
		{name: "tasks", path: state.Config.TasksDBPath(), open: func() (io.Closer, error) { return tasks.New(state.Config.TasksDBPath()) }},
		{name: "bookmarks", path: state.Config.BookmarksDBPath(), open: func() (io.Closer, error) { return bookmarks.New(state.Config.BookmarksDBPath()) }},
		{name: "calendar", path: state.Config.CalendarDBPath(), open: func() (io.Closer, error) { return calendar.New(state.Config.CalendarDBPath()) }},
		{name: "notes", path: state.Config.NotesDBPath(), open: func() (io.Closer, error) { return notes.New(state.Config.NotesDBPath()) }},
		{name: "plans", path: state.Config.PlansDBPath(), open: func() (io.Closer, error) { return plans.New(state.Config.PlansDBPath()) }},
		{name: "projects", path: state.Config.ProjectsDBPath(), open: func() (io.Closer, error) { return projects.New(state.Config.ProjectsDBPath()) }},
		{name: "artifacts", path: state.Config.ArtifactsDBPath(), open: func() (io.Closer, error) { return artifacts.New(state.Config.ArtifactsDBPath()) }},
		{name: "decisions", path: state.Config.DecisionsDBPath(), open: func() (io.Closer, error) { return decisions.New(state.Config.DecisionsDBPath()) }},
		{name: "preferences", path: state.Config.PreferencesDBPath(), open: func() (io.Closer, error) { return preferences.New(state.Config.PreferencesDBPath()) }},
		{name: "reminders", path: state.Config.RemindersDBPath(), open: func() (io.Closer, error) { return reminder.New(state.Config.RemindersDBPath()) }},
	}
	created := make([]string, 0, len(targets))
	for _, target := range targets {
		existed := fileExists(target.path)
		store, err := target.open()
		if err != nil {
			return created, fmt.Errorf("initialize %s db: %w", target.name, err)
		}
		if err := store.Close(); err != nil {
			return created, fmt.Errorf("close %s db: %w", target.name, err)
		}
		if !existed && fileExists(target.path) {
			created = append(created, target.path)
		}
	}
	return created, nil
}
