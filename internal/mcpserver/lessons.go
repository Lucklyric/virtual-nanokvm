package mcpserver

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Lessons are short notes an agent keeps per app about what made it slow and
// what worked, so the next task in that app plans better. They live on this
// Mac only, outside the plugin and the source checkout, which updates replace.
//
// Lessons are versioned: each revision (set) bumps the version, and each line
// is tagged with the version it was added under. Task records (one per finished task,
// with what it cost) carry the version in force, so a set of lessons can be
// judged by the tasks run under it and a bad change traced to its lines. The
// app name "all" holds rules for every app.

const (
	maxLessons = 40
	maxTasks   = 50
	globalApp  = "all"
)

func lessonsDir() string {
	if d := os.Getenv("VNKVM_LESSONS_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vnkvm", "lessons")
}

var unsafeName = regexp.MustCompile(`[^a-z0-9]+`)

// appFile is where app's data of one kind (".md", ".tasks.jsonl", ".recipes.json") lives.
func appFile(app, ext string) (string, error) {
	name := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(app), "-"), "-")
	if name == "" {
		return "", fmt.Errorf("give the app name, e.g. Safari")
	}
	return filepath.Join(lessonsDir(), name+ext), nil
}

func lessonsFile(app string) (string, error) { return appFile(app, ".md") }

var (
	versionHeader = regexp.MustCompile(`\(v(\d+)\)\s*$`)
	lessonTag     = regexp.MustCompile(`^(v\d+ )?(\d{4}-\d{2}-\d{2}: )?`)
)

// readLessons returns app's lessons and their version (0 when none).
func readLessons(app string) ([]string, int, error) {
	path, err := lessonsFile(app)
	if err != nil {
		return nil, 0, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	var out []string
	version := 0
	for _, l := range strings.Split(string(b), "\n") {
		if m := versionHeader.FindStringSubmatch(l); strings.HasPrefix(l, "#") && m != nil {
			version, _ = strconv.Atoi(m[1])
		}
		if l = strings.TrimSpace(strings.TrimPrefix(l, "- ")); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out, version, err
}

// writeLessons keeps the newest maxLessons lines.
func writeLessons(app string, lines []string, version int) error {
	path, err := lessonsFile(app)
	if err != nil {
		return err
	}
	if len(lines) > maxLessons {
		lines = lines[len(lines)-maxLessons:]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# vnkvm lessons: %s (v%d)\n\n", app, version)
	for _, l := range lines {
		b.WriteString("- " + l + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func tag(version int, text string) string {
	return fmt.Sprintf("v%d %s: %s", version, time.Now().Format("2006-01-02"), text)
}

func addLesson(app, text string) ([]string, int, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return nil, 0, fmt.Errorf("the lesson is empty")
	}
	if len(text) > 300 {
		return nil, 0, fmt.Errorf("keep a lesson under 300 characters")
	}
	lines, version, err := readLessons(app)
	if err != nil {
		return nil, 0, err
	}
	for _, l := range lines {
		if strings.Contains(strings.ToLower(lessonTag.ReplaceAllString(l, "")), strings.ToLower(text)) {
			return lines, version, nil // already known
		}
	}
	// A lesson added between revisions belongs to the version in force, so
	// that version's task records measure it too.
	lines = append(lines, tag(version, text))
	return lines, version, writeLessons(app, lines, version)
}

// setLessons replaces app's lessons. Lines that keep their tag keep their
// version; new or rewritten lines are tagged with the new one.
func setLessons(app string, set []string) ([]string, int, error) {
	_, version, err := readLessons(app)
	if err != nil {
		return nil, 0, err
	}
	version++
	var lines []string
	for _, l := range set {
		if l = strings.Join(strings.Fields(l), " "); l == "" {
			continue
		}
		if lessonTag.FindString(l) == "" { // a kept line keeps its tag
			l = tag(version, l)
		}
		lines = append(lines, l)
	}
	return lines, version, writeLessons(app, lines, version)
}

// appSummary is what is kept for one app.
type appSummary struct {
	App     string `json:"app"`
	Version int    `json:"version"`
	Lessons int    `json:"lessons"`
	Tasks   int    `json:"tasks"`
	Recipes int    `json:"recipes"`
	Review  bool   `json:"review_due,omitempty"`
}

// listApps summarizes every app with lessons, task records or recipes.
func listApps() ([]appSummary, error) {
	ents, err := os.ReadDir(lessonsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []appSummary
	seen := map[string]bool{}
	for _, e := range ents {
		name := e.Name()
		for _, ext := range []string{".tasks.jsonl", ".recipes.json", ".md"} {
			if app, ok := strings.CutSuffix(name, ext); ok && !seen[app] {
				seen[app] = true
				lines, v, _ := readLessons(app)
				tasks, _ := readTasks(app)
				rs, _ := readRecipes(app)
				out = append(out, appSummary{App: app, Version: v, Lessons: len(lines), Tasks: len(tasks), Recipes: len(rs),
					Review: review(app, v, tasks) != ""})
				break
			}
		}
	}
	return out, nil
}

// task is the record of one finished task: what it cost, under which lessons.
type task struct {
	Date     string `json:"date"`
	Task     string `json:"task"`
	Version  int    `json:"lessons_version"`
	Calls    int    `json:"tool_calls"`
	Shots    int    `json:"shots"`
	Acts     int    `json:"act_calls"`
	Steps    int    `json:"steps"`
	Failures int    `json:"failures"`
	Expects  int    `json:"expects,omitempty"`
	Misses   int    `json:"expect_misses,omitempty"`
	Recipes  int    `json:"recipe_runs,omitempty"`
}

func readTasks(app string) ([]task, error) {
	path, err := appFile(app, ".tasks.jsonl")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	var out []task
	for _, l := range strings.Split(string(b), "\n") {
		var t task
		if json.Unmarshal([]byte(l), &t) == nil && t.Date != "" {
			out = append(out, t)
		}
	}
	return out, err
}

// addTask appends t and keeps the newest maxTasks records.
func addTask(app string, t task) ([]task, error) {
	tasks, err := readTasks(app)
	if err != nil {
		return nil, err
	}
	tasks = append(tasks, t)
	if len(tasks) > maxTasks {
		tasks = tasks[len(tasks)-maxTasks:]
	}
	var b strings.Builder
	for _, t := range tasks {
		j, _ := json.Marshal(t)
		b.Write(append(j, '\n'))
	}
	path, _ := appFile(app, ".tasks.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return tasks, os.WriteFile(path, []byte(b.String()), 0o600)
}

// versionStat is how the tasks run under one lessons version went.
type versionStat struct {
	Version      int     `json:"version"`
	Tasks        int     `json:"tasks"`
	CallsPerTask float64 `json:"tool_calls_per_task"`
	StepsPerCall float64 `json:"steps_per_call"`
	MissRate     float64 `json:"expect_miss_rate,omitempty"`
}

func byVersion(tasks []task) []versionStat {
	var out []versionStat
	idx := map[int]int{}
	type sum struct{ calls, steps, expects, misses int }
	sums := map[int]*sum{}
	for _, t := range tasks {
		if _, ok := idx[t.Version]; !ok {
			idx[t.Version] = len(out)
			out = append(out, versionStat{Version: t.Version})
			sums[t.Version] = &sum{}
		}
		out[idx[t.Version]].Tasks++
		s := sums[t.Version]
		s.calls += t.Calls
		s.steps += t.Steps
		s.expects += t.Expects
		s.misses += t.Misses
	}
	for i := range out {
		s := sums[out[i].Version]
		out[i].CallsPerTask = round1(float64(s.calls) / float64(out[i].Tasks))
		if s.calls > 0 {
			out[i].StepsPerCall = round1(float64(s.steps) / float64(s.calls))
		}
		if s.expects > 0 {
			out[i].MissRate = round1(float64(s.misses) / float64(s.expects))
		}
	}
	return out
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// review says when app's lessons are due for a revision: after every fifth
// task on one version, or when the current version runs clearly worse than
// the one before it.
func review(app string, version int, tasks []task) string {
	stats := byVersion(tasks)
	var cur, prev *versionStat
	for i := range stats {
		switch {
		case stats[i].Version == version:
			cur = &stats[i]
		case stats[i].Version < version && stats[i].Tasks >= 3 && (prev == nil || stats[i].Version > prev.Version):
			prev = &stats[i]
		}
	}
	if cur == nil {
		return ""
	}
	if prev != nil && cur.Tasks >= 3 && cur.CallsPerTask > prev.CallsPerTask*1.25 {
		return fmt.Sprintf("Review %s lessons: tasks under v%d take %.1f tool calls on average, v%d took %.1f. "+
			"The lines tagged after v%d may be hurting: rewrite or drop them with set (unless the recent tasks were simply harder).",
			app, version, cur.CallsPerTask, prev.Version, prev.CallsPerTask, prev.Version)
	}
	if cur.Tasks%5 == 0 {
		return fmt.Sprintf("Review %s lessons: %d tasks since v%d. Read them with the task records; keep what helped, "+
			"merge duplicates, drop what did not, add what the last tasks taught, and save with set.", app, cur.Tasks, version)
	}
	return ""
}

// stats counts what this server process has done, to spot an inefficient
// loop the model cannot see from inside one turn.
type stats struct {
	mu sync.Mutex
	counters
	base          counters // at the last finished task
	saveHinted    map[string]bool
	lookStreak    int // screenshots since the last action
	singleStreak  int // single-action calls in a row
	lastFail      string
	lastFailCount int
}

type counters struct {
	Calls    int            `json:"tool_calls"`
	Shots    int            `json:"shots"`
	Zooms    int            `json:"zooms"`
	Acts     int            `json:"act_calls"`
	Singles  int            `json:"single_action_calls"`
	Steps    int            `json:"steps_run"`
	Failures map[string]int `json:"failures"`
	Expects  int            `json:"expects"`
	Misses   int            `json:"expect_misses"`
	Recipes  int            `json:"recipe_runs"`
}

func (s *stats) call() {
	s.mu.Lock()
	s.Calls++
	s.mu.Unlock()
}

func (s *stats) predicted(expects, misses int, recipe bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Expects += expects
	s.Misses += misses
	if recipe {
		s.Recipes++
	}
}

// saveHint suggests saving a sequence as a recipe, once per app and session.
func (s *stats) saveHint(app string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if app == "" || s.saveHinted[app] {
		return ""
	}
	if s.saveHinted == nil {
		s.saveHinted = map[string]bool{}
	}
	s.saveHinted[app] = true
	return "If this sequence will come up again in " + app + ", save it with recipes (save), so the next time is one call."
}

// finish closes a task: it returns what the task cost since the last one.
func (s *stats) finish(summary string, version int) task {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, b := s.counters, s.base
	fails := 0
	for k, v := range c.Failures {
		fails += v - b.Failures[k]
	}
	t := task{
		Date: time.Now().Format("2006-01-02"), Task: summary, Version: version,
		Calls: c.Calls - b.Calls, Shots: c.Shots - b.Shots, Acts: c.Acts + c.Singles - b.Acts - b.Singles,
		Steps: c.Steps - b.Steps, Failures: fails, Expects: c.Expects - b.Expects, Misses: c.Misses - b.Misses,
		Recipes: c.Recipes - b.Recipes,
	}
	s.base = c
	s.base.Failures = map[string]int{}
	for k, v := range c.Failures {
		s.base.Failures[k] = v
	}
	return t
}

func (s *stats) shot(zoom bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Shots++
	if zoom {
		s.Zooms++
	}
	s.lookStreak++
	if s.lookStreak == 3 {
		return "Efficiency: 3 screenshots without an action. Plan from the shot you have, and zoom only where a target is unclear."
	}
	return ""
}

func (s *stats) action(single bool, steps int, err error) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookStreak = 0
	s.Steps += steps
	var hints []string
	if single {
		s.Singles++
		s.singleStreak++
		if s.singleStreak == 3 {
			hints = append(hints, "Efficiency: 3 single actions in a row. Chain the steps you can already see into one act call.")
		}
	} else {
		s.Acts++
		s.singleStreak = 0
	}
	if err != nil {
		kind := failureKind(err)
		if s.Failures == nil {
			s.Failures = map[string]int{}
		}
		s.Failures[kind]++
		if kind == s.lastFail {
			s.lastFailCount++
		} else {
			s.lastFail, s.lastFailCount = kind, 1
		}
		if s.lastFailCount == 2 && kind != "user took control" {
			hints = append(hints, fmt.Sprintf("Efficiency: the same failure (%s) twice. Change the approach, and record what works with the lessons tool.", kind))
		}
	}
	return strings.Join(hints, "\n")
}

func (s *stats) snapshot() counters {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.counters
	c.Failures = map[string]int{}
	for k, v := range s.Failures {
		c.Failures[k] = v
	}
	return c
}

func failureKind(err error) string {
	msg := err.Error()
	for _, k := range []string{"user took control", "covered", "changed size", "is gone", "outside", "unknown shot"} {
		if strings.Contains(msg, k) {
			return k
		}
	}
	return "other"
}
